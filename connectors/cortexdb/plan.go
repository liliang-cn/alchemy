package cortexdb

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// The identifiers this connector writes under. Every one of them is namespaced
// by the run, which is the whole of "two runs are two graphs".
//
// The "entity:" prefix is not decoration: cortexdb.EntityNodeIDPrefix is the
// signal CortexDB reads to mean "the caller has already decided this node's
// identity, do not derive one from the name". Without it every alchemy entity
// would be keyed on its folded name and two runs would fuse.
func entityNodeID(run, id string) string { return "entity:alchemy:" + run + ":" + id }
func chunkNodeID(run string, index int) string {
	return "chunk:alchemy:" + run + ":" + strconv.Itoa(index)
}
func runNodeID(run string) string { return "alchemy:run:" + run }

// documentID maps alchemy's Provenance.Source onto CortexDB's document.
//
// This is the one place the two provenance models line up exactly: CortexDB
// scopes an edge's identity and an entity's source_document_ids by document,
// and alchemy's Source is the file a fact came from. Namespacing it by the run
// keeps two imports of the same file two documents, which is the same decision
// as everything else here.
func documentID(run, source string) string { return "alchemy:" + run + ":" + source }

// edgeGroup is the set of alchemy relations that CortexDB's identity rule makes
// one edge.
//
// The rule is CortexDB's, not a second one invented here:
// graphrag_tool_ingest.relationEdgeID keys an edge on (from, to, type,
// document). Grouping by the same tuple *before* the write is what keeps
// CortexDB's own merge path from firing — it overlays scalar properties, so a
// second member would silently overwrite the first's provenance, and unioning
// them here loses nothing.
type edgeGroup struct {
	from, to, typ, doc string
	members            []int
	// keys is the distinct set of non-empty Relation.Key values in the group.
	// More than one means the producer said these were different edges and
	// CortexDB has nowhere to put the difference.
	keys []string
}

// plan is a whole result checked and ready to write. Nothing reaches the store
// until one of these exists. It holds indexes into the result rather than
// copies of it, for §8.4's reason: a connector that doubles a
// four-hundred-thousand-record graph in memory in order to check it dies on
// the import it was bought for.
type plan struct {
	res    alchemy.Result
	opts   Options
	digest string

	entities []int
	groups   []edgeGroup
	// chunks are the indexes of Result.Chunks that will be written, and
	// vectorFor maps a chunk index to the embedding alchemy computed for it.
	chunks    []int
	vectorFor map[int]int
	// dim is the dimension every vector shares. A collection has one.
	dim int

	sourceSeen map[string]struct{}

	// skipped names the relations that will not be written, and why, so that
	// "the graph is missing an edge" is never something a buyer discovers by
	// counting.
	skipped []string
	// fused names the groups CortexDB's identity rule collapsed. See
	// Options.FuseParallelEdges.
	fused []string
	// sources is every distinct Provenance.Source and Chunk.Source in the
	// result, in the order first seen. Each becomes one CortexDB document.
	sources []string
}

// source records that this run touched a file, once. It is called from
// everywhere a Source appears rather than from one place, because a source that
// only violations mention is still a file this run read.
func (p *plan) source(name string) {
	if _, seen := p.sourceSeen[name]; seen {
		return
	}
	if p.sourceSeen == nil {
		p.sourceSeen = map[string]struct{}{}
	}
	p.sourceSeen[name] = struct{}{}
	p.sources = append(p.sources, name)
}

// cortexNodeProps and cortexEdgeProps are the property names CortexDB writes
// on its own nodes and edges. A source attribute landing on one of them would
// not collide loudly — it would quietly change what CortexDB thinks the record
// is — so it is refused by name, and the refusal names the knob that frees it.
var cortexNodeProps = map[string]struct{}{
	"name": {}, "description": {}, "type": {}, "source_document_ids": {},
	"stub": {}, "document_id": {}, "chunk_index": {}, "title": {},
}

var cortexEdgeProps = map[string]struct{}{
	"document_id": {}, "chunk_ids": {}, "inferred": {},
	"provenance": {}, "rule_id": {}, "support_edge_ids": {},
}

