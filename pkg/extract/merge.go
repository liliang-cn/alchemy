package extract

import (
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// entityID derives the ID of an entity from what makes it that entity: its
// type and its name, case-folded and whitespace-normalised.
//
// It is a pure function of those two, and that is the point. The same company
// named in chunk 3 and chunk 40 has to become one node, and the only way an ID
// can be the same in both places is for it to depend on nothing that differs
// between them — not on which chunk arrived first, not on how many workers
// were running, not on a counter. A digest would do that too; a readable key
// does it and can also be read in a citation, a violation subject and a
// dangling-relation report, which is where these IDs are actually looked at.
//
// Folding is what makes "SuperAI" and "superai" one node rather than two.
// It is applied to the ID only: Entity.Type and Entity.Name keep the spelling
// the document used, because the ID exists to join facts and the name exists
// to show a person what was said.
func entityID(typ, name string) string {
	return foldKey(typ) + ":" + foldKey(name)
}

// foldKey lowercases and collapses runs of whitespace. Models re-wrap names
// across a line break more often than they misspell them.
func foldKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// provenanceFor is the provenance every entity and relation out of this
// package carries. §5b: it is not optional and it is not partial.
func provenanceFor(c alchemy.Chunk, opts Options, confidence float64) alchemy.Provenance {
	return alchemy.Provenance{
		Source:     c.Source,
		Chunk:      c.Index,
		Producer:   alchemy.ProducerLLMExtract,
		Model:      opts.LLM.Name(),
		Ontology:   opts.OntologyID,
		Chunking:   c.Strategy,
		Confidence: confidence,
	}
}

// entitiesOf turns one reply's proposed entities into entities with identity
// and provenance.
func entitiesOf(c alchemy.Chunk, r reply, opts Options) []alchemy.Entity {
	out := make([]alchemy.Entity, 0, len(r.Entities))
	for _, e := range r.Entities {
		out = append(out, alchemy.Entity{
			ID:         entityID(e.Type, e.Name),
			Type:       e.Type,
			Name:       e.Name,
			Attributes: e.Attributes,
			Provenance: provenanceFor(c, opts, e.Confidence),
		})
	}
	return out
}

// merger folds the entities proposed by every chunk into one node per identity.
//
// It is fed in chunk order and keeps first-seen order, which is what makes the
// output independent of how many workers ran (§7): the workers decide when a
// reply arrives, they do not decide where it goes.
type merger struct {
	order     []string
	byID      map[string]*alchemy.Entity
	conflicts []alchemy.Conflict

	// byName is how an untyped relation end finds its entity. It holds every
	// ID a folded name was seen under, so that "two entities share this name"
	// is answerable rather than silently resolved to whichever was added last.
	byName map[string][]string

	relOrder []string
	relByKey map[string]*alchemy.Relation
}

func newMerger() *merger {
	return &merger{
		byID:     map[string]*alchemy.Entity{},
		byName:   map[string][]string{},
		relByKey: map[string]*alchemy.Relation{},
	}
}

// add merges one proposed entity into the node it names.
//
// The provenance of the earliest chunk that named it is kept whole and is never
// assembled from pieces of several. A Provenance holding chunk 0's index next
// to chunk 40's confidence would describe no reply any model actually gave, and
// a citation that points at no real event is worse than one that points at the
// first of two.
//
// Attributes are unioned rather than replaced, because two chunks describing
// one thing usually state different things about it, and keeping only the
// first chunk's would throw away the reason the second one was read.
func (m *merger) add(e alchemy.Entity) {
	cur, ok := m.byID[e.ID]
	if !ok {
		cp := e
		cp.Attributes = copyAttributes(e.Attributes)
		m.byID[e.ID] = &cp
		m.order = append(m.order, e.ID)
		n := foldKey(e.Name)
		m.byName[n] = append(m.byName[n], e.ID)
		return
	}
	// Sorted, because map order is random and this loop can append conflicts:
	// a result whose Conflicts slice reorders between two runs of the same
	// input is not reproducible, and nothing downstream would explain why.
	for _, k := range sortedKeys(e.Attributes) {
		v := e.Attributes[k]
		if have, taken := cur.Attributes[k]; taken {
			if !sameValue(have, v) {
				// Two chunks stating different values for one attribute is a
				// disagreement, not a merge. After this function there is one
				// node and the loser's value is gone, so this is the last
				// place it can be reported at all — and §7.3 says a conflict
				// is the one thing no caller may opt out of a person seeing.
				m.conflicts = append(m.conflicts, attributeConflict(*cur, e, k, have, v))
			}
			continue
		}
		if cur.Attributes == nil {
			cur.Attributes = map[string]any{}
		}
		cur.Attributes[k] = v
	}
}

// attributeConflict describes one disagreement in the words of the person who
// has to settle it: which thing, which attribute, and what each chunk said.
func attributeConflict(kept, incoming alchemy.Entity, attr string, keptVal, incomingVal any) alchemy.Conflict {
	return alchemy.Conflict{
		Kind:    alchemy.ConflictEntityAttributes,
		Subject: kept.ID,
		Detail: fmt.Sprintf("two chunks name %q but state different values for attribute %q",
			kept.Name, attr),
		Left:  alchemy.Claim{Statement: claimText(kept, attr, keptVal), Provenance: kept.Provenance},
		Right: alchemy.Claim{Statement: claimText(incoming, attr, incomingVal), Provenance: incoming.Provenance},
	}
}

func claimText(e alchemy.Entity, attr string, val any) string {
	return fmt.Sprintf("%s %s: %s = %v", e.Type, e.Name, attr, val)
}

// sameValue compares two attribute values as the model wrote them. Values
// arrive from encoding/json as string, float64, bool, nil or a nested
// structure, and rendering them is enough: two chunks that wrote the same thing
// render the same, and this comparison never has to decide whether "1" and 1
// are one claim — that is a question for a person, which is what a conflict is.
func sameValue(a, b any) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (m *merger) entities() []alchemy.Entity {
	out := make([]alchemy.Entity, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, *m.byID[id])
	}
	return out
}

