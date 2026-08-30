package review

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// records maps the subjects the verifier writes back onto the graph records
// that produced them.
//
// The alternative was to parse a subject into its parts, and it is worse in a
// way that only shows up later: "a -[USES]-> b.role" is ambiguous between an
// edge attribute and an entity whose ID contains a dot, and the formats belong
// to verify, so a parser here would be a private copy of another package's
// output format that no test in either package would notice drifting. Building
// the same strings from the records and looking the subject up cannot drift
// silently — a subject that no record renders simply finds nothing, and an
// item with no targets changes no graph rather than changing the wrong one.
type records struct {
	bySubject map[string][]Ref
}

func index(entities []alchemy.Entity, relations []alchemy.Relation) *records {
	idx := &records{bySubject: make(map[string][]Ref, len(entities)+len(relations))}
	for _, e := range entities {
		idx.add(e.ID, entityRef(e))
	}
	for _, r := range relations {
		ref := relationRef(r)
		idx.add(directed(r, r.Type), ref)
		// The undirected form is what a direction conflict is filed under,
		// because which arrow is drawn is the question being asked.
		idx.add(undirected(r, r.Type), ref)
		// Both spellings again with the producer's key in them, because verify
		// writes the key into a subject when — and only when — there is a
		// sibling edge to be confused with, and this package cannot see the
		// whole graph to know whether it did. Registering both costs two map
		// entries on an edge that has a key and finds the record either way; a
		// keyed subject that matched nothing would leave a conflict with no
		// targets, which is a question §7.3 holds the job for and nobody can
		// answer.
		if r.Key != "" {
			idx.add(directed(r, labelled(r)), ref)
			idx.add(undirected(r, labelled(r)), ref)
		}
	}
	return idx
}

func (idx *records) add(subject string, ref Ref) {
	for _, have := range idx.bySubject[subject] {
		if have == ref {
			return // one Ref already names every record that says this.
		}
	}
	idx.bySubject[subject] = append(idx.bySubject[subject], ref)
}

// find returns the records one source produced under a subject, and the
// attribute name when the subject named one.
//
// Narrowing by provenance is what keeps a decision on one side of a conflict
// from acting on the other. It is not perfect: one chunk that states two
// contradictory things about one entity produces two records with identical
// provenance, and a rejection there removes both. That is the honest answer
// available — nothing in the record distinguishes them — and it is the right
// direction to be imprecise in, since the reviewer's judgement was that this
// chunk is wrong about this subject.
func (idx *records) find(subject string, p alchemy.Provenance) ([]Ref, string) {
	if refs := idx.narrow(subject, p); len(refs) > 0 {
		return refs, ""
	}
	// Attribute subjects are the subject of a record with the attribute name
	// appended, so one fallback is enough and it is only taken when the whole
	// subject named nothing.
	if dot := strings.LastIndex(subject, "."); dot > 0 {
		if refs := idx.narrow(subject[:dot], p); len(refs) > 0 {
			return refs, subject[dot+1:]
		}
	}
	return nil, ""
}

func (idx *records) narrow(subject string, p alchemy.Provenance) []Ref {
	var out []Ref
	for _, ref := range idx.bySubject[subject] {
		if ref.Provenance == p {
			out = append(out, ref)
		}
	}
	return out
}

func entityRef(e alchemy.Entity) Ref {
	// The type is part of an entity's Ref because it is part of what the
	// record claims. Two records both calling themselves n1 while typing it
	// differently are the whole of alchemy.ConflictEntityType, and a Ref that
	// could not tell them apart would let a decision about one delete both.
	return Ref{Kind: RefEntity, ID: e.ID, Type: e.Type, Provenance: e.Provenance}
}

// relationRef carries the key for the reason entityRef carries the type: it is
// part of what the record claims. Two foreign keys onto one table are two
// edges that agree about their ends, their type and their source and differ
// only in which of them they are, and a Ref that could not tell them apart
// would let a decision about one delete both.
func relationRef(r alchemy.Relation) Ref {
	return Ref{Kind: RefRelation, From: r.From, To: r.To, Type: r.Type, Key: r.Key, Provenance: r.Provenance}
}

// labelled is the type with the producer's key after it, the spelling verify
// uses for an edge that has a sibling. The two are built from the record in
// both packages rather than parsed, so neither can drift into a format the
// other does not write.
func labelled(r alchemy.Relation) string { return r.Type + "#" + r.Key }

// directedSubject is how an item names one edge: the arrow the record drew,
// with the producer's key in it when the record has one, so that two parallel
// edges are two subjects rather than one.
func directedSubject(r alchemy.Relation) string {
	if r.Key == "" {
		return directed(r, r.Type)
	}
	return directed(r, labelled(r))
}

func directed(r alchemy.Relation, label string) string {
	return fmt.Sprintf("%s -[%s]-> %s", r.From, label, r.To)
}

func undirected(r alchemy.Relation, label string) string {
	lo, hi := r.From, r.To
	if lo > hi {
		lo, hi = hi, lo
	}
	return fmt.Sprintf("%s -[%s]- %s", lo, label, hi)
}
