// Package verify is the stage DESIGN.md §3 refuses to make a flag: "an
// extraction nobody checked is an extraction nobody should act on".
//
// It does two jobs, and the boundary between them is the whole point. A
// violation is one source saying something the ontology does not allow —
// attributable, excludable, and the rest of the graph is usable without it, so
// it never holds a job. A conflict is two claims both asserting they are right
// with nothing in the data to decide between them — §7.3 makes that a question,
// and a question has to be asked of someone, so it holds the job whether or not
// review mode is on.
package verify

import (
	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// Input is one job's accumulated extraction, checked against one part's
// vocabulary. §8.1: the whole job is checked at once, because a conflict is
// global to it and only something that sees both sides can notice one.
type Input struct {
	Entities  []alchemy.Entity
	Relations []alchemy.Relation
	// Vocabulary is the part the extraction was constrained by — the same list
	// on both sides of the model (§5b).
	Vocabulary ontology.Vocabulary
	// OntologyID names that vocabulary in violation text and in the provenance
	// of items that did not already carry it.
	OntologyID string
}

// Report is what the verifier hands the coordinator.
type Report struct {
	// Entities and Relations are the input with types canonicalised. Nothing is
	// dropped: §5b says a rule-breaking edge is "not silently dropped and not
	// silently kept", so exclusion stays the caller's or the reviewer's decision
	// and this stage only says what is wrong with what.
	Entities  []alchemy.Entity
	Relations []alchemy.Relation

	Violations []alchemy.Violation
	Conflicts  []alchemy.Conflict
	Counts     alchemy.Counts
}

// Check verifies one job's extraction. The output is a pure function of the
// input, ordered the same way every time — see the determinism test.
func Check(in Input) Report {
	rs := newRules(in.Vocabulary, in.OntologyID)
	entities, relations, types := canonicalise(in, rs)
	out := Report{
		Entities:   entities,
		Relations:  relations,
		Violations: violations(entities, relations, types, rs),
		Conflicts:  conflicts(entities, relations),
	}
	out.Counts = count(out)
	return out
}
