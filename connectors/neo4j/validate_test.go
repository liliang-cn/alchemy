package neo4j

import (
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

func ent(id, typ, name string) alchemy.Entity {
	return alchemy.Entity{ID: id, Type: typ, Name: name, Provenance: sampleProvenance()}
}

func rel(from, to, typ string) alchemy.Relation {
	return alchemy.Relation{From: from, To: to, Type: typ, Provenance: sampleProvenance()}
}

func opts() Options {
	return Options{RunID: "run-1"}
}

// §7.3: a job that finds a conflict does not finish, and a graph that
// contradicts itself is worse than no graph. The service already refuses to
// hand such a result over, but a Result can reach a connector from a file, a
// page of StreamResult or a fixture — and the connector is the last place
// before the contradiction becomes a graph an agent will answer from.
func TestRefusesHeldResult(t *testing.T) {
	res := alchemy.Result{
		Entities: []alchemy.Entity{ent("e1", "System", "SuperAI"), ent("e2", "System", "CortexDB")},
		Conflicts: []alchemy.Conflict{{
			Kind:    alchemy.ConflictRelationDirection,
			Subject: "e1 USES e2",
			Left:    alchemy.Claim{Statement: "e1 uses e2"},
			Right:   alchemy.Claim{Statement: "e2 uses e1"},
		}},
	}
	_, err := preflight(res, opts())
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("preflight of a held result: err = %v, want ErrHeld", err)
	}
	if !strings.Contains(err.Error(), "1") {
		t.Fatalf("error does not say how many conflicts are unanswered: %v", err)
	}
}

// An answered conflict is not a held job. The definition of "unanswered" is
// pkg/review's, not a copy of it: a connector that re-derived the rule would
// be free to drift from the service that enforces it.
func TestAcceptsAnsweredConflict(t *testing.T) {
	c := alchemy.Conflict{Kind: alchemy.ConflictEntityType, Subject: "e1"}
	c.Left.Provenance.ReviewedBy = "ada"
	res := alchemy.Result{Entities: []alchemy.Entity{ent("e1", "System", "SuperAI")}, Conflicts: []alchemy.Conflict{c}}
	if _, err := preflight(res, opts()); err != nil {
		t.Fatalf("preflight refused a result whose conflict was answered: %v", err)
	}
}

// The run identity is the caller's to supply and cannot be defaulted. A
// generated one makes a retry impossible to tell from a second import; a
// constant one merges two runs whose entity IDs mean different things.
func TestRefusesWithoutRunID(t *testing.T) {
	_, err := preflight(alchemy.Result{}, Options{})
	if !errors.Is(err, ErrNoRunID) {
		t.Fatalf("preflight with no RunID: err = %v, want ErrNoRunID", err)
	}
}

// Everything alchemy knows lives under the reserved prefix. An attribute in
// that namespace would silently overwrite provenance, which is §5b's
// guarantee being ended by a model choosing a field name.
func TestRefusesReservedAttributeKey(t *testing.T) {
	e := ent("e1", "System", "SuperAI")
	e.Attributes = map[string]any{"_producer": "hand-written"}
	_, err := preflight(alchemy.Result{Entities: []alchemy.Entity{e}}, opts())
	if err == nil || !strings.Contains(err.Error(), "_producer") {
		t.Fatalf("preflight accepted an attribute in the reserved namespace: %v", err)
	}
	// The prefix is configurable so a buyer whose ontology genuinely uses
	// underscore-led fields has somewhere to go other than editing the graph.
	o := opts()
	o.ReservedPrefix = "alchemy_"
	if _, err := preflight(alchemy.Result{Entities: []alchemy.Entity{e}}, o); err != nil {
		t.Fatalf("moving the reserved prefix did not free the name: %v", err)
	}
}

// An attribute called "name" that disagrees with Entity.Name is refused,
// because one of the two would have to win silently. An attribute that agrees
// is not a collision at all and must not fail a four-hundred-thousand-record
// import.
func TestNameAttributeCollision(t *testing.T) {
	e := ent("e1", "System", "SuperAI")
	e.Attributes = map[string]any{"name": "SuperAI"}
	if _, err := preflight(alchemy.Result{Entities: []alchemy.Entity{e}}, opts()); err != nil {
		t.Fatalf("an attribute that agrees with the entity name was refused: %v", err)
	}
	e.Attributes = map[string]any{"name": "Super AI Inc"}
	_, err := preflight(alchemy.Result{Entities: []alchemy.Entity{e}}, opts())
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("a disagreeing name attribute was accepted: %v", err)
	}
}

// A type that cannot be a label fails before anything is written, naming the
// entity, rather than at batch nine of twelve.
func TestRefusesUnrepresentableType(t *testing.T) {
	_, err := preflight(alchemy.Result{Entities: []alchemy.Entity{ent("e1", "", "SuperAI")}}, opts())
	if err == nil || !strings.Contains(err.Error(), "e1") {
		t.Fatalf("an untypeable entity was accepted, or the error does not name it: %v", err)
	}
}

// A dangling relation is an ontology violation (ViolationDanglingRelation) and
// §7.3 puts violations on the "graph delivered" side of the line. So it is
// skipped rather than fatal — and named in the report, because a relation that
// vanished with no record of it is the silent loss this whole design refuses.
func TestDanglingRelationIsSkippedNotFatal(t *testing.T) {
	res := alchemy.Result{
		Entities:  []alchemy.Entity{ent("e1", "System", "SuperAI")},
		Relations: []alchemy.Relation{rel("e1", "e404", "USES"), rel("e1", "e1", "USES")},
	}
	p, err := preflight(res, opts())
	if err != nil {
		t.Fatalf("a dangling relation was fatal: %v", err)
	}
	if len(p.skipped) != 1 || !strings.Contains(p.skipped[0], "e404") {
		t.Fatalf("skipped = %v, want the one dangling relation named", p.skipped)
	}
	if got := len(p.relations); got != 1 {
		t.Fatalf("%d relations planned, want 1: the dangling one must not be attempted", got)
	}
}

