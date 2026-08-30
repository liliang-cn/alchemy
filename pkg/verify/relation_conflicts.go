package verify

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// direction is which way a record ran relative to its key's sorted endpoints.
// The key is undirected on purpose: A→B and B→A have to land in the same bucket
// or the disagreement between them is invisible, which is §8.1's sharding
// failure arriving inside a single process.
type direction int

const (
	forward direction = 0
	reverse direction = 1
)

type relationKey struct{ lo, hi, typ string }

// edgeKey is one edge's identity: the undirected pair and the type, which is
// all identity has ever been here, plus the producer's own name for the edge —
// alchemy.Relation.Key — where that name is actually telling two edges apart.
//
// The pair and the type alone are not an identity, and the case that proves it
// is the most ordinary thing in SQL. A table modelling a relationship between
// two rows of one table references that table twice, once per end;
// NODE_CONNECTIONS names NODES as both the source and the destination of a
// connection. Both foreign keys are correct. They differ only in what they say
// about themselves — different columns, different constraint names — and those
// are precisely the values this file compares, so an identity that ignored the
// key read two correct edges as two sources contradicting each other. Over one
// customer's real schema that was ten questions no person could answer, on a
// job §7.3 will not let finish.
//
// Attributes cannot be promoted into the identity instead: they are the thing
// being compared, so any disagreement would become a different edge and
// ConflictRelationAttributes could never fire again. Only the producer knows,
// which is why the key comes from the producer or not at all.
type edgeKey struct {
	relationKey
	// key is empty whenever the group holds fewer than two distinct producer
	// keys — see parallelEdges for why that is not the same as copying
	// Relation.Key straight in.
	key string
}

// relationGroup is everything remembered about one edge. first[dir][class]
// holds the provenance of the earliest record seen running that way from a
// deterministic or an inferred producer — four entries however many million
// records arrive, which is the identity-keyed index §8.1 asks for in place of a
// pairwise scan.
type relationGroup struct {
	first [2][2]*alchemy.Provenance

	// One edge is one question of each kind. A hundred chunks repeating the
	// same reversal is still one thing a person has to decide, and a queue that
	// asks it a hundred times is a queue people stop reading (§5c).
	reportedDirection     bool
	reportedContradiction bool

	attrs map[attrKey]*attrPair
}

type attrKey struct {
	dir  direction
	name string
}

// attrPair remembers the first value each class of producer gave one attribute
// of one edge. first[class] is a slot, the same one the entity side uses, so
// "the same statement twice is corroboration" means one thing in both families
// and each distinct disagreement is asked about once.
//
// Two entries however many records arrive: this is the identity-keyed index
// §8.1 asks for, not a scan of what has been seen before.
type attrPair struct {
	first [2]*slot

	// A schema disagreeing with a model is one question per attribute, however
	// many chunks repeat it.
	reportedContradiction bool
}

// valued is one side of a comparison being assembled, before it is worth
// spending a sentence on.
type valued struct {
	value string
	prov  alchemy.Provenance
}

// parallelEdges names the {pair, type} buckets that really do hold more than
// one edge: the ones where two records carry different producer keys.
//
// The key is used to partition a bucket only there, and the reason is the case
// the partition would otherwise break. A key tells two edges apart; it cannot
// tell you which of them a record carrying no key meant. A schema states its
// foreign key with a constraint name and a model reading the same relationship
// out of prose states it with nothing — and "a schema says otherwise" is the
// finding §5c ranks above every other, so losing it to a field the model had no
// way to fill would be trading one silence for a worse one. Where the bucket
// holds one edge there is nothing to be ambiguous about, so the keyed record
// and the unkeyed one are compared exactly as they were before this field
// existed. Where it holds several, an unkeyed record names none of them, and
// attaching it to whichever arrived first is the silent guess §2.1 is about.
//
// It costs one extra pass over the relations and no comparisons: §8.1's
// objection is to the pairwise scan, not to reading the input twice.
func parallelEdges(relations []alchemy.Relation) map[relationKey]bool {
	first := make(map[relationKey]string, len(relations))
	var out map[relationKey]bool // nil until a job actually has parallel edges.
	for _, r := range relations {
		if r.Key == "" {
			continue
		}
		k, _ := identify(r)
		seen, ok := first[k]
		if !ok {
			first[k] = r.Key
			continue
		}
		if seen != r.Key {
			if out == nil {
				out = map[relationKey]bool{}
			}
			out[k] = true
		}
	}
	return out
}

