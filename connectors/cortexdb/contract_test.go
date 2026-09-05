package cortexdb

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
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
// Two of the spec's rows are not here and their absence is the finding rather
// than an omission: a rejected record never reaches a sink, and a held result
// is refused before the first write. The `_contradicts` row is not here either,
// for a different and smaller reason — this fixture has no conflict in it, so
// there is no disagreement to record; contradicting() is the one that does.
// See contract.go.
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
// normalised.
//
// _contradicts is absent here for a plainer reason and is checked anyway: this
// result contains no conflict, so no record disagrees with another, and the key
// says nothing rather than "[]". "This record disagrees with nothing" and "this
// record's disagreements are the empty list" are not the same claim, and the
// second is one pkg/cortexdb.ValidateContract rejects.
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

// contradicting is the row this connector could not write until a Conflict's
// sides carried Refs: two records the graph holds, that cannot both be true,
// neither of them being removed.
//
// A direction reversal is the plainest shape of it and the one argus produces
// by the hour — two outlets narrating the same event with the arrow the other
// way round. The reviewed side is what makes it loadable at all: §7.3 refuses a
// result with an unanswered conflict before the first write, so what reaches a
// store is always a question somebody answered, which is exactly the case the
// contract says both records survive.
func contradicting() alchemy.Result {
	reuters := alchemy.Provenance{
		Source: "reuters.html", Chunk: 0, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Ontology: "world@3", Chunking: "heading", Confidence: 0.77,
	}
	ap := alchemy.Provenance{
		Source: "apnews.html", Chunk: 0, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Ontology: "world@3", Chunking: "heading", Confidence: 0.64,
		ReviewedBy: "ada@example.com",
	}
	one := alchemy.Relation{From: "p:ada", To: "org:northgate", Type: "LEADS", Provenance: reuters}
	two := alchemy.Relation{From: "org:northgate", To: "p:ada", Type: "LEADS", Provenance: ap}
	ref := func(r alchemy.Relation) alchemy.Ref {
		return alchemy.Ref{Kind: alchemy.RefRelation, From: r.From, To: r.To, Type: r.Type, Key: r.Key}
	}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "p:ada", Type: "Person", Name: "Ada", Provenance: reuters},
			{ID: "org:northgate", Type: "Organization", Name: "Northgate", Provenance: reuters},
		},
		Relations: []alchemy.Relation{one, two},
		Conflicts: []alchemy.Conflict{{
			Kind:    alchemy.ConflictRelationDirection,
			Subject: "org:northgate -[LEADS]- p:ada",
			Detail:  "one outlet has Ada leading Northgate and the other has it the other way round",
			Left:    alchemy.Claim{Statement: "Ada leads Northgate", About: ref(one), Provenance: reuters},
			Right:   alchemy.Claim{Statement: "Northgate leads Ada", About: ref(two), Provenance: ap},
		}},
	}
}

