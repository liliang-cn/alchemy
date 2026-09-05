package cortexdb

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The literals of the knowledge contract, pinned.
//
// contract.go here is a copy of CortexDB's pkg/cortexdb/contract.go, made
// because that file is not in any published version of the store (see the note
// there). A copy nobody checks is a copy that drifts, and the drift is silent:
// a record graded "selfconsistent" against a reader filtering on
// "self_consistent" is not a failure anybody sees, it is a graph that answers
// with half of itself. So every string is written out here as the spec's own
// table writes it, and the day the constants are published this test is what
// says the two agree.
func TestEveryLiteralOfTheKnowledgeContractIsWrittenAsTheSpecWroteIt(t *testing.T) {
	for got, want := range map[string]string{
		keyGrade: "grade", keyState: "state", keyWhy: "why", keyContradicts: "contradicts",

		gradeVerified: "verified", gradeSelfConsistent: "self_consistent",
		gradeAsserted: "asserted", gradeHeld: "held", gradeRefused: "refused",
	} {
		if got != want {
			t.Errorf("contract literal = %q, want %q", got, want)
		}
	}
	// The prefix is an Option and the contract fixes it at "_". A store loaded
	// under any other one is a store whose contract keys no cross-producer
	// reader will find.
	if defaultReservedPrefix != "_" {
		t.Fatalf("default reserved prefix = %q; the contract's keys are the %q-prefixed ones", defaultReservedPrefix, "_")
	}
}

// graded is one result per reachable row of the spec's alchemy mapping table,
// so that one load exercises the whole of what this connector can establish.
//
// Three of the spec's seven rows are not here and their absence is the finding
// rather than an omission: a rejected record never reaches a sink, a held
// result is refused before the first write, and a Conflict names its two sides
// in prose. See contract.go.
func graded() alchemy.Result {
	ddl := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "sds@3"}
	tabular := alchemy.Provenance{Source: "people.csv", Chunk: -1, Producer: alchemy.ProducerTabular, Ontology: "sds@3"}
	llm := alchemy.Provenance{
		Source: "architecture.pdf", Chunk: 0, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Ontology: "sds@3", Chunking: "heading", Confidence: 0.41,
	}
	reviewed := llm
	reviewed.ReviewedBy = "ada@example.com"
	human := alchemy.Provenance{
		Source: "correction.md", Chunk: -1, Producer: alchemy.ProducerHuman,
		By: "ana@example.com", At: "2026-03-01T00:00:00Z",
	}

	bad := alchemy.Entity{ID: "x1", Type: "Deployment", Name: "prod", Provenance: llm}
	badEdge := alchemy.Relation{From: "x1", To: "d1", Type: "DEPLOYS", Provenance: llm}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "d1", Type: "System", Name: "CortexDB", Provenance: ddl},
			{ID: "t1", Type: "Person", Name: "Ada", Provenance: tabular},
			{ID: "m1", Type: "System", Name: "SuperAI", Provenance: llm},
			{ID: "r1", Type: "System", Name: "Alchemy", Provenance: reviewed},
			{ID: "h1", Type: "Person", Name: "Ana", Provenance: human},
			bad,
		},
		Relations: []alchemy.Relation{
			{From: "d1", To: "t1", Type: "USES", Provenance: ddl},
			{From: "m1", To: "d1", Type: "MENTIONS", Provenance: llm},
			{From: "r1", To: "d1", Type: "OWNS", Provenance: reviewed},
			badEdge,
		},
		// The two findings that name a record in fields. Violation.About is
		// what makes them joinable at all; a violation about a file (a
		// malformed row, an unnamed column) carries a zero Ref and grades
		// nothing.
		Violations: []alchemy.Violation{
			{
				Kind: alchemy.ViolationUnknownEntityType, Subject: "x1",
				About:  alchemy.Ref{Kind: alchemy.RefEntity, ID: "x1", Type: "Deployment"},
				Detail: `entity type "Deployment" is not declared by ontology "sds@3"`, Provenance: llm,
			},
			{
				Kind: alchemy.ViolationUnknownRelationType, Subject: "x1 -[DEPLOYS]-> d1",
				About:  alchemy.Ref{Kind: alchemy.RefRelation, From: "x1", To: "d1", Type: "DEPLOYS"},
				Detail: `relation type "DEPLOYS" is not declared by ontology "sds@3"`, Provenance: llm,
			},
			{
				Kind: alchemy.ViolationMalformedRow, Subject: "people.csv:12",
				Detail: "row 12 has 4 fields and the header has 5", Provenance: tabular,
			},
		},
	}
}

