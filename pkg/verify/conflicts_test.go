package verify_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

var (
	fromSchema   = alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	fromPDF      = alchemy.Provenance{Source: "contract.pdf", Chunk: 41, Producer: alchemy.ProducerLLMExtract, Model: "gemini-3.6-flash-high"}
	fromOtherPDF = alchemy.Provenance{Source: "architecture.pdf", Chunk: 4, Producer: alchemy.ProducerLLMExtract, Model: "gemini-3.6-flash-high"}
)

func check(t *testing.T, es []alchemy.Entity, rs []alchemy.Relation) verify.Report {
	t.Helper()
	return verify.Check(verify.Input{Entities: es, Relations: rs, Vocabulary: vocab(), OntologyID: "sds@3"})
}

func TestSameEntityWithTwoTypesIsAConflict(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{
			{ID: "n1", Type: "Node", Name: "node-1", Provenance: fromSchema},
			{ID: "n1", Type: "StoragePool", Name: "node-1", Provenance: fromPDF},
		}, nil)

	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want exactly one", got.Conflicts)
	}
	c := got.Conflicts[0]
	if c.Kind != alchemy.ConflictEntityType {
		t.Fatalf("kind = %q, want %q", c.Kind, alchemy.ConflictEntityType)
	}
	if c.Subject != "n1" {
		t.Fatalf("subject = %q, want the entity ID", c.Subject)
	}
	// §5c: the reviewer must see a schema on one side and a PDF on the other.
	if c.Left.Provenance.Source != "schema.sql" || c.Right.Provenance.Source != "contract.pdf" {
		t.Fatalf("claims = %+v / %+v, want each side to keep its own provenance", c.Left, c.Right)
	}
	if !strings.Contains(c.Left.Statement, "Node") || !strings.Contains(c.Right.Statement, "StoragePool") {
		t.Fatalf("statements = %q / %q, want each to state its own claim", c.Left.Statement, c.Right.Statement)
	}
	if got.Counts.Conflicts != 1 {
		t.Fatalf("counts.conflicts = %d, want 1", got.Counts.Conflicts)
	}
	// A conflict is not a violation: the item breaks no ontology rule, and
	// filing it as one would let a caller exclude an item and carry on with a
	// graph that still contradicts itself.
	if len(got.Violations) != 0 {
		t.Fatalf("violations = %+v, want none", got.Violations)
	}
}

// Canonicalisation runs first, so a difference of spelling is never a question
// for a person. Waking someone to adjudicate "cluster vs Cluster" is how a
// review queue stops being read.
func TestTypesThatDifferOnlyInSpellingAreNotAConflict(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{
			{ID: "c1", Type: "cluster", Name: "prod", Provenance: fromSchema},
			{ID: "c1", Type: "CLUSTER", Name: "prod", Provenance: fromPDF},
		}, nil)

	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none", got.Conflicts)
	}
}

// Three distinct claims are two questions, not three: each is reported against
// the first claim, which is the identity-keyed scan §8.1 asks for rather than
// the pairwise one it warns about.
func TestEachDistinctTypeIsReportedOnceAgainstTheFirstClaim(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{
			{ID: "n1", Type: "Node", Provenance: fromSchema},
			{ID: "n1", Type: "StoragePool", Provenance: fromPDF},
			{ID: "n1", Type: "StoragePool", Provenance: fromOtherPDF},
			{ID: "n1", Type: "Cluster", Provenance: fromPDF},
		}, nil)

	if len(got.Conflicts) != 2 {
		t.Fatalf("conflicts = %d, want 2 (StoragePool and Cluster, each against Node)", len(got.Conflicts))
	}
	for _, c := range got.Conflicts {
		if !strings.Contains(c.Left.Statement, "Node") {
			t.Fatalf("left = %q, want every conflict measured against the first claim", c.Left.Statement)
		}
	}
}

func TestSameEntityAttributeWithTwoValuesIsAConflict(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{
			{ID: "c1", Type: "Cluster", Name: "prod", Attributes: map[string]any{"region": "eu-west", "version": "3.1"}, Provenance: fromSchema},
			{ID: "c1", Type: "Cluster", Name: "prod", Attributes: map[string]any{"region": "us-east", "version": "3.1"}, Provenance: fromPDF},
		}, nil)

	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want exactly one (region, not version)", got.Conflicts)
	}
	c := got.Conflicts[0]
	if c.Kind != alchemy.ConflictEntityAttributes {
		t.Fatalf("kind = %q, want %q", c.Kind, alchemy.ConflictEntityAttributes)
	}
	if !strings.Contains(c.Subject, "c1") || !strings.Contains(c.Subject, "region") {
		t.Fatalf("subject = %q, want it to name the entity and the attribute", c.Subject)
	}
	if !strings.Contains(c.Left.Statement, "eu-west") || !strings.Contains(c.Right.Statement, "us-east") {
		t.Fatalf("statements = %q / %q", c.Left.Statement, c.Right.Statement)
	}
	if c.Left.Provenance.Source != "schema.sql" || c.Right.Provenance.Source != "contract.pdf" {
		t.Fatalf("claims kept the wrong provenance: %+v / %+v", c.Left, c.Right)
	}
}

