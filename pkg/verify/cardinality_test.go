package verify_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// A cardinality conflict is an assertion that one node can be at this end of
// only one edge of this type. Nothing but an ontology has ever been entitled to
// say that, so these tests fix which side of that line each case falls on, and
// they fix the other half too: two records of one edge are one edge, and one
// edge is not a breach of anything.

// fromCorrection is the second document in the run this file was written from:
// a small graph import stating what the profile got wrong. It is deterministic
// and it arrived second, which is exactly the shape that must not be allowed to
// decide anything by itself.
var fromCorrection = alchemy.Provenance{Source: "correction.json", Chunk: -1, Producer: alchemy.ProducerGraphImport}

// northgateVocab declares the three types the cases below need: one constrained at
// the `to` end, one at the `from` end, and one constrained at neither, because
// the last is what proves the check is reading a declaration rather than
// counting edges.
func northgateVocab() ontology.Vocabulary {
	return ontology.Vocabulary{
		Entities: []ontology.EntityType{{Name: "Person"}, {Name: "Organization"}},
		Relations: []ontology.RelationType{
			{Name: "CHIEF_TECHNOLOGY_OFFICER_OF", From: []string{"Person"}, To: []string{"Organization"}, AtMostOneIn: true},
			{Name: "PART_OF", From: []string{"Organization"}, To: []string{"Organization"}, AtMostOneOut: true},
			{Name: "WORKS_FOR", From: []string{"Person"}, To: []string{"Organization"}},
		},
	}
}

func people(ids ...string) []alchemy.Entity {
	out := []alchemy.Entity{{ID: "org:northgate", Type: "Organization", Name: "Northgate", Provenance: fromPDF}}
	for _, id := range ids {
		out = append(out, alchemy.Entity{ID: id, Type: "Person", Provenance: fromPDF})
	}
	return out
}

func northgate(t *testing.T, es []alchemy.Entity, rs []alchemy.Relation) verify.Report {
	t.Helper()
	return verify.Check(verify.Input{Entities: es, Relations: rs, Vocabulary: northgateVocab(), OntologyID: "company@1"})
}

func cardinalities(cs []alchemy.Conflict) []alchemy.Conflict {
	var out []alchemy.Conflict
	for _, c := range cs {
		if c.Kind == alchemy.ConflictCardinality {
			out = append(out, c)
		}
	}
	return out
}

// The case that started this, measured: one company profile extracted
// from a PDF says Ada is the CTO, one correction document imported as a graph
// says Bruno is, and the job came back holding both edges and reporting zero
// conflicts. That is the failure a graph is least able to survive — not a wrong
// edge, which provenance attributes and a reader excludes, but a right edge and
// a stale one side by side with nothing between them.
func TestTwoPeopleAssertedAsCTOOfOneCompanyAreACardinalityConflict(t *testing.T) {
	got := northgate(t, people("person:ada", "person:bruno"), []alchemy.Relation{
		{From: "person:ada", To: "org:northgate", Type: "CHIEF_TECHNOLOGY_OFFICER_OF", Provenance: fromPDF},
		{From: "person:bruno", To: "org:northgate", Type: "CHIEF_TECHNOLOGY_OFFICER_OF", Provenance: fromCorrection},
	})

	cs := cardinalities(got.Conflicts)
	if len(cs) != 1 {
		t.Fatalf("cardinality conflicts = %+v, want exactly one: the ontology declares a company has one CTO", got.Conflicts)
	}
	c := cs[0]
	// The subject names the dissenting edge, which is the record pkg/review
	// resolves a decision onto. A subject no record renders would leave §7.3
	// holding the job on a question nobody can answer.
	if c.Subject != "person:bruno -[CHIEF_TECHNOLOGY_OFFICER_OF]-> org:northgate" {
		t.Fatalf("subject = %q, want the newcomer's edge", c.Subject)
	}
	if c.Left.Provenance.Source != fromPDF.Source {
		t.Fatalf("left = %+v, want the incumbent claim, which is the one already standing", c.Left)
	}
	if c.Right.Provenance.Source != "correction.json" {
		t.Fatalf("right = %+v, want the dissenting claim, which is what a decision acts on", c.Right)
	}
	// A person reading the queue has to see which node, which type, and both
	// claims with their sources, or the question cannot be answered away from
	// the data.
	for _, want := range []string{"org:northgate", "CHIEF_TECHNOLOGY_OFFICER_OF", "person:ada", "person:bruno", "contract.pdf", "correction.json"} {
		if !strings.Contains(c.Detail, want) {
			t.Fatalf("detail = %q, want it to name %q", c.Detail, want)
		}
	}
	// Neither edge is dropped and neither is a violation: §7.3 holds the job
	// and a person decides, which is the whole of what this finding licenses.
	if len(got.Relations) != 2 {
		t.Fatalf("relations = %d, want both edges left standing", len(got.Relations))
	}
	if len(got.Violations) != 0 {
		t.Fatalf("violations = %+v, want none: neither edge breaks an ontology rule", got.Violations)
	}
	if got.Counts.Conflicts != 1 {
		t.Fatalf("counts.Conflicts = %d, want 1", got.Counts.Conflicts)
	}
}

