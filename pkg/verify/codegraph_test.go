package verify_test

import (
	"os"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/source/graphimport"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// codeGraph reads the fixture and returns what the connector made of it. The
// fixture's own header says where it came from and why; see testdata.
func codeGraph(t *testing.T) graphimport.Result {
	t.Helper()
	f, err := os.Open("testdata/service-code-graph.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := graphimport.Parse("service-code-graph.json", f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return res
}

func directionConflicts(cs []alchemy.Conflict) []alchemy.Conflict {
	var out []alchemy.Conflict
	for _, c := range cs {
		if c.Kind == alchemy.ConflictRelationDirection {
			out = append(out, c)
		}
	}
	return out
}

// A whole code graph imports without a single question for a person.
//
// This is the test the defect was found by. Two Java classes that each import
// the other is ordinary and legal, the tool recorded both facts correctly, and
// this package read every one of them as two sources contradicting each other
// about which way the edge runs — 79 of them over the customer's whole graph,
// which the fixture reproduces the shape of, on a job
// §7.3 will not let finish. Nothing had declared `imports` asymmetric; nothing
// had declared it at all.
func TestACodeGraphImportsWithoutDirectionConflicts(t *testing.T) {
	res := codeGraph(t)

	got := verify.Check(verify.Input{Entities: res.Entities, Relations: res.Relations})
	if n := len(directionConflicts(got.Conflicts)); n != 0 {
		t.Fatalf("direction conflicts = %d, want 0; first is %+v", n, directionConflicts(got.Conflicts)[0])
	}
	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %d, want 0; first is %+v", len(got.Conflicts), got.Conflicts[0])
	}
	// Zero is also what an empty fixture would produce, so the count above is
	// only half the claim: the mutual pairs really are in there, both ways.
	if n := mutualPairs(res.Relations); n != 6 {
		t.Fatalf("mutual pairs in the fixture = %d, want 6", n)
	}
}

// The same graph checked under an ontology that governs the job but does not
// declare `imports`. A vocabulary is in force here, so the undeclared type is
// reported as a violation — attributable, excludable, and the rest of the graph
// usable without it (§7.3) — and it is still not a question for a person.
//
// This is the case the deployed job actually ran: three sources, one of them a
// code graph, checked against a vocabulary that never mentions `imports`.
func TestAnUndeclaredTypeRunningBothWaysIsAViolationAndNotAConflict(t *testing.T) {
	res := codeGraph(t)

	got := verify.Check(verify.Input{
		Entities: res.Entities, Relations: res.Relations,
		Vocabulary: ontology.Vocabulary{
			Entities:  []ontology.EntityType{{Name: "file"}, {Name: "class"}, {Name: "function"}, {Name: "service"}},
			Relations: []ontology.RelationType{{Name: "contains", From: []string{"file"}, To: []string{"class", "function"}}},
		},
		OntologyID: "service-code@1",
	})
	if n := len(directionConflicts(got.Conflicts)); n != 0 {
		t.Fatalf("direction conflicts = %d, want 0; first is %+v", n, directionConflicts(got.Conflicts)[0])
	}
	var unknown int
	for _, v := range got.Violations {
		if v.Kind == alchemy.ViolationUnknownRelationType {
			unknown++
		}
	}
	if unknown == 0 {
		t.Fatal("no violation reported for the undeclared relation types; the finding moved from one silence to another")
	}
}

// mutualPairs counts the undirected {pair, type} groups the fixture states in
// both directions. It is the property the fixture exists for, so a fixture that
// lost it must fail rather than pass quietly.
func mutualPairs(rs []alchemy.Relation) int {
	type key struct{ from, to, typ string }
	seen := map[key]bool{}
	for _, r := range rs {
		seen[key{r.From, r.To, r.Type}] = true
	}
	pairs := map[key]bool{}
	for k := range seen {
		if k.from == k.to || !seen[key{k.to, k.from, k.typ}] {
			continue
		}
		lo, hi := k.from, k.to
		if lo > hi {
			lo, hi = hi, lo
		}
		pairs[key{lo, hi, k.typ}] = true
	}
	return len(pairs)
}
