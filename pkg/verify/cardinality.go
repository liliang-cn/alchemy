package verify

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// end is which side of an edge a constraint was declared about. It is part of
// the group key rather than a flag on the finding because a type may constrain
// both ends, and one node can be over the limit at one end while being
// perfectly ordinary at the other.
type end int

const (
	toEnd end = iota
	fromEnd
)

// endKey is one node's one end under one type — the only thing a cardinality
// constraint is ever about.
//
// It is the whole of why this pass is separate from relationConflicts. Every
// other conflict compares two claims about one edge, so it groups by the pair
// and the type; this compares two claims about one NODE, so a group here holds
// edges that share exactly one endpoint and disagree about the other. Trying to
// fold it into the edge-keyed scan would mean looking outside the group, which
// is the pairwise walk §8.1 names as the implementation that looks fine and
// dies at volume.
type endKey struct {
	node string
	typ  string
	end  end
}

// standing is the claim already holding one constrained end.
//
// seen is the same slot the entity and attribute checks use, keyed by
// alchemy.Relation.Identity: "the same statement twice is corroboration" has to
// mean one thing everywhere, and here it means that a hundred chunks
// corroborating one edge are one edge and breach nothing. A check that counted
// records instead of edges would hold every job in the corpus, because two
// chunks stating the same sentence is the commonest thing in it.
type standing struct {
	seen slot
	// first is the record that got here first, kept whole because the finding
	// has to render the other end of its edge and the producer's key, which the
	// provenance alone does not carry.
	first alchemy.Relation
}

// cardinalityConflicts finds a node with more edges of one type at one end than
// the ontology said it may have.
//
// Every newcomer is reported against the claim already standing, so N distinct
// edges at one end are N-1 findings rather than the N(N-1)/2 a pairwise walk
// would produce. The alternative — one finding naming all of them — was
// rejected because alchemy.Conflict has exactly two claims, and a finding that
// held three would have to leave the third out of the Left/Right fields both a
// reviewer and pkg/review read; a decision on it would then act on records
// nobody could see in it. Reported this way each question is what a person is
// actually being asked: this edge arrived, that one is already here, which
// holds. The order they arrived in decides which is which and nothing else:
// see pkg/ontology's RelationType.AtMostOneIn for why that must not be allowed
// to look like a verdict.
//
// Nothing is dropped and nothing is chosen, so a job holding one of these stops
// under §7.3 until a person answers it, which is the entire point — a stale
// edge and a current one side by side is the state a graph is least able to
// survive.
func cardinalityConflicts(relations []alchemy.Relation, rs *rules) []alchemy.Conflict {
	var out []alchemy.Conflict
	// nil until a job actually has a constrained type in it: most ontologies
	// declare none, and those should pay a map lookup per record and no
	// allocation at all.
	var ends map[endKey]*standing

	for _, r := range relations {
		// An undeclared type constrains nothing, which holdsAtMostOne answers
		// without this pass having to know it: the vocabulary is the only thing
		// entitled to say a company has one CTO, and where it said nothing
		// there is no rule for two edges to have broken. Such an edge is
		// already an unknown-relation-type violation, which names it and leaves
		// the rest of the graph usable; a conflict on top would hold the job on
		// a question the reviewer was given no vocabulary to answer.
		limit := rs.holdsAtMostOne(r.Type)
		if !limit.constrains() {
			continue
		}
		identity := r.Identity()
		if limit.in {
			out = append(out, atEnd(&ends, endKey{node: r.To, typ: r.Type, end: toEnd}, identity, r)...)
		}
		if limit.out {
			out = append(out, atEnd(&ends, endKey{node: r.From, typ: r.Type, end: fromEnd}, identity, r)...)
		}
	}
	return out
}

// atEnd records one edge at one constrained end and reports whether that is
// news. It returns a slice rather than a conflict and a bool so the two ends of
// a type that constrains both read as one line each above.
func atEnd(ends *map[endKey]*standing, k endKey, identity string, r alchemy.Relation) []alchemy.Conflict {
	if *ends == nil {
		*ends = map[endKey]*standing{}
	}
	held := (*ends)[k]
	if held == nil {
		(*ends)[k] = &standing{seen: slot{value: identity, prov: r.Provenance}, first: r}
		return nil
	}
	if !held.seen.disagrees(identity) {
		return nil // the same edge again: corroboration, or a question already asked.
	}
	return []alchemy.Conflict{{
		Kind: alchemy.ConflictCardinality,
		// The subject is the newcomer's edge, and it is the newcomer's because
		// that is the record a decision on this item acts on: pkg/review reads
		// the right-hand claim's provenance and looks the subject up, so a
		// subject naming the incumbent, or naming the node, would leave a
		// held conflict with no record to answer it with.
		Subject: directedEdge(r),
		Detail: fmt.Sprintf("%s is at the %s end of %s per %s and of %s per %s; the ontology declares at most one, and the later record may be a correction or may be the mistake, so nothing in the data settles it",
			k.node, k.end, directedEdge(held.first), where(held.first.Provenance), directedEdge(r), where(r.Provenance)),
		// The two Refs are two different edges, which is what makes this kind
		// and the direction family the ones a store can write `_contradicts`
		// for: nothing here is being merged or removed, the graph holds both,
		// and the ontology is the only thing saying they cannot both stand.
		Left:  claim(directedEdge(held.first), held.first.Provenance, relationRef(held.first)),
		Right: claim(directedEdge(r), r.Provenance, relationRef(r)),
	}}
}

func (e end) String() string {
	if e == toEnd {
		return "to"
	}
	return "from"
}

// directedEdge writes one record's edge the way its own producer drew it, with
// the producer's key in the label whenever the record carries one.
//
// The key is always written where there is one, rather than only where a
// sibling edge exists as the direction checks do it, because here the sibling
// is the whole finding: two foreign keys onto one table at a `to` end declared
// at_most_one_in are two records a reviewer must be able to tell apart, and
// they agree about their ends, their type and their source. It is the spelling
// pkg/review already registers for a keyed relation, so both forms resolve back
// to the record that rendered them.
func directedEdge(r alchemy.Relation) string {
	if r.Key == "" {
		return fmt.Sprintf("%s -[%s]-> %s", r.From, r.Type, r.To)
	}
	return fmt.Sprintf("%s -[%s#%s]-> %s", r.From, r.Type, r.Key, r.To)
}