// The mapping table, row by row, read back out of the store rather than out of
// the function that wrote it.
func TestEachRowOfTheMappingTableGetsTheGradeTheSpecGivesIt(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-G"})
	if _, err := l.Load(context.Background(), graded()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	for id, want := range map[string]string{
		"d1": gradeSelfConsistent, // deterministic: a CREATE TABLE said so
		"t1": gradeSelfConsistent, // tabular, likewise, whatever Deterministic() says
		"m1": gradeAsserted,       // a model said so and nothing has checked it
		"r1": gradeVerified,       // a named person looked and kept it
		"h1": gradeAsserted,       // a person asserting is still nobody checking
		"x1": gradeRefused,        // the ontology would not have it
	} {
		np := nodeProps(t, l, entityNodeID("run-G", id))
		if got, _ := np["_grade"].(string); got != want {
			t.Errorf("entity %s: _grade = %#v, want %q", id, np["_grade"], want)
		}
	}

	for edge, want := range map[string]string{
		"USES":     gradeSelfConsistent,
		"MENTIONS": gradeAsserted,
		"OWNS":     gradeVerified,
		"DEPLOYS":  gradeRefused,
	} {
		ep := edgeProps(t, l, edge)
		if got, _ := ep["_grade"].(string); got != want {
			t.Errorf("edge %s: _grade = %#v, want %q", edge, ep["_grade"], want)
		}
	}
}

// §5b's promise applied to the one grade a reader acts on by deleting: "a
// refusal without a reason is noise the reader will delete". The contract makes
// _why required beside _grade=refused, and this is the invariant rather than
// the example — every record this connector writes, in every fixture it has.
func TestNothingIsRefusedWithoutSayingWhy(t *testing.T) {
	for name, res := range map[string]alchemy.Result{"graded": graded(), "fixture": fixture()} {
		t.Run(name, func(t *testing.T) {
			l := openLocal(t, Options{RunID: "run-W"})
			if _, err := l.Load(context.Background(), res); err != nil {
				t.Fatalf("Load: %v", err)
			}
			for _, props := range everyRecord(t, l) {
				grade, _ := props["_grade"].(string)
				if grade == "" {
					t.Errorf("a record carries no _grade at all: %v", props)
					continue
				}
				if grade != gradeRefused && grade != gradeHeld {
					continue
				}
				if why, _ := props["_why"].(string); why == "" {
					t.Errorf("%s record with no _why: %v", grade, props)
				}
			}
		})
	}
}

// The producer's own word, verbatim and un-normalised, for the one row that
// has one. §7.3 calls a violation attributable; the kind is what it is
// attributable to, and a reader filtering "show me what the vocabulary
// rejected" reads it rather than parsing the sentence in _why.
func TestARefusedRecordCarriesTheOntologysOwnWords(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-V"})
	if _, err := l.Load(context.Background(), graded()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	np := nodeProps(t, l, entityNodeID("run-V", "x1"))
	if got, _ := np["_state"].(string); got != "violation:unknown_entity_type" {
		t.Errorf("entity x1: _state = %#v, want the ViolationKind verbatim", np["_state"])
	}
	if got, _ := np["_why"].(string); got != `entity type "Deployment" is not declared by ontology "sds@3"` {
		t.Errorf("entity x1: _why = %#v, want the finding's own detail", np["_why"])
	}
	ep := edgeProps(t, l, "DEPLOYS")
	if got, _ := ep["_state"].(string); got != "violation:unknown_relation_type" {
		t.Errorf("edge DEPLOYS: _state = %#v, want the ViolationKind verbatim", ep["_state"])
	}
	if got, _ := ep["_why"].(string); got == "" {
		t.Error("edge DEPLOYS: no _why")
	}
}

// The other half of truthfulness: a key this connector cannot fill is absent,
// not empty and not guessed.
//
// _state is the producer's own fine-grained word and alchemy has one for
// exactly one row. A record somebody accepted arrives carrying ReviewedBy — a
// name — and no verb: review.Apply removes what a reviewer rejected and stamps
// the survivors with who looked, and neither the Provenance nor the Result
// carries which of accept/edit/always it was. Writing "accept" there would be
// this connector inventing the one thing the contract says is never
// normalised. _contradicts is absent for its own reason; see contract.go.
func TestAKeyThisConnectorCannotFillIsAbsentRatherThanGuessed(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-A"})
	if _, err := l.Load(context.Background(), graded()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	np := nodeProps(t, l, entityNodeID("run-A", "r1"))
	if _, invented := np["_state"]; invented {
		t.Errorf("a reviewed entity carries _state = %#v; alchemy does not record the verb", np["_state"])
	}
	if _, ok := np["_why"]; ok {
		t.Errorf("a verified entity carries _why = %#v", np["_why"])
	}
	for _, props := range everyRecord(t, l) {
		if _, ok := props["_contradicts"]; ok {
			t.Errorf("_contradicts = %#v on a record this connector cannot join to a Conflict", props["_contradicts"])
		}
	}
}

// A fused edge answers as its least-established member, which is the rule
// writeRelations already applies to `inferred` and for the same reason: an edge
// one source stated deterministically and a model also proposed is still an
// edge a model proposed. Here: one member a person accepted and one nobody
// looked at is not a verified edge.
func TestAFusedEdgeIsGradedByItsLeastEstablishedMember(t *testing.T) {
	res := graded()
	reviewed := res.Relations[2].Provenance // r1 -[OWNS]-> d1, ReviewedBy set
	unchecked := reviewed
	unchecked.ReviewedBy = ""
	res.Relations = append(res.Relations, alchemy.Relation{
		From: "r1", To: "d1", Type: "OWNS", Provenance: unchecked,
	})

	l := openLocal(t, Options{RunID: "run-F"})
	if _, err := l.Load(context.Background(), res); err != nil {
		t.Fatalf("Load: %v", err)
	}
	ep := edgeProps(t, l, "OWNS")
	if got, _ := ep["_grade"].(string); got != gradeAsserted {
		t.Errorf("fused OWNS edge: _grade = %#v, want %q — one reviewed member does not vouch for the other", ep["_grade"], gradeAsserted)
	}
	if got, _ := ep["_assertions"].(string); got != "2" {
		t.Fatalf("the fixture did not fuse: _assertions = %#v", ep["_assertions"])
	}
}

// The contract's keys are alchemy's bookkeeping and must read back as such:
// under the reserved prefix, and therefore out of the map whose documented
// meaning is "what the source said about this thing".
func TestTheContractKeysDoNotLeakIntoWhatTheSourceSaid(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-L"})
	if _, err := l.Load(context.Background(), graded()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	d, err := l.Describe(context.Background(), "run-L", "x1")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	for _, k := range []string{"_grade", "_state", "_why", "grade", "state", "why"} {
		if _, leaked := d.Attributes[k]; leaked {
			t.Errorf("Describe reports %q as an attribute of the source: %v", k, d.Attributes)
		}
	}
}

// everyRecord is every node and edge this load wrote, as CortexDB holds them.
func everyRecord(t *testing.T, l *Loader) []map[string]any {
	t.Helper()
	var out []map[string]any
	// Alchemy's own records and only those. CortexDB writes a mention edge
	// from every chunk that names an entity, which is its record rather than
	// this connector's: the contract asks a producer to grade what it produced,
	// and the same structural test claims.go uses says which those are.
	for _, q := range []string{
		"SELECT COALESCE(properties,'{}') FROM graph_nodes WHERE id LIKE 'entity:alchemy:%'",
		"SELECT COALESCE(properties,'{}') FROM graph_edges " +
			"WHERE from_node_id LIKE 'entity:alchemy:%' AND to_node_id LIKE 'entity:alchemy:%'",
	} {
		rows, err := l.db().SQL().QueryContext(context.Background(), q)
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				t.Fatalf("scan: %v", err)
			}
			out = append(out, decodeProps(t, raw))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatalf("rows: %v", err)
		}
	}
	if len(out) == 0 {
		t.Fatal("the load wrote nothing")
	}
	return out
}

func decodeProps(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}

// The contract requires _at, and alchemy stamps no clock on an extraction:
// Result is content-addressed, so a clock on it would change every address ever
// produced. This store therefore says when *it* put the record on the shelf —
// once per load, so a reader can tell "the same load" from "the same second" —
// and a producer that did say when (ProducerHuman) keeps its own word.
func TestEveryRecordSaysWhenItWentOnTheShelf(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-AT"})
	before := time.Now().UTC().Add(-time.Second)
	if _, err := l.Load(context.Background(), graded()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	shelf := map[string]int{}
	for _, rec := range everyRecord(t, l) {
		raw, _ := rec["_at"].(string)
		if raw == "" {
			t.Errorf("record without _at: %v", rec)
			continue
		}
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t.Errorf("_at %q is not RFC 3339: %v", raw, err)
			continue
		}
		if by, _ := rec["_by"].(string); by == "ana@example.com" {
			// The one record whose producer said when. Its word, not ours.
			if raw != "2026-03-01T00:00:00Z" {
				t.Errorf("human assertion's own At was overwritten: %q", raw)
			}
			continue
		}
		if at.Before(before) || at.After(after) {
			t.Errorf("_at %q is not this load's moment (%v .. %v)", raw, before, after)
		}
		shelf[raw]++
	}
	// One load, one moment. Two different seconds across one load's records
	// would let a reader mistake a slow load for two loads.
	if len(shelf) != 1 {
		t.Errorf("one load wrote %d distinct shelf moments: %v", len(shelf), shelf)
	}
}