// §5c's question — "is it one customer or two?" — is usually asked by the name
// before any other field, so the name is compared as the attribute it is.
func TestSameEntityWithTwoNamesIsAnAttributeConflict(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{
			{ID: "cust-7", Type: "Cluster", Name: "Acme Inc", Provenance: fromSchema},
			{ID: "cust-7", Type: "Cluster", Name: "Acme Incorporated", Provenance: fromPDF},
		}, nil)

	if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != alchemy.ConflictEntityAttributes {
		t.Fatalf("conflicts = %+v, want one entity_attributes conflict on the name", got.Conflicts)
	}
}

// An attribute one source did not mention is not that source claiming it is
// empty. Treating silence as a claim would fill the queue with questions no
// document ever asked.
func TestAnAbsentAttributeIsNotADisagreement(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{
			{ID: "c1", Type: "Cluster", Attributes: map[string]any{"region": "eu-west"}, Provenance: fromSchema},
			{ID: "c1", Type: "Cluster", Provenance: fromPDF},
		}, nil)

	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none", got.Conflicts)
	}
}

// Requirement: the same source repeating itself is redundancy, not a conflict —
// and so is one source corroborating another. Two claims that agree are the one
// case where there is something in the data to decide with.
func TestIdenticalStatementsAreRedundancyNotConflict(t *testing.T) {
	e := func(p alchemy.Provenance) alchemy.Entity {
		return alchemy.Entity{ID: "c1", Type: "Cluster", Name: "prod", Attributes: map[string]any{"region": "eu-west"}, Provenance: p}
	}
	r := func(p alchemy.Provenance) alchemy.Relation {
		return alchemy.Relation{From: "c1", To: "n1", Type: "CONTAINS", Provenance: p}
	}
	got := check(t,
		[]alchemy.Entity{e(fromPDF), e(fromPDF), e(fromSchema), e(fromOtherPDF), {ID: "n1", Type: "Node"}},
		[]alchemy.Relation{r(fromPDF), r(fromPDF), r(fromSchema)})

	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none: repetition and corroboration are not questions", got.Conflicts)
	}
}

// Requirement: two statements from the *same* source that disagree.
//
// This is a conflict, not a violation. A violation is defined by two properties
// (§7.3): the ontology can name the rule that was broken, and the graph minus
// the offending item is usable. Neither holds here — both records may be
// perfectly well-typed, and there is no rule saying which of the two to drop.
// Dropping either is a decision about the world, which is the definition of a
// question for a person. The harm is identical too: an agent traversing the
// result answers from whichever edge it reached first, and it makes no
// difference to that reader whether the two edges came from one PDF or two.
func TestOneSourceDisagreeingWithItselfIsAConflict(t *testing.T) {
	page4 := alchemy.Provenance{Source: "contract.pdf", Chunk: 4, Producer: alchemy.ProducerLLMExtract}
	page41 := alchemy.Provenance{Source: "contract.pdf", Chunk: 41, Producer: alchemy.ProducerLLMExtract}

	got := check(t,
		[]alchemy.Entity{
			{ID: "c1", Type: "Cluster", Attributes: map[string]any{"region": "eu-west"}, Provenance: page4},
			{ID: "c1", Type: "Cluster", Attributes: map[string]any{"region": "us-east"}, Provenance: page41},
		}, nil)

	if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != alchemy.ConflictEntityAttributes {
		t.Fatalf("conflicts = %+v, want one entity_attributes conflict", got.Conflicts)
	}
	c := got.Conflicts[0]
	if c.Left.Provenance.Chunk != 4 || c.Right.Provenance.Chunk != 41 {
		t.Fatalf("claims = %+v / %+v, want the two chunks of the one file", c.Left, c.Right)
	}
	if len(got.Violations) != 0 {
		t.Fatalf("violations = %+v: a source contradicting itself breaks no ontology rule", got.Violations)
	}
}