func relationConflicts(relations []alchemy.Relation) []alchemy.Conflict {
	var out []alchemy.Conflict
	parallel := parallelEdges(relations)
	groups := make(map[edgeKey]*relationGroup, len(relations))

	for _, r := range relations {
		base, dir := identify(r)
		key := edgeKey{relationKey: base}
		if parallel[base] {
			key.key = r.Key
		}
		g := groups[key]
		if g == nil {
			g = &relationGroup{}
			groups[key] = g
		}
		det := r.Provenance.Producer.Deterministic()
		opposite := 1 - dir

		// The contradiction is checked before the plain reversal and wins when
		// both apply: §5c ranks "a schema says otherwise" above "two documents
		// disagree", because the deterministic side almost always settles it and
		// the rare time it does not is where the interesting bug lives.
		if partner := g.first[opposite][class(!det)]; partner != nil && !g.reportedContradiction {
			g.reportedContradiction = true
			// The deterministic claim goes on the left: a reviewer reads left to
			// right, and the side that read a statement belongs first.
			left, leftDir := r.Provenance, dir
			right, rightDir := *partner, direction(opposite)
			if !det {
				left, leftDir, right, rightDir = right, rightDir, left, leftDir
			}
			out = append(out, alchemy.Conflict{
				Kind:    alchemy.ConflictContradiction,
				Subject: subjectOf(key),
				Detail: fmt.Sprintf("%s is asserted by %s and reversed by %s; the deterministic side usually wins, and the time it does not is the one worth reading",
					subjectOf(key), where(left), where(right)),
				Left:  claim(edgeOf(key, leftDir), left),
				Right: claim(edgeOf(key, rightDir), right),
			})
		}
		if partner := g.first[opposite][class(det)]; partner != nil && !g.reportedDirection {
			g.reportedDirection = true
			out = append(out, alchemy.Conflict{
				Kind:    alchemy.ConflictRelationDirection,
				Subject: subjectOf(key),
				Detail: fmt.Sprintf("%s runs one way per %s and the other per %s; neither side has more standing than the other, so nothing in the data settles it",
					subjectOf(key), where(*partner), where(r.Provenance)),
				Left:  claim(edgeOf(key, direction(opposite)), *partner),
				Right: claim(edgeOf(key, dir), r.Provenance),
			})
		}
		if g.first[dir][class(det)] == nil {
			p := r.Provenance
			g.first[dir][class(det)] = &p
		}

		out = append(out, attributeConflicts(g, key, dir, det, r)...)
	}
	return out
}