// preflight checks everything checkable without the store and refuses the whole
// load if anything is wrong. A load that stops at batch nine of twelve leaves a
// partial graph; anything knowable up front is refused up front.
func preflight(res alchemy.Result, o Options) (*plan, error) {
	o = o.withDefaults()
	if o.RunID == "" {
		return nil, ErrNoRunID
	}

	// §7.3 first, before anything else is even looked at. "Unanswered" is
	// review.Held's definition and not a copy of it: two definitions of what
	// holds a job is how the guarantee ends.
	if open := review.Held(res); len(open) > 0 {
		return nil, fmt.Errorf("%w: %d of %d conflict(s) unanswered, first is %s (%s)",
			ErrHeld, len(open), len(res.Conflicts), open[0].Subject, open[0].Kind)
	}

	p := &plan{res: res, opts: o, digest: digest(res), vectorFor: map[int]int{}, sourceSeen: map[string]struct{}{}}

	ids := make(map[string]int, len(res.Entities))
	for i, e := range res.Entities {
		if e.ID == "" {
			return nil, fmt.Errorf("entity %d has no ID, so nothing can refer to it", i)
		}
		// CortexDB's node upsert is ON CONFLICT DO UPDATE: two entities of one
		// result sharing an ID would leave the second silently wearing the
		// first's edges. Refused rather than resolved, because resolving it
		// means picking a winner nobody asked for.
		if prev, dup := ids[e.ID]; dup {
			return nil, fmt.Errorf("entities %d and %d share the ID %q; one would silently overwrite the other", prev, i, e.ID)
		}
		if err := checkAttributes(e.Attributes, o.ReservedPrefix, cortexNodeProps, "CortexDB writes on an entity node"); err != nil {
			return nil, fmt.Errorf("entity %s: %w", e.ID, err)
		}
		ids[e.ID] = i
		p.source(e.Provenance.Source)
		p.entities = append(p.entities, i)
	}

	if err := p.planChunks(); err != nil {
		return nil, err
	}
	if err := p.planRelations(ids); err != nil {
		return nil, err
	}
	return p, nil
}

// planChunks decides which chunks travel and which embedding belongs to each.
//
// A vector whose chunk is not in the result is dropped: it describes text
// nobody can read, and a citation pointing at text that is not there is exactly
// what §5b promises against.
func (p *plan) planChunks() error {
	if p.opts.SkipChunks {
		return nil
	}
	byIndex := make(map[int]int, len(p.res.Chunks))
	for i, c := range p.res.Chunks {
		if prev, dup := byIndex[c.Index]; dup {
			return fmt.Errorf("chunks %d and %d both claim index %d", prev, i, c.Index)
		}
		byIndex[c.Index] = i
		p.source(c.Source)
		p.chunks = append(p.chunks, i)
	}
	for i, v := range p.res.Vectors {
		if _, ok := byIndex[v.Chunk]; !ok {
			continue
		}
		// One collection holds one dimension. Two embedding models in one
		// result is a result the caller has to split, and finding that out
		// halfway through a write is finding it out too late.
		if p.dim == 0 {
			p.dim = len(v.Values)
		} else if len(v.Values) != p.dim {
			return fmt.Errorf("vector for chunk %d has %d dimensions and an earlier one has %d; "+
				"a collection holds one dimension", v.Chunk, len(v.Values), p.dim)
		}
		p.vectorFor[v.Chunk] = i
	}
	return nil
}

