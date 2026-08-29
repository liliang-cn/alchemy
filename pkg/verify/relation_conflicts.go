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

// attrPair is the one comparison §5c's third bullet describes: what a schema
// stated about this edge against what a model inferred about it.
//
// Two *inferred* sources disagreeing about an edge attribute is a real
// disagreement with no ConflictKind to file it under — contradiction would tell
// a reviewer a schema is involved when none is, and direction would be false.
// Adding a fifth kind means changing the shared contract, which is not this
// package's to change; the limit is pinned by a test that will fail the day it
// is lifted.
type attrPair struct {
	det, inf *valued
	reported bool
}

type valued struct {
	value string
	prov  alchemy.Provenance
}

func relationConflicts(relations []alchemy.Relation) []alchemy.Conflict {
	var out []alchemy.Conflict
	groups := make(map[relationKey]*relationGroup, len(relations))

	for _, r := range relations {
		key, dir := identify(r)
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

		out = append(out, attributeContradictions(g, key, dir, det, r)...)
	}
	return out
}

// attributeContradictions compares what a schema stated about this edge with
// what a model inferred about it. Same direction only: when two records run
// opposite ways the direction is the question, and their attributes are answers
// to different ones.
func attributeContradictions(g *relationGroup, key relationKey, dir direction, det bool, r alchemy.Relation) []alchemy.Conflict {
	var out []alchemy.Conflict
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
		switch {
		case det && pair.det == nil:
			pair.det = &valued{value: a.value, prov: r.Provenance}
		case !det && pair.inf == nil:
			pair.inf = &valued{value: a.value, prov: r.Provenance}
		}
		if pair.reported || pair.det == nil || pair.inf == nil || pair.det.value == pair.inf.value {
			continue
		}
		pair.reported = true
		e := edgeOf(key, dir)
		out = append(out, alchemy.Conflict{
			Kind:    alchemy.ConflictContradiction,
			Subject: e + "." + a.name,
			Detail: fmt.Sprintf("%s: %s says %s = %s, %s says %s; a model disagreeing with a statement is the case worth a person's time",
				e, where(pair.det.prov), a.name, pair.det.value, where(pair.inf.prov), pair.inf.value),
			Left:  claim(fmt.Sprintf("%s has %s = %s", e, a.name, pair.det.value), pair.det.prov),
			Right: claim(fmt.Sprintf("%s has %s = %s", e, a.name, pair.inf.value), pair.inf.prov),
		})
	}
	return out
}

// identify maps a record onto its undirected key and says which way it ran.
func identify(r alchemy.Relation) (relationKey, direction) {
	if r.From <= r.To {
		return relationKey{lo: r.From, hi: r.To, typ: r.Type}, forward
	}
	return relationKey{lo: r.To, hi: r.From, typ: r.Type}, reverse
}

// subjectOf writes the edge without an arrow, because which arrow is the
// question being asked.
func subjectOf(k relationKey) string {
	return fmt.Sprintf("%s -[%s]- %s", k.lo, k.typ, k.hi)
}

// edgeOf writes one side's claim with the arrow that side drew.
func edgeOf(k relationKey, dir direction) string {
	if dir == forward {
		return fmt.Sprintf("%s -[%s]-> %s", k.lo, k.typ, k.hi)
	}
	return fmt.Sprintf("%s -[%s]-> %s", k.hi, k.typ, k.lo)
}

func class(deterministic bool) int {
	if deterministic {
		return 1
	}
	return 0
}