// attributeConflicts compares what one source said about this edge's attributes
// with what another said about the same attributes of the same edge.
//
// Same direction only: when two records run opposite ways the direction is the
// question, and their attributes are answers to different ones.
//
// The kind turns on standing rather than on producer. A deterministic side
// against an inferred one is a ConflictContradiction, because "a schema says
// otherwise" is the fact that usually settles it and §5c wants that on the
// label. Two sides of equal standing — two models, or two schemas — is a
// ConflictRelationAttributes: neither side has that advantage, which is exactly
// what leaves the question for a person. This is the same standing rule the
// direction family uses, so the two cannot drift apart.
func attributeConflicts(g *relationGroup, key edgeKey, dir direction, det bool, r alchemy.Relation) []alchemy.Conflict {
	var out []alchemy.Conflict
	mine, theirs := class(det), class(!det)

	for _, a := range sortedAttributes(r.Attributes) {
		k := attrKey{dir: dir, name: a.name}
		pair := g.attrs[k]
		if pair == nil {
			pair = &attrPair{}
			if g.attrs == nil {
				g.attrs = make(map[attrKey]*attrPair, len(r.Attributes))
			}
			g.attrs[k] = pair
		}
		// Checked before the equal-standing case and reported once, for the same
		// reason the direction family checks it first: it is the more actionable
		// label when both could apply.
		if partner := pair.first[theirs]; partner != nil && !pair.reportedContradiction && partner.value != a.value {
			pair.reportedContradiction = true
			e := edgeOf(key, dir)
			left, right := valued{value: a.value, prov: r.Provenance}, valued{value: partner.value, prov: partner.prov}
			if !det {
				left, right = right, left // the side that read a statement goes first.
			}
			out = append(out, alchemy.Conflict{
				Kind:    alchemy.ConflictContradiction,
				Subject: e + "." + a.name,
				Detail: fmt.Sprintf("%s: %s says %s = %s, %s says %s; a model disagreeing with a statement is the case worth a person's time",
					e, where(left.prov), a.name, left.value, where(right.prov), right.value),
				Left:  claim(edgeAttrStatement(e, a.name, left.value), left.prov),
				Right: claim(edgeAttrStatement(e, a.name, right.value), right.prov),
			})
		}

		if pair.first[mine] == nil {
			pair.first[mine] = &slot{value: a.value, prov: r.Provenance}
			continue
		}
		first := pair.first[mine]
		if !first.disagrees(a.value) {
			continue
		}
		// edgeOf is rendered here rather than above the branches: the common
		// record agrees with what is already known, and a string built for a
		// message nobody sends is the sort of cost §8 notices.
		e := edgeOf(key, dir)
		out = append(out, alchemy.Conflict{
			Kind:    alchemy.ConflictRelationAttributes,
			Subject: e + "." + a.name,
			Detail: fmt.Sprintf("%s: %s says %s = %s, %s says %s; neither source has more standing than the other, so nothing in the data settles it",
				e, where(first.prov), a.name, first.value, where(r.Provenance), a.value),
			Left:  claim(edgeAttrStatement(e, a.name, first.value), first.prov),
			Right: claim(edgeAttrStatement(e, a.name, a.value), r.Provenance),
		})
	}
	return out
}

func edgeAttrStatement(e, name, value string) string {
	return fmt.Sprintf("%s has %s = %s", e, name, value)
}

// identify maps a record onto its undirected key and says which way it ran.
func identify(r alchemy.Relation) (relationKey, direction) {
	if r.From <= r.To {
		return relationKey{lo: r.From, hi: r.To, typ: r.Type}, forward
	}
	return relationKey{lo: r.To, hi: r.From, typ: r.Type}, reverse
}

// labelOf writes the type, and the producer's name for the edge after it when
// there is a sibling edge to be confused with.
//
// The name is in the subject rather than only in the group because a subject is
// what a reviewer answers and what review.Apply looks records up by. Two
// questions about two different foreign keys between the same two tables that
// rendered the same string would be two questions a person cannot tell apart,
// and one decision would land on both edges — which is the defect this change
// is about, one layer down. Where nothing is being told apart the string is
// exactly what it always was, so every subject a caller has ever seen is
// unchanged.
func labelOf(k edgeKey) string {
	if k.key == "" {
		return k.typ
	}
	return k.typ + "#" + k.key
}

// subjectOf writes the edge without an arrow, because which arrow is the
// question being asked.
func subjectOf(k edgeKey) string {
	return fmt.Sprintf("%s -[%s]- %s", k.lo, labelOf(k), k.hi)
}

// edgeOf writes one side's claim with the arrow that side drew.
func edgeOf(k edgeKey, dir direction) string {
	if dir == forward {
		return fmt.Sprintf("%s -[%s]-> %s", k.lo, labelOf(k), k.hi)
	}
	return fmt.Sprintf("%s -[%s]-> %s", k.hi, labelOf(k), k.lo)
}

func class(deterministic bool) int {
	if deterministic {
		return 1
	}
	return 0
}