// Two records asserting one edge are one edge — alchemy.Relation.Identity is
// what says so — and one edge cannot breach a limit of one. Two chunks
// corroborating the same sentence is the commonest thing in the corpus, and a
// check that counted records rather than edges would hold every job that had
// one.
func TestTwoRecordsOfOneEdgeAtAConstrainedEndAreCorroborationNotABreach(t *testing.T) {
	got := northgate(t, people("person:ada"), []alchemy.Relation{
		{From: "person:ada", To: "org:northgate", Type: "CHIEF_TECHNOLOGY_OFFICER_OF", Provenance: fromPDF},
		{From: "person:ada", To: "org:northgate", Type: "CHIEF_TECHNOLOGY_OFFICER_OF", Provenance: fromOtherPDF},
	})

	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none: two records of one edge are one edge", got.Conflicts)
	}
}

// The asymmetry is the point of there being two fields. PART_OF constrains the
// `from` end — a thing is part of one thing — and WORKS_FOR constrains neither,
// so no number of edges into one company is a question for anybody.
func TestAtMostOneOutConstrainsTheFromEndAndATypeDeclaringNeitherConstrainsNothing(t *testing.T) {
	es := []alchemy.Entity{
		{ID: "org:northgate", Type: "Organization", Provenance: fromPDF},
		{ID: "org:eu", Type: "Organization", Provenance: fromPDF},
		{ID: "org:at", Type: "Organization", Provenance: fromPDF},
		{ID: "person:ada", Type: "Person", Provenance: fromPDF},
		{ID: "person:bruno", Type: "Person", Provenance: fromPDF},
		{ID: "person:cleo", Type: "Person", Provenance: fromPDF},
	}
	got := northgate(t, es, []alchemy.Relation{
		{From: "org:northgate", To: "org:eu", Type: "PART_OF", Provenance: fromPDF},
		{From: "org:northgate", To: "org:at", Type: "PART_OF", Provenance: fromCorrection},
		{From: "person:ada", To: "org:northgate", Type: "WORKS_FOR", Provenance: fromPDF},
		{From: "person:bruno", To: "org:northgate", Type: "WORKS_FOR", Provenance: fromPDF},
		{From: "person:cleo", To: "org:northgate", Type: "WORKS_FOR", Provenance: fromCorrection},
	})

	cs := cardinalities(got.Conflicts)
	if len(cs) != 1 {
		t.Fatalf("cardinality conflicts = %+v, want exactly the one PART_OF pair", cs)
	}
	if cs[0].Subject != "org:northgate -[PART_OF]-> org:at" {
		t.Fatalf("subject = %q, want the newcomer's PART_OF edge", cs[0].Subject)
	}
	// The `to` end of PART_OF is not constrained either, so the two edges
	// arriving at two different parents must not be read as a breach at those.
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want only the one", got.Conflicts)
	}
}

// Three edges at one constrained node is a queue a person can read: each
// newcomer is asked about once, against the claim already standing. The
// pairwise version is four more questions here and half a million on a corpus
// §8.1 sizes, which is a queue nobody reads at all.
func TestThreeEdgesAtOneConstrainedNodeAreTwoQuestionsNotSix(t *testing.T) {
	got := northgate(t, people("person:ada", "person:bruno", "person:dana"), []alchemy.Relation{
		{From: "person:ada", To: "org:northgate", Type: "CHIEF_TECHNOLOGY_OFFICER_OF", Provenance: fromPDF},
		{From: "person:bruno", To: "org:northgate", Type: "CHIEF_TECHNOLOGY_OFFICER_OF", Provenance: fromCorrection},
		{From: "person:dana", To: "org:northgate", Type: "CHIEF_TECHNOLOGY_OFFICER_OF", Provenance: fromOtherPDF},
	})

	cs := cardinalities(got.Conflicts)
	if len(cs) != 2 {
		t.Fatalf("cardinality conflicts = %d, want one per newcomer against the standing claim: %+v", len(cs), cs)
	}
	for i, want := range []string{
		"person:bruno -[CHIEF_TECHNOLOGY_OFFICER_OF]-> org:northgate",
		"person:dana -[CHIEF_TECHNOLOGY_OFFICER_OF]-> org:northgate",
	} {
		if cs[i].Subject != want {
			t.Fatalf("conflict %d subject = %q, want %q", i, cs[i].Subject, want)
		}
		if cs[i].Left.Statement != "person:ada -[CHIEF_TECHNOLOGY_OFFICER_OF]-> org:northgate" {
			t.Fatalf("conflict %d left = %q, want the standing claim on the left", i, cs[i].Left.Statement)
		}
	}
}