// Two runs are two graphs. The digest is what lets a second Load of the same
// result be recognised as a replay and a second Load of a different result
// under the same run ID be refused, so it has to depend on the content and not
// on the order it arrived in.
func TestRunDigest(t *testing.T) {
	a := alchemy.Result{
		Entities:  []alchemy.Entity{ent("e1", "System", "SuperAI"), ent("e2", "System", "CortexDB")},
		Relations: []alchemy.Relation{rel("e1", "e2", "USES")},
	}
	b := alchemy.Result{
		Entities:  []alchemy.Entity{ent("e2", "System", "CortexDB"), ent("e1", "System", "SuperAI")},
		Relations: []alchemy.Relation{rel("e1", "e2", "USES")},
	}
	if sink.Digest(a) != sink.Digest(b) {
		t.Fatalf("digest depends on the order records arrived in: %s vs %s", sink.Digest(a), sink.Digest(b))
	}
	c := a
	c.Entities = append([]alchemy.Entity{}, a.Entities...)
	c.Entities[0].Name = "SuperAI Ltd"
	if sink.Digest(a) == sink.Digest(c) {
		t.Fatal("digest is blind to a changed entity name: two different graphs share one run identity")
	}
	d := a
	d.Relations = []alchemy.Relation{rel("e2", "e1", "USES")}
	if sink.Digest(a) == sink.Digest(d) {
		t.Fatal("digest is blind to a reversed relation")
	}
	// Provenance is part of the graph's identity: the same edge attributed to
	// a different model is not the same import.
	e := a
	e.Relations = append([]alchemy.Relation{}, a.Relations...)
	e.Relations[0].Provenance.Model = "some-other-model"
	if sink.Digest(a) == sink.Digest(e) {
		t.Fatal("digest is blind to provenance: a re-run against another model would replay as if unchanged")
	}
}

// An edge's identity is the assertion, not the pair it connects. Two chunks
// both saying A USES B are two pieces of evidence with two provenances, and
// collapsing them onto one edge would leave one of them unable to name its
// producer.
func TestRelationKeyIsTheAssertion(t *testing.T) {
	r1 := rel("e1", "e2", "USES")
	r2 := rel("e1", "e2", "USES")
	if relationKey(r1) != relationKey(r2) {
		t.Fatal("the same assertion twice has two keys, so a replay would double the edge")
	}
	r2.Provenance.Chunk = 15
	if relationKey(r1) == relationKey(r2) {
		t.Fatal("two chunks asserting the same edge share a key, so one loses its provenance")
	}
}

// An ontology type that collides with one of the labels this connector uses
// for its own bookkeeping is refused. A type called "AlchemyViolation" would
// make `MATCH (v:AlchemyViolation)` — the query §5's numbers are read with —
// return the buyer's entities mixed in with the findings, and neither side
// would look wrong.
func TestRefusesInternalLabelCollision(t *testing.T) {
	for _, typ := range []string{"AlchemyRun", "AlchemyViolation", "AlchemyChunk"} {
		_, err := preflight(alchemy.Result{Entities: []alchemy.Entity{ent("e1", typ, "x")}}, opts())
		if err == nil || !strings.Contains(err.Error(), "BaseLabel") {
			t.Fatalf("type %q: err = %v, want a refusal that names the way out", typ, err)
		}
	}
	// The base label is the way out, so moving it must free the name.
	o := opts()
	o.BaseLabel = "Import"
	if _, err := preflight(alchemy.Result{Entities: []alchemy.Entity{ent("e1", "AlchemyRun", "x")}}, o); err != nil {
		t.Fatalf("moving the base label did not free the name: %v", err)
	}
}

// The digest decides whether a second load under one run ID is a replay or a
// contradiction, so it has to cover everything the load writes. Findings are
// written, so two results that agree about the graph and disagree about what
// is wrong with it are two different imports.
func TestDigestCoversFindings(t *testing.T) {
	a := alchemy.Result{Entities: []alchemy.Entity{ent("e1", "System", "SuperAI")}}
	b := a
	b.Violations = []alchemy.Violation{{Kind: alchemy.ViolationUnknownEntityType, Subject: "e1", Detail: "no"}}
	if sink.Digest(a) == sink.Digest(b) {
		t.Fatal("digest is blind to violations: a result that found a problem replays as one that did not")
	}
	c := a
	c.Duplicates = []alchemy.Duplicate{{Signal: alchemy.DuplicateNameAffix, Subject: "a ~ b"}}
	if sink.Digest(a) == sink.Digest(c) {
		t.Fatal("digest is blind to duplicates")
	}
	d := a
	d.Unread = []alchemy.Unread{{Source: "a.pdf", Locator: "page 9", Reason: "scanned"}}
	if sink.Digest(a) == sink.Digest(d) {
		t.Fatal("digest is blind to unread source material")
	}
	e := a
	e.Guesses = []alchemy.Guess{{Field: "owner_id", ChosenAs: "Person"}}
	if sink.Digest(a) == sink.Digest(e) {
		t.Fatal("digest is blind to guesses")
	}
}