func copyAttributes(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// relationOf turns one proposed relation into an edge between entity IDs.
//
// It runs after every chunk's entities have been merged, which is the whole
// reason relations are resolved in a second pass: a relation in chunk 1 may
// name a thing chunk 0 introduced, and one in chunk 0 may name a thing chunk 5
// introduces. Resolving as the replies arrived would join the first and break
// the second, and which of the two a corpus hits would depend on the order the
// author happened to write their sections in.
func (m *merger) relationOf(c alchemy.Chunk, rr rawRelation, opts Options) alchemy.Relation {
	// An end that is neither a name nor an object is not dropped: it is
	// resolved to an ID nothing carries, so the edge arrives at the verifier as
	// a dangling relation with its chunk attached. A dropped edge is invisible;
	// a dangling one is a line in a report.
	from, _ := endOf(rr.From, rr.FromType)
	to, _ := endOf(rr.To, rr.ToType)
	return alchemy.Relation{
		From:       m.resolveEnd(from),
		To:         m.resolveEnd(to),
		Type:       rr.Type,
		Attributes: copyAttributes(rr.Attributes),
		Provenance: provenanceFor(c, opts, rr.Confidence),
	}
}

// resolveEnd turns one end into an entity ID.
//
// A typed end needs no lookup: type and name are exactly what entityID is a
// function of, so an end the model typed lands on the same ID as the entity of
// that name wherever it was listed — including in a chunk this reply never saw.
//
// An untyped end is matched by name, and only when the match is unique. Two
// entities sharing a name is precisely the case where picking one is a guess,
// and §2.1's second lesson is that a guess which does not announce itself is a
// bug with a three-month fuse. The alternative to guessing is not dropping the
// edge: it is an ID nothing carries, which the verifier returns as
// alchemy.ViolationDanglingRelation naming the chunk. Wrong and visible beats
// wrong and confident.
func (m *merger) resolveEnd(e end) string {
	if strings.TrimSpace(e.Type) != "" {
		return entityID(e.Type, e.Name)
	}
	if ids := m.byName[foldKey(e.Name)]; len(ids) == 1 {
		return ids[0]
	}
	return entityID("", e.Name)
}

// addRelation merges one edge into the graph. Two chunks asserting the same
// edge is one edge, for the same reason two chunks naming the same thing is one
// node, and the earliest chunk's provenance is kept whole.
//
// Attribute disagreement between two assertions of one edge is not reported as
// a conflict here: alchemy's ConflictEntityAttributes is about entities, and
// labelling an edge's disagreement with an entity's kind would send a reviewer
// looking for a node. It is a gap, and it is named as one rather than papered
// over with the nearest kind.
func (m *merger) addRelation(r alchemy.Relation) {
	key := r.From + "\x00" + foldKey(r.Type) + "\x00" + r.To
	cur, ok := m.relByKey[key]
	if !ok {
		cp := r
		m.relByKey[key] = &cp
		m.relOrder = append(m.relOrder, key)
		return
	}
	for _, k := range sortedKeys(r.Attributes) {
		if _, taken := cur.Attributes[k]; taken {
			continue
		}
		if cur.Attributes == nil {
			cur.Attributes = map[string]any{}
		}
		cur.Attributes[k] = r.Attributes[k]
	}
}

func (m *merger) relations() []alchemy.Relation {
	out := make([]alchemy.Relation, 0, len(m.relOrder))
	for _, k := range m.relOrder {
		out = append(out, *m.relByKey[k])
	}
	return out
}