// A type the ontology never declared has no cardinality to breach, for the same
// reason it has no direction to run: nobody claimed anything, so there is no
// rule for two edges to have broken. It is not thereby ignored — it is already
// an unknown-relation-type violation, which names it, attributes it, and leaves
// the rest of the graph usable. A conflict on top would hold the job on a
// question the vocabulary gave the reviewer no way to answer.
func TestAnUndeclaredRelationTypeIsAViolationAndNeverACardinalityConflict(t *testing.T) {
	got := northgate(t, people("person:ada", "person:bruno"), []alchemy.Relation{
		{From: "person:ada", To: "org:northgate", Type: "CTO_OF", Provenance: fromPDF},
		{From: "person:bruno", To: "org:northgate", Type: "CTO_OF", Provenance: fromCorrection},
	})

	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none: an undeclared type constrains nothing", got.Conflicts)
	}
	if len(got.Violations) != 2 {
		t.Fatalf("violations = %+v, want one per record naming the undeclared type", got.Violations)
	}
	for _, v := range got.Violations {
		if v.Kind != alchemy.ViolationUnknownRelationType {
			t.Fatalf("violation kind = %q, want %q", v.Kind, alchemy.ViolationUnknownRelationType)
		}
	}
}

// The constraint this finding has to satisfy outside this package, pinned here
// because it is invisible from inside it.
//
// pkg/review indexes a decision's target by the subject the verifier wrote, and
// it indexes an entity under its bare ID. So a cardinality conflict whose
// subject named the node — which is what the finding is *about* — would resolve
// to the company, and a reviewer rejecting a stale claim about who the CTO is
// would delete Northgate. A subject naming nothing is barely better: pkg/review
// calls an item with no targets a question §7.3 holds the job for and nobody
// can answer. Naming the dissenting edge is what makes the decision act on the
// record the reviewer was actually asked about; the node, the type and the end
// are in the Detail, which is the sentence they read.
//
// The import runs verify_test -> review -> verify, which is not a cycle: this
// is the external test package, and nothing in review imports it.
func TestACardinalityConflictResolvesToTheDissentingEdgeAndNotToTheNode(t *testing.T) {
	rep := northgate(t, people("person:ada", "person:bruno"), []alchemy.Relation{
		{From: "person:ada", To: "org:northgate", Type: "CHIEF_TECHNOLOGY_OFFICER_OF", Provenance: fromPDF},
		{From: "person:bruno", To: "org:northgate", Type: "CHIEF_TECHNOLOGY_OFFICER_OF", Provenance: fromCorrection},
	})

	items := review.Queue(rep, alchemy.Result{}, review.Options{})
	if len(items) != 1 {
		t.Fatalf("queue = %+v, want the one held conflict", items)
	}
	if len(items[0].Targets) != 1 {
		t.Fatalf("targets = %+v, want exactly the record the reviewer is being asked about", items[0].Targets)
	}
	got := items[0].Targets[0]
	if got.Kind != alchemy.RefRelation {
		t.Fatalf("target kind = %q, want %q: a decision here must act on the edge, never on the company", got.Kind, alchemy.RefRelation)
	}
	if got.From != "person:bruno" || got.To != "org:northgate" || got.Type != "CHIEF_TECHNOLOGY_OFFICER_OF" {
		t.Fatalf("target = %+v, want the dissenting edge", got)
	}
	// And the incumbent is untouched by that decision, which is the other half
	// of putting it on the left.
	// Compared field by field because verify stamps the ontology it checked
	// against onto a provenance that did not name one.
	if got.Provenance.Source != fromCorrection.Source || got.Provenance.Producer != fromCorrection.Producer {
		t.Fatalf("target provenance = %+v, want the newcomer's, so the standing claim survives a rejection", got.Provenance)
	}
}
