package cortexdb

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	check "github.com/liliang-cn/alchemy/pkg/preflight"
	"github.com/liliang-cn/alchemy/pkg/sink"
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
	// at is when this load put its records on the shelf, RFC 3339, taken once
	// so every record of one load says the same moment. It fills the contract's
	// _at for records alchemy gave no time of their own — every extraction,
	// because Result is content-addressed and a clock on it would change every
	// address ever produced (alchemy.Provenance.At). A producer's own At, when
	// it has one (ProducerHuman), wins over this.
	at string

	entities []int
	groups   []edgeGroup
	// chunks are the indexes of Result.Chunks that will be written, and
	// vectorFor maps a chunk index to the embedding alchemy computed for it.
	chunks    []int
	vectorFor map[int]int
	// dim is the dimension every vector shares. A collection has one.
	dim int

	sourceSeen map[string]struct{}
	// ids and relIndex are what make the plan buildable a batch at a time
	// rather than only from a whole result. They are the two lookups the
	// per-record checks need — is this ID already claimed, and which group does
	// this edge belong to — and they are here rather than local to a loop
	// because pkg/sink hands this store a stream and preflight hands it a
	// slice, and both have to reach the same plan.
	ids      map[string]int
	relIndex map[string]int
	chunkAt  map[int]int

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
	// refused indexes the ontology's findings by the record each is about, so
	// that a write can grade a record the vocabulary rejected without parsing a
	// Violation.Subject back into its parts — the join Ref exists to make
	// possible and pkg/verify already built. It is filled from sink.Findings,
	// which arrive after every record and before Commit, which is the only
	// window in which this is knowable: the grade goes on a node the same call
	// writes.
	refused map[alchemy.Ref]alchemy.Violation
}

// refuse files the findings a write grades records by.
//
// A violation whose About is zero is skipped rather than guessed at: a
// malformed row and an unnamed column are about a FILE, and findings.go says
// so — inventing a Ref for them would be a join that resolves to nothing. The
// first finding about a record wins, in the result's own order, because
// pkg/verify walks entities before relations and reports the undeclared type
// before the endpoint rule: the first is the one a reviewer is shown first and
// the one whose fix removes the rest.
func (p *plan) refuse(vs []alchemy.Violation) {
	for _, v := range vs {
		if v.About == (alchemy.Ref{}) {
			continue
		}
		if _, seen := p.refused[v.About]; seen {
			continue
		}
		p.refused[v.About] = v
	}
}

// refusal returns the finding that names one record, or nil for a record the
// ontology had nothing to say about.
func (p *plan) refusal(ref alchemy.Ref) *alchemy.Violation {
	v, ok := p.refused[ref]
	if !ok {
		return nil
	}
	return &v
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
		// The result names the job that produced it, and that is the fact
		// Options.RunID says only the caller has: stated by the service rather
		// than generated, so it is the same after a crash and the same when
		// another node takes the job over (§8.3), and different for a genuinely
		// different import. A caller who wants two names for one graph still
		// says so and still wins.
		o.RunID = res.Job
	}
	if o.RunID == "" {
		return nil, ErrNoRunID
	}

	// §7.3 first, before anything else is even looked at. "Unanswered" is
	// review.Held's definition and not a copy of it: two definitions of what
	// holds a job is how the guarantee ends.
	if open := res.Held(); len(open) > 0 {
		return nil, fmt.Errorf("%w: %d of %d conflict(s) unanswered, first is %s (%s)",
			ErrHeld, len(open), len(res.Conflicts), open[0].Subject, open[0].Kind)
	}

	p := newPlan(o, sink.Digest(res))
	if err := p.addEntities(res.Entities); err != nil {
		return nil, err
	}
	if err := p.addChunks(pairs(res)); err != nil {
		return nil, err
	}
	if err := p.addRelations(res.Relations); err != nil {
		return nil, err
	}
	if err := p.checkParallelEdges(); err != nil {
		return nil, err
	}

	// The refusals every store had to write for itself, asked once.
	//
	// It runs last, so everything this connector already caught still comes
	// back as this connector's own error with this connector's own wording;
	// what changes is only the set of results that used to reach a write. Four
	// stores, written without sight of each other, each defended a different
	// subset of one list — and the gaps were not opinions, they were silent
	// overwrites nobody could see, because nothing said the invariants existed.
	//
	// Everything on the list is refused here, including the parts that would
	// harm some other store and not this one. §7.3's own sentence is the
	// argument: a guarantee that only holds where it is convenient is not a
	// guarantee, and a result that pgvector rejects and this accepts is a
	// corpus loaded into half of a buyer's estate.
	if err := check.Refuse(res); err != nil {
		return nil, err
	}

	return p, nil
}