func TestOppositeDirectionsFromTwoInferredSourcesIsADirectionConflict(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}},
		[]alchemy.Relation{
			{From: "c1", To: "n1", Type: "MENTIONS", Provenance: fromPDF},
			{From: "n1", To: "c1", Type: "MENTIONS", Provenance: fromOtherPDF},
		})

	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want exactly one", got.Conflicts)
	}
	c := got.Conflicts[0]
	if c.Kind != alchemy.ConflictRelationDirection {
		t.Fatalf("kind = %q, want %q", c.Kind, alchemy.ConflictRelationDirection)
	}
	if !strings.Contains(c.Subject, "c1") || !strings.Contains(c.Subject, "n1") || !strings.Contains(c.Subject, "MENTIONS") {
		t.Fatalf("subject = %q, want it to name both ends and the type", c.Subject)
	}
	if c.Left.Provenance.Source != "contract.pdf" || c.Right.Provenance.Source != "architecture.pdf" {
		t.Fatalf("claims = %+v / %+v", c.Left, c.Right)
	}
}

// §5c: worth surfacing precisely *because* the deterministic side almost always
// wins, and the exception is where the interesting bug lives. The kind changes
// with the producers, not with the shape of the disagreement, because "a schema
// says otherwise" is the fact that decides it.
func TestADeterministicEdgeReversedByAnInferredOneIsAContradiction(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}},
		[]alchemy.Relation{
			{From: "c1", To: "n1", Type: "CONTAINS", Provenance: fromSchema},
			{From: "n1", To: "c1", Type: "CONTAINS", Provenance: fromPDF},
		})

	if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != alchemy.ConflictContradiction {
		t.Fatalf("conflicts = %+v, want one contradiction", got.Conflicts)
	}
	c := got.Conflicts[0]
	if !c.Left.Provenance.Producer.Deterministic() || c.Right.Provenance.Producer.Deterministic() {
		t.Fatalf("claims = %+v / %+v, want the deterministic side on the left", c.Left, c.Right)
	}
	// The reversed edge also breaks the ontology, and that is a second, separate
	// fact: it is the evidence that resolves the question, so it is reported
	// rather than folded into the conflict.
	if len(got.Violations) != 1 || got.Violations[0].Kind != alchemy.ViolationRelationNotAllowed {
		t.Fatalf("violations = %+v, want the reversed edge reported too", got.Violations)
	}
}

func TestADeterministicEdgeGivenOtherAttributesByAnInferredOneIsAContradiction(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{{ID: "p1", Type: "StoragePool"}, {ID: "n1", Type: "Node"}},
		[]alchemy.Relation{
			{From: "p1", To: "n1", Type: "DEPLOYED_ON", Attributes: map[string]any{"on_delete": "cascade"}, Provenance: fromSchema},
			{From: "p1", To: "n1", Type: "DEPLOYED_ON", Attributes: map[string]any{"on_delete": "restrict"}, Provenance: fromPDF},
		})

	if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != alchemy.ConflictContradiction {
		t.Fatalf("conflicts = %+v, want one contradiction", got.Conflicts)
	}
	c := got.Conflicts[0]
	if !strings.Contains(c.Left.Statement, "cascade") || !strings.Contains(c.Right.Statement, "restrict") {
		t.Fatalf("statements = %q / %q", c.Left.Statement, c.Right.Statement)
	}
	if len(got.Violations) != 0 {
		t.Fatalf("violations = %+v, want none: both edges are well-typed", got.Violations)
	}
}

// The same edge asserted the same way by a schema and a document is the schema
// being confirmed, which is the opposite of a question.
func TestADeterministicEdgeAgreedWithByAnInferredOneIsNotAConflict(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}},
		[]alchemy.Relation{
			{From: "c1", To: "n1", Type: "CONTAINS", Provenance: fromSchema},
			{From: "c1", To: "n1", Type: "CONTAINS", Provenance: fromPDF},
		})

	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none", got.Conflicts)
	}
}

// One edge is one question. A hundred chunks repeating the reversal must not
// become a hundred queue items, or §5c's queue is one people stop reading.
func TestOneEdgeYieldsOneDirectionQuestionHoweverManyTimesItRepeats(t *testing.T) {
	rels := []alchemy.Relation{{From: "c1", To: "n1", Type: "MENTIONS", Provenance: fromPDF}}
	for i := 0; i < 50; i++ {
		rels = append(rels, alchemy.Relation{From: "n1", To: "c1", Type: "MENTIONS", Provenance: fromOtherPDF})
	}
	got := check(t, []alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}}, rels)

	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1", len(got.Conflicts))
	}
}

// Two relation types between one pair are two independent claims: a cluster may
// both contain and mention a node, and neither says anything about the other.
func TestDifferentRelationTypesBetweenOnePairAreNotAConflict(t *testing.T) {
	got := check(t,
		[]alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}},
		[]alchemy.Relation{
			{From: "c1", To: "n1", Type: "CONTAINS", Provenance: fromSchema},
			{From: "c1", To: "n1", Type: "MENTIONS", Provenance: fromPDF},
		})

	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none", got.Conflicts)
	}
}