// edgeRecords is every alchemy edge this load wrote, by the id CortexDB gave
// it, so an assertion about `_contradicts` is checked against ids the STORE
// chose rather than against ids this test rebuilt the same wrong way the
// connector might have.
func edgeRecords(t *testing.T, l *Loader) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	rows, err := l.db().SQL().QueryContext(context.Background(),
		"SELECT id, COALESCE(properties,'{}') FROM graph_edges "+
			"WHERE from_node_id LIKE 'entity:alchemy:%' AND to_node_id LIKE 'entity:alchemy:%'")
	if err != nil {
		t.Fatalf("query edges: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[id] = decodeProps(t, raw)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// The row itself: whoever detects a disagreement writes it on BOTH records, as
// a JSON array of the ids of the other side. The disagreement is information,
// not an error, and both records stay.
func TestBothSidesOfADisagreementNameTheOther(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-C"})
	if _, err := l.Load(context.Background(), contradicting()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	edges := edgeRecords(t, l)
	var marked []string
	for id, props := range edges {
		if _, ok := props["_contradicts"]; ok {
			marked = append(marked, id)
		}
	}
	if len(marked) != 2 {
		t.Fatalf("%d of %d edges carry _contradicts, want both sides of the one conflict: %v",
			len(marked), len(edges), edges)
	}
	sort.Strings(marked)
	for i, id := range marked {
		other := marked[1-i]
		ids := contradictsOf(t, edges[id]["_contradicts"])
		if len(ids) != 1 || ids[0] != other {
			t.Errorf("edge %s: _contradicts = %v, want exactly [%s] — the id of the record it "+
				"disagrees with, as the store knows it", id, ids, other)
		}
	}
}

// The contract's own validation of this key, applied where the record is
// written rather than where it is read: pkg/cortexdb.ValidateContract parses
// _contradicts as a JSON array of ids and rejects an empty one. A connector
// that wrote a comma-joined list, or a bare id, or `[""]` would put a record in
// a shared brain that every conformant reader refuses.
func TestTheContradictsKeyIsTheJSONArrayTheContractValidates(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-CJ"})
	if _, err := l.Load(context.Background(), contradicting()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := 0
	for id, props := range edgeRecords(t, l) {
		raw, ok := props["_contradicts"]
		if !ok {
			continue
		}
		found++
		s, isString := raw.(string)
		if !isString {
			t.Errorf("edge %s: _contradicts is %T; the contract's keys are metadata strings", id, raw)
			continue
		}
		var ids []string
		if err := json.Unmarshal([]byte(s), &ids); err != nil {
			t.Errorf("edge %s: _contradicts %q is not a JSON array of ids: %v", id, s, err)
			continue
		}
		if len(ids) == 0 {
			t.Errorf("edge %s: _contradicts is an empty array; a record that disagrees with "+
				"nothing carries no key at all", id)
		}
		for _, other := range ids {
			if strings.TrimSpace(other) == "" {
				t.Errorf("edge %s: _contradicts contains an empty id: %q", id, s)
			}
		}
	}
	if found == 0 {
		t.Fatal("no record carries _contradicts; the fixture is not exercising the key")
	}
}

// Every record the load wrote still passes everything else the contract asks
// of it. `_contradicts` is an addition and must not cost a grade, a reason or
// a time — the four rows this connector could already fill.
func TestARecordThatContradictsAnotherStillCarriesTheRestOfTheContract(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-CR"})
	if _, err := l.Load(context.Background(), contradicting()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, props := range everyRecord(t, l) {
		grade, _ := props["_grade"].(string)
		if grade == "" {
			t.Errorf("a record carries no _grade: %v", props)
		}
		if at, _ := props["_at"].(string); at == "" {
			t.Errorf("a record carries no _at: %v", props)
		}
		if src, _ := props["_source"].(string); src == "" {
			t.Errorf("a record carries no _source: %v", props)
		}
		if (grade == gradeRefused || grade == gradeHeld) && props["_why"] == nil {
			t.Errorf("%s record with no _why: %v", grade, props)
		}
	}
}

// The other half of truthfulness, kept: a disagreement INSIDE one record is not
// a `_contradicts`, because there is no other record to name.
//
// alchemy.Claim.About says so in the type — two claims about one node carry one
// Ref twice — and this is the store's side of it. An entity given two values
// for one attribute is one node in the graph; writing its own id into its own
// `_contradicts` would answer "which record disagrees with this one" with the
// record itself, which every reader would then follow back to where it started.
func TestARecordDoesNotContradictItself(t *testing.T) {
	res := contradicting()
	inside := res.Conflicts[0]
	inside.Kind = alchemy.ConflictEntityAttributes
	inside.Subject = "org:northgate.founded"
	inside.Detail = "two outlets give the founding year differently"
	self := alchemy.Ref{Kind: alchemy.RefEntity, ID: "org:northgate", Type: "Organization"}
	inside.Left = alchemy.Claim{Statement: "founded 1997", About: self, Provenance: res.Relations[0].Provenance}
	inside.Right = alchemy.Claim{Statement: "founded 1998", About: self, Provenance: res.Relations[1].Provenance}
	res.Conflicts = []alchemy.Conflict{inside}

	l := openLocal(t, Options{RunID: "run-CS"})
	if _, err := l.Load(context.Background(), res); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, props := range everyRecord(t, l) {
		if v, ok := props["_contradicts"]; ok {
			t.Errorf("_contradicts = %#v on a conflict whose two sides name one record", v)
		}
	}
}

func contradictsOf(t *testing.T, raw any) []string {
	t.Helper()
	s, ok := raw.(string)
	if !ok {
		t.Fatalf("_contradicts is %T, want the contract's JSON array in a metadata string", raw)
	}
	var ids []string
	if err := json.Unmarshal([]byte(s), &ids); err != nil {
		t.Fatalf("_contradicts %q is not a JSON array: %v", s, err)
	}
	return ids
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