// planChunks decides which chunks travel and which embedding belongs to each.
//
// A vector whose chunk is not in the result is dropped: it describes text
// nobody can read, and a citation pointing at text that is not there is exactly
// what §5b promises against.
func (p *plan) addChunks(batch []sink.Chunk) error {
	if p.opts.SkipChunks {
		return nil
	}
	for _, c := range batch {
		i := len(p.res.Chunks)
		if prev, dup := p.chunkAt[c.Index]; dup {
			return fmt.Errorf("chunks %d and %d both claim index %d", prev, i, c.Index)
		}
		if p.chunkAt == nil {
			p.chunkAt = map[int]int{}
		}
		p.chunkAt[c.Index] = i
		p.res.Chunks = append(p.res.Chunks, c.Chunk)
		p.source(c.Source)
		p.chunks = append(p.chunks, i)
		if c.Vector == nil {
			continue
		}
		v := alchemy.Vector{Chunk: c.Index, Values: c.Vector, Model: c.Model}
		p.res.Vectors = append(p.res.Vectors, v)
		// One collection holds one dimension. Two embedding models in one
		// result is a result the caller has to split, and finding that out
		// halfway through a write is finding it out too late.
		if p.dim == 0 {
			p.dim = len(v.Values)
		} else if len(v.Values) != p.dim {
			return fmt.Errorf("vector for chunk %d has %d dimensions and an earlier one has %d; "+
				"a collection holds one dimension", v.Chunk, len(v.Values), p.dim)
		}
		p.vectorFor[v.Chunk] = len(p.res.Vectors) - 1
	}
	return nil
}

func newPlan(o Options, digest string) *plan {
	return &plan{
		at:   time.Now().UTC().Format(time.RFC3339),
		opts: o, digest: digest,
		vectorFor: map[int]int{}, sourceSeen: map[string]struct{}{},
		ids: map[string]int{}, relIndex: map[string]int{}, chunkAt: map[int]int{},
		refused: map[alchemy.Ref]alchemy.Violation{},
	}
}

// pairs is the whole-result path's version of what the envelope hands a
// streaming one: every chunk with the embedding that describes it. It exists
// so that addChunks has one caller shape rather than two.
func pairs(res alchemy.Result) []sink.Chunk {
	byChunk := make(map[int]int, len(res.Vectors))
	for i, v := range res.Vectors {
		byChunk[v.Chunk] = i
	}
	out := make([]sink.Chunk, 0, len(res.Chunks))
	for _, c := range res.Chunks {
		pc := sink.Chunk{Chunk: c}
		if i, ok := byChunk[c.Index]; ok {
			pc.Vector, pc.Model = res.Vectors[i].Values, res.Vectors[i].Model
		}
		out = append(out, pc)
	}
	return out
}

// addEntities checks one batch of nodes and files them.
func (p *plan) addEntities(batch []alchemy.Entity) error {
	for _, e := range batch {
		i := len(p.res.Entities)
		if e.ID == "" {
			return fmt.Errorf("entity %d has no ID, so nothing can refer to it", i)
		}
		// CortexDB's node upsert is ON CONFLICT DO UPDATE: two entities of one
		// result sharing an ID would leave the second silently wearing the
		// first's edges. Refused rather than resolved, because resolving it
		// means picking a winner nobody asked for.
		if prev, dup := p.ids[e.ID]; dup {
			return fmt.Errorf("entities %d and %d share the ID %q; one would silently overwrite the other", prev, i, e.ID)
		}
		if err := checkAttributes(e.Attributes, p.opts.ReservedPrefix, cortexNodeProps, "CortexDB writes on an entity node"); err != nil {
			return fmt.Errorf("entity %s: %w", e.ID, err)
		}
		p.res.Entities = append(p.res.Entities, e)
		p.ids[e.ID] = i
		p.source(e.Provenance.Source)
		p.entities = append(p.entities, i)
	}
	return nil
}

// addRelations groups one batch of edges by CortexDB's identity.
//
// It is a batch rather than the whole slice because pkg/sink streams, and the
// grouping index lives on the plan for the same reason. What it cannot do is
// decide anything about a group until every batch has arrived — see
// checkParallelEdges — which is this store's own constraint: CortexDB
// identifies an edge by (from, to, type, document), so two records that are one
// edge can be pages apart in a paged result and the store must see both before
// it writes either.
func (p *plan) addRelations(batch []alchemy.Relation) error {
	for _, r := range batch {
		i := len(p.res.Relations)
		p.res.Relations = append(p.res.Relations, r)
		if err := checkAttributes(r.Attributes, p.opts.ReservedPrefix, cortexEdgeProps, "CortexDB writes on a relation edge"); err != nil {
			return fmt.Errorf("relation %s-[%s]->%s: %w", r.From, r.Type, r.To, err)
		}
		// A dangling relation is ViolationDanglingRelation, and §7.3 puts
		// violations on the "returned, graph delivered" side of the line. It
		// is skipped rather than fatal — and not skipped quietly. CortexDB
		// would reject it too (the store enforces the endpoints as a foreign
		// key), but it would reject it in the middle of a batch, where the
		// news is a count rather than a name.
		_, from := p.ids[r.From]
		_, to := p.ids[r.To]
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
		at, seen := p.relIndex[key]
		if !seen {
			at = len(p.groups)
			p.relIndex[key] = at
			p.groups = append(p.groups, g)
		}
		grp := &p.groups[at]
		grp.members = append(grp.members, i)
		if r.Key != "" && !contains(grp.keys, r.Key) {
			grp.keys = append(grp.keys, r.Key)
		}
	}
	return nil
}

// checkParallelEdges is the finding this connector exists to make visible, and
// it can only be made once every edge has arrived: two records that CortexDB's
// identity rule calls one edge can be pages apart.
func (p *plan) checkParallelEdges() error {
	for _, g := range p.groups {
		if len(g.keys) < 2 {
			continue
		}
		// The finding this connector exists to make visible. Relation.Key is
		// the producer saying "these are two edges, and here is what I call
		// each"; CortexDB's identity is (from, to, type, document) and has
		// nowhere to put the name. NODE_CONNECTIONS is the ordinary
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