// planRelations groups the edges by CortexDB's identity and refuses the
// collisions CortexDB's model cannot hold.
func (p *plan) planRelations(ids map[string]int) error {
	index := map[string]int{}
	for i, r := range p.res.Relations {
		if err := checkAttributes(r.Attributes, p.opts.ReservedPrefix, cortexEdgeProps, "CortexDB writes on a relation edge"); err != nil {
			return fmt.Errorf("relation %s-[%s]->%s: %w", r.From, r.Type, r.To, err)
		}
		// A dangling relation is ViolationDanglingRelation, and §7.3 puts
		// violations on the "returned, graph delivered" side of the line. It
		// is skipped rather than fatal — and not skipped quietly. CortexDB
		// would reject it too (the store enforces the endpoints as a foreign
		// key), but it would reject it in the middle of a batch, where the
		// news is a count rather than a name.
		_, from := ids[r.From]
		_, to := ids[r.To]
		if !from || !to {
			p.skipped = append(p.skipped, fmt.Sprintf("%s -[%s]-> %s (%s)", r.From, r.Type, r.To, missing(from, to)))
			continue
		}
		p.source(r.Provenance.Source)
		g := edgeGroup{
			from: entityNodeID(p.opts.RunID, r.From),
			to:   entityNodeID(p.opts.RunID, r.To),
			typ:  r.Type,
			doc:  documentID(p.opts.RunID, r.Provenance.Source),
		}
		key := g.from + "\x00" + g.to + "\x00" + g.typ + "\x00" + g.doc
		at, seen := index[key]
		if !seen {
			at = len(p.groups)
			index[key] = at
			p.groups = append(p.groups, g)
		}
		grp := &p.groups[at]
		grp.members = append(grp.members, i)
		if r.Key != "" && !contains(grp.keys, r.Key) {
			grp.keys = append(grp.keys, r.Key)
		}
	}

	for _, g := range p.groups {
		if len(g.keys) < 2 {
			continue
		}
		// The finding this connector exists to make visible. Relation.Key is
		// the producer saying "these are two edges, and here is what I call
		// each"; CortexDB's identity is (from, to, type, document) and has
		// nowhere to put the name. a customer schema's NODE_CONNECTIONS is the ordinary
		// case: one table, two foreign keys to the same table, two constraint
		// names.
		sort.Strings(g.keys)
		note := fmt.Sprintf("%s -[%s]-> %s in %s: keys %v", g.from, g.typ, g.to, g.doc, g.keys)
		if !p.opts.FuseParallelEdges {
			return fmt.Errorf("%w: %s; CortexDB identifies an edge by (from, to, type, document) and has "+
				"nowhere to put Relation.Key, so one of them would win silently — set Options.FuseParallelEdges to accept that", ErrParallelEdges, note)
		}
		p.fused = append(p.fused, note)
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func missing(from, to bool) string {
	switch {
	case !from && !to:
		return "neither endpoint is in the result"
	case !from:
		return "the source entity is not in the result"
	default:
		return "the target entity is not in the result"
	}
}

// checkAttributes enforces the one rule that makes the property layout safe:
// everything alchemy knows lives under the reserved prefix, everything CortexDB
// knows lives under the names it chose, and everything the source said lives
// outside both. Letting an attribute win overwrites the provenance §5b
// promises; letting alchemy win drops something the source actually said.
func checkAttributes(attrs map[string]any, prefix string, reserved map[string]struct{}, whose string) error {
	for k := range attrs {
		if len(prefix) > 0 && len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			return fmt.Errorf("attribute %q is in the reserved %q namespace, where alchemy writes provenance; "+
				"set Options.ReservedPrefix to move the namespace", k, prefix)
		}
		if _, clash := reserved[k]; clash {
			return fmt.Errorf("attribute %q is a property %s; one of the two would have to win silently", k, whose)
		}
	}
	return nil
}

// digest is the identity of everything a Load writes: the same digest under the
// same run is a replay and must converge, a different one is the caller telling
// the store two different things about one import.
//
// It covers provenance as well as content, because §5b's guarantee is about
// attribution: the same edges re-extracted by a different model are not the
// same import. It is order-independent because a result that arrived paged
// (§8.4) can be reassembled in a different order than it was produced in.
func digest(res alchemy.Result) string {
	lines := make([]string, 0, len(res.Entities)+len(res.Relations)+len(res.Chunks))
	for _, e := range res.Entities {
		lines = append(lines, "E\x00"+e.ID+"\x00"+e.Type+"\x00"+e.Name+"\x00"+canonical(e.Attributes)+"\x00"+canonical(e.Provenance))
	}
	for _, r := range res.Relations {
		lines = append(lines, "R\x00"+r.From+"\x00"+r.To+"\x00"+r.Type+"\x00"+r.Key+"\x00"+canonical(r.Attributes)+"\x00"+canonical(r.Provenance))
	}
	// The chunks and the vectors are in the digest because they are written.
	// Two results that agree about the graph and disagree about the text a
	// citation resolves to are two different imports.
	for _, c := range res.Chunks {
		lines = append(lines, "C\x00"+strconv.Itoa(c.Index)+"\x00"+c.Source+"\x00"+c.Strategy+"\x00"+c.Text)
	}
	for _, v := range res.Vectors {
		lines = append(lines, "V\x00"+strconv.Itoa(v.Chunk)+"\x00"+v.Model+"\x00"+canonical(v.Values))
	}
	// The findings are in it because they are written onto the run marker: a
	// result that came back with a clean bill of health must not replay as one
	// that did not.
	lines = append(lines, "F\x00"+canonical(res.Violations)+"\x00"+canonical(res.Duplicates)+
		"\x00"+canonical(res.Guesses)+"\x00"+canonical(res.Unread)+"\x00"+canonical(res.Counts))
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		// Length-prefixed so two records cannot be rearranged into the same
		// byte stream.
		fmt.Fprintf(h, "%d:%s", len(l), l)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonical renders a value for hashing. json.Marshal sorts map keys, which is
// what makes an Attributes map hash the same twice.
func canonical(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}
