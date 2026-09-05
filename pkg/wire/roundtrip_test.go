package wire_test

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/wire"
)

// everything builds a Result in which every field of every type this package
// converts is populated, and no two fields hold the same value.
//
// Distinct values everywhere is the whole method. A struct copied field by
// field into another struct cannot be checked by the compiler, so a
// transposition — From written into To, Left into Right, RuleSet into RuledBy
// — is invisible to any test whose fixture repeats a value. It is not a
// hypothetical: provenanceToProto copied ten fields and kept copying ten after
// alchemy.Provenance grew By and At, and no test noticed for as long as the
// two directions were never compared.
func everything() alchemy.Result {
	// One provenance per producer, each with every scalar set to something no
	// other provenance in this result holds, so that a record arriving with
	// another record's source is a failure rather than a coincidence.
	prov := func(n int, p alchemy.Producer) alchemy.Provenance {
		id := strconv.Itoa(n)
		return alchemy.Provenance{
			Source:     "source-" + id + ".pdf",
			Chunk:      100 + n,
			Producer:   p,
			Model:      "model-" + id,
			Ontology:   "ontology-" + id,
			Chunking:   "chunking-" + id,
			Confidence: float64(n) / 128,
			ReviewedBy: "reviewer-" + id,
			RuleSet:    "ruleset-" + id,
			RuledBy:    "ruledby-" + id,
			At:         fmt.Sprintf("2026-08-30T%02d:%02d:00Z", n/60, n%60),
			By:         "by-" + id,
		}
	}

	return alchemy.Result{
		Job: "job-42",
		Entities: []alchemy.Entity{
			{
				ID: "e1", Type: "Person", Name: "Theodore",
				Aliases: []string{"Theo", "P."},
				// The JSON value domain and nothing else, per
				// alchemy.Entity.Attributes: structpb carries exactly these,
				// and an int here would come back a float64 and the failure
				// would be about the fixture rather than about the code.
				Attributes: map[string]any{
					"region": "eu-central",
					"rank":   float64(3),
					"active": true,
					"note":   nil,
					"tags":   []any{"a", "b"},
					"nested": map[string]any{"depth": float64(2)},
				},
				Provenance: prov(0, alchemy.ProducerDDL),
			},
			{ID: "e2", Type: "Org", Name: "Northgate", Provenance: prov(1, alchemy.ProducerGraphImport)},
			{ID: "e3", Type: "Product", Name: "Ravel", Provenance: prov(2, alchemy.ProducerTabular)},
			{ID: "e4", Type: "Team", Name: "Ops", Provenance: prov(3, alchemy.ProducerLLMExtract)},
			{ID: "e5", Type: "Role", Name: "CTO", Provenance: prov(4, alchemy.ProducerHuman)},
		},
		Relations: []alchemy.Relation{
			{
				From: "e1", To: "e2", Type: "WORKS_AT",
				// The parallel-edge case Relation.Key exists for: without a
				// key these two rows are one edge described twice.
				Key:        "fk_node_conn_source",
				Attributes: map[string]any{"since": "2019"},
				Provenance: prov(5, alchemy.ProducerDDL),
			},
			{From: "e1", To: "e2", Type: "WORKS_AT", Key: "fk_node_conn_target", Provenance: prov(6, alchemy.ProducerHuman)},
			{From: "e2", To: "e3", Type: "SHIPS", Provenance: prov(7, alchemy.ProducerLLMExtract)},
		},
		Chunks: []alchemy.Chunk{
			{Index: 0, Text: "first", Source: "a.md", Strategy: "heading", Heading: "Intro", Start: 0, End: 5},
			{Index: 1, Text: "second", Source: "b.md", Strategy: "fixed", Start: 7, End: 13},
		},
		Vectors: []alchemy.Vector{
			{Chunk: 0, Values: []float32{0.5, -0.25}, Model: "embed-1"},
			{Chunk: 1, Values: []float32{0.125}, Model: "embed-2"},
		},
		Violations: []alchemy.Violation{
			{
				Kind: alchemy.ViolationUnknownEntityType, Detail: "d1", Subject: "s1",
				About:      alchemy.Ref{Kind: alchemy.RefEntity, ID: "e1", Type: "Person"},
				Provenance: prov(8, alchemy.ProducerDDL),
			},
			{
				Kind: alchemy.ViolationUnknownRelationType, Detail: "d2", Subject: "s2",
				About:      alchemy.Ref{Kind: alchemy.RefRelation, From: "e1", To: "e2", Type: "WORKS_AT", Key: "k2"},
				Provenance: prov(9, alchemy.ProducerLLMExtract),
			},
			{Kind: alchemy.ViolationRelationNotAllowed, Detail: "d3", Subject: "s3", Provenance: prov(10, alchemy.ProducerTabular)},
			{Kind: alchemy.ViolationDanglingRelation, Detail: "d4", Subject: "s4", Provenance: prov(11, alchemy.ProducerHuman)},
			// The four source-shaped kinds are about a file and carry a zero
			// About on purpose; see alchemy.Violation.About. The round trip has
			// to keep a zero Ref zero — an empty Ref on the wire says "the
			// entity with no id", which is joinable and false.
			{Kind: alchemy.ViolationMalformedRow, Detail: "d5", Subject: "s5", Provenance: prov(12, alchemy.ProducerTabular)},
			{Kind: alchemy.ViolationUnnamedColumn, Detail: "d6", Subject: "s6", Provenance: prov(13, alchemy.ProducerTabular)},
			{Kind: alchemy.ViolationMissingID, Detail: "d7", Subject: "s7", Provenance: prov(14, alchemy.ProducerTabular)},
			{Kind: alchemy.ViolationDuplicateID, Detail: "d8", Subject: "s8", Provenance: prov(15, alchemy.ProducerDDL)},
		},
		// Every claim names the record it was read from, and no two of the
		// twelve name the same one, so a converter that copied Left.About into
		// Right — or dropped it, as the pre-round-trip converters dropped
		// Provenance.By — fails here rather than in a store that silently stops
		// writing `_contradicts`. The one exception is deliberate and is the
		// other case the converter has to get right: c5's two sides name no
		// record at all.
		Conflicts: []alchemy.Conflict{
			{Kind: alchemy.ConflictEntityAttributes, Subject: "c1", Detail: "cd1",
				Left: alchemy.Claim{Statement: "l1", About: alchemy.Ref{Kind: alchemy.RefEntity, ID: "e1", Type: "Person"},
					Provenance: prov(16, alchemy.ProducerDDL)},
				Right: alchemy.Claim{Statement: "r1", About: alchemy.Ref{Kind: alchemy.RefEntity, ID: "e2", Type: "Org"},
					Provenance: prov(17, alchemy.ProducerLLMExtract)}},
			{Kind: alchemy.ConflictEntityType, Subject: "c2", Detail: "cd2",
				Left: alchemy.Claim{Statement: "l2", About: alchemy.Ref{Kind: alchemy.RefEntity, ID: "e3", Type: "Product"},
					Provenance: prov(18, alchemy.ProducerTabular)},
				Right: alchemy.Claim{Statement: "r2", About: alchemy.Ref{Kind: alchemy.RefEntity, ID: "e3", Type: "Component"},
					Provenance: prov(19, alchemy.ProducerHuman)}},
			{Kind: alchemy.ConflictRelationDirection, Subject: "c3", Detail: "cd3",
				Left: alchemy.Claim{Statement: "l3", About: alchemy.Ref{Kind: alchemy.RefRelation, From: "e1", To: "e2", Type: "WORKS_AT", Key: "k3"},
					Provenance: prov(20, alchemy.ProducerGraphImport)},
				Right: alchemy.Claim{Statement: "r3", About: alchemy.Ref{Kind: alchemy.RefRelation, From: "e2", To: "e1", Type: "WORKS_AT", Key: "k4"},
					Provenance: prov(21, alchemy.ProducerDDL)}},
			{Kind: alchemy.ConflictContradiction, Subject: "c4", Detail: "cd4",
				Left: alchemy.Claim{Statement: "l4", About: alchemy.Ref{Kind: alchemy.RefRelation, From: "e2", To: "e3", Type: "SHIPS", Key: "k5"},
					Provenance: prov(22, alchemy.ProducerDDL)},
				Right: alchemy.Claim{Statement: "r4", About: alchemy.Ref{Kind: alchemy.RefRelation, From: "e3", To: "e2", Type: "SHIPS", Key: "k6"},
					Provenance: prov(23, alchemy.ProducerLLMExtract)}},
			// A side that names no record, which is the case AboutToProto turns
			// into no message at all: an empty Ref on the wire says "the entity
			// with no id", which is joinable and false.
			{Kind: alchemy.ConflictRelationAttributes, Subject: "c5", Detail: "cd5",
				Left:  alchemy.Claim{Statement: "l5", Provenance: prov(24, alchemy.ProducerTabular)},
				Right: alchemy.Claim{Statement: "r5", Provenance: prov(25, alchemy.ProducerTabular)}},
			{Kind: alchemy.ConflictCardinality, Subject: "c6", Detail: "cd6",
				Left: alchemy.Claim{Statement: "l6", About: alchemy.Ref{Kind: alchemy.RefRelation, From: "e1", To: "e4", Type: "MEMBER_OF", Key: "k7"},
					Provenance: prov(26, alchemy.ProducerHuman)},
				Right: alchemy.Claim{Statement: "r6", About: alchemy.Ref{Kind: alchemy.RefRelation, From: "e5", To: "e4", Type: "MEMBER_OF", Key: "k8"},
					Provenance: prov(27, alchemy.ProducerGraphImport)}},
		},
		Duplicates: []alchemy.Duplicate{
			{Signal: alchemy.DuplicateNameAffix, Subject: "u1", Detail: "ud1",
				Left:  alchemy.DuplicateSide{ID: "e3", Type: "Product", Name: "Ravel", Provenance: prov(28, alchemy.ProducerLLMExtract)},
				Right: alchemy.DuplicateSide{ID: "e3b", Type: "Product", Name: "Ravel package", Provenance: prov(29, alchemy.ProducerLLMExtract)}},
			{Signal: alchemy.DuplicateNameAcrossProducers, Subject: "u2", Detail: "ud2",
				Left:  alchemy.DuplicateSide{ID: "org:northgate", Type: "Org", Name: "Northgate", Provenance: prov(30, alchemy.ProducerGraphImport)},
				Right: alchemy.DuplicateSide{ID: "organization:northgate", Type: "Org", Name: "Northgate", Provenance: prov(31, alchemy.ProducerLLMExtract)}},
			{Signal: alchemy.DuplicateAlias, Subject: "u3", Detail: "ud3",
				Left:  alchemy.DuplicateSide{ID: "e1", Type: "Person", Name: "Theodore", Provenance: prov(32, alchemy.ProducerHuman)},
				Right: alchemy.DuplicateSide{ID: "e1b", Type: "Person", Name: "Theo", Provenance: prov(33, alchemy.ProducerLLMExtract)}},
		},
		Guesses: []alchemy.Guess{
			{Field: "g1", ChosenAs: "ga1", Alternatives: []string{"alt1", "alt2"}, Reason: "gr1", Provenance: prov(34, alchemy.ProducerTabular)},
			{Field: "g2", ChosenAs: "ga2", Provenance: prov(35, alchemy.ProducerTabular)},
		},
		Unread: []alchemy.Unread{
			{Source: "scan.pdf", Locator: "p.7", Reason: "no OCR model supplied"},
			{Source: "book.xlsx", Locator: "Sheet3", Reason: "password protected"},
		},
		Counts: alchemy.Counts{
			Entities: 5, Relations: 3, Chunks: 2, Vectors: 2,
			Deterministic: 9, Inferred: 8, Violations: 8, Conflicts: 6,
			Guesses: 2, Duplicates: 3, ChunksEmpty: 11, ChunksUnread: 12, Dropped: 13,
		},
		ModelCalls: []alchemy.ModelCall{
			{Model: "m1", Stage: "extract", Calls: 17, Tokens: 1900},
			{Model: "m2", Stage: "embed", Calls: 21, Tokens: 2300},
		},
		RuleSets: []alchemy.RuleSet{
			{Name: "ruleset-a", Rules: []alchemy.StandingRule{
				{Name: "reviewed:unknown_entity_type/Widget", Told: "liliang accepted Widget on 2026-08-01"},
				{Name: "authored:guess/region", Told: "policy: region maps to region"},
			}},
			{Name: "ruleset-b", Rules: []alchemy.StandingRule{{Name: "reviewed:duplicate/Theo"}}},
		},
		Supersessions: []alchemy.Supersession{
			{
				Retires: "e5",
				By:      alchemy.Ref{Kind: alchemy.RefEntity, ID: "e5b", Type: "Role"},
				Reason:  "Bruno holds the office now",
				// The supersession's own provenance, not the superseding
				// record's: a reviewer may retire what a model proposed.
				Provenance: prov(36, alchemy.ProducerHuman),
			},
			{
				Retires:    "e1|WORKS_AT|e2|fk_node_conn_source",
				By:         alchemy.Ref{Kind: alchemy.RefRelation, From: "e1", To: "e2b", Type: "WORKS_AT", Key: "fk_new"},
				Reason:     "moved",
				Provenance: prov(37, alchemy.ProducerHuman),
			},
		},
		Proposals: []alchemy.Proposal{
			{
				Kind: alchemy.ProposalEntity, Type: "Team", Records: 4,
				Sources: []string{"a.md"}, Producers: []alchemy.Producer{alchemy.ProducerHuman},
				Example: alchemy.Ref{Kind: alchemy.RefEntity, ID: "e4", Type: "Team"},
			},
			{
				Kind: alchemy.ProposalRelation, Type: "MEMBER_OF", Records: 6,
				From: []string{"Person"}, To: []string{"Team"},
				Sources:   []string{"b.md", "c.md"},
				Producers: []alchemy.Producer{alchemy.ProducerLLMExtract, alchemy.ProducerTabular},
				Example:   alchemy.Ref{Kind: alchemy.RefRelation, From: "e1", To: "e4", Type: "MEMBER_OF", Key: "kx"},
			},
			{
				Kind: alchemy.ProposalRelationEnds, Type: "DEVELOPS", Records: 9,
				From: []string{"Person"}, To: []string{"Platform"},
				DeclaredFrom: []string{"Person"}, DeclaredTo: []string{"Product", "Component"},
				Sources:   []string{"d.md"},
				Producers: []alchemy.Producer{alchemy.ProducerDDL, alchemy.ProducerGraphImport},
				Example:   alchemy.Ref{Kind: alchemy.RefRelation, From: "e1", To: "e3", Type: "DEVELOPS"},
			},
		},
	}
}

// TestAResultWithEveryFieldPopulatedSurvivesTheWireInBothDirections is the test
// this package was built to make possible.
//
// Until now there was a converter to protobuf and no converter back, so no
// test could compare a result with the result. Every check was on one field at
// a time, written after somebody noticed that field was missing, which is why
// Provenance.By and Provenance.At were dropped for a while and why
// alchemy.ProducerHuman serialised as PRODUCER_UNSPECIFIED: a table lookup
// that misses returns the zero value, and a field-by-field copy that misses a
// field returns a struct that looks fine.
//
// Deep equality against the original is the only assertion that catches all of
// those at once. If it ever fails on a field protobuf genuinely cannot carry,
// the fix is to say so in the proto and here — not to compare fewer fields.
func TestAResultWithEveryFieldPopulatedSurvivesTheWireInBothDirections(t *testing.T) {
	want := everything()
	got := wire.ResultFromProto(wire.ResultToProto(want))

	// Compared a field at a time first, so that a failure names the field
	// rather than printing two four-kilobyte structs and leaving somebody to
	// diff them by eye. A test whose output nobody reads is a test that gets
	// skipped rather than fixed.
	for _, part := range []struct {
		name      string
		got, want any
	}{
		{"Job", got.Job, want.Job},
		{"Entities", got.Entities, want.Entities},
		{"Relations", got.Relations, want.Relations},
		{"Chunks", got.Chunks, want.Chunks},
		{"Vectors", got.Vectors, want.Vectors},
		{"Conflicts", got.Conflicts, want.Conflicts},
		{"Violations", got.Violations, want.Violations},
		{"Guesses", got.Guesses, want.Guesses},
		{"Duplicates", got.Duplicates, want.Duplicates},
		{"Counts", got.Counts, want.Counts},
		{"ModelCalls", got.ModelCalls, want.ModelCalls},
		{"Unread", got.Unread, want.Unread},
		{"Supersessions", got.Supersessions, want.Supersessions},
		{"Proposals", got.Proposals, want.Proposals},
		{"RuleSets", got.RuleSets, want.RuleSets},
	} {
		if !reflect.DeepEqual(part.got, part.want) {
			t.Errorf("Result.%s did not survive the round trip\n got %#v\nwant %#v",
				part.name, part.got, part.want)
		}
	}

	// And then the whole struct, because the list above can go stale and a
	// field added to alchemy.Result is exactly the case this test is for. If
	// this fires and nothing above did, the field is one nobody listed.
	if !reflect.DeepEqual(got, want) && !t.Failed() {
		t.Errorf("the result did not survive the round trip and no field above says why — "+
			"alchemy.Result has grown a field that is neither converted nor listed here\n"+
			" got %#v\nwant %#v", got, want)
	}
}

// TestEveryProducerStaysDeterministicAcrossTheWire is the round trip asked the
// question a buyer actually asks of a graph.
//
// It is separate from the deep-equality test above because it fails with a
// different sentence. Deep equality says "these two structs differ"; this says
// the graph changed its mind about which half of §5b's split it belongs to,
// which is the consequence anybody would notice in production and the last
// thing they would suspect a converter of.
func TestEveryProducerStaysDeterministicAcrossTheWire(t *testing.T) {
	res := wire.ResultFromProto(wire.ResultToProto(everything()))
	for _, e := range res.Entities {
		p := e.Provenance.Producer
		if p.Deterministic() != wantDeterministic(p) {
			t.Errorf("entity %s: producer %q reports Deterministic()=%v after the round trip",
				e.ID, p, p.Deterministic())
		}
	}
}

func wantDeterministic(p alchemy.Producer) bool {
	switch p {
	case alchemy.ProducerDDL, alchemy.ProducerGraphImport, alchemy.ProducerHuman:
		return true
	default:
		return false
	}
}

// TestAReviewItemWithEveryFieldPopulatedSurvivesTheWireInBothDirections is the
// same test for the other half of this package.
//
// A queue item carries three things that are easy to drop and impossible to
// notice: the provenance on each target, which is what narrows a rejection to
// the side of a conflict the reviewer threw away rather than the side they
// kept; the rule that already answered the item, which is what stops the queue
// from silently getting shorter; and that rule's Origin, where the zero value
// means one of the two warrants and losing the marker reads the weaker as the
// stronger.
func TestAReviewItemWithEveryFieldPopulatedSurvivesTheWireInBothDirections(t *testing.T) {
	at := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	want := review.Item{
		ID: "item-1", Kind: review.KindDuplicate, Rank: 3, Index: 7,
		Subject: "Ravel ~ Ravel package", Summary: "are these the same thing?",
		Shape: "duplicate/Product",
		Targets: []review.Ref{
			{
				Ref:        alchemy.Ref{Kind: alchemy.RefEntity, ID: "e3", Type: "Product"},
				Provenance: alchemy.Provenance{Source: "a.md", Chunk: 4, Producer: alchemy.ProducerLLMExtract, Model: "m1", Confidence: 0.5},
			},
			{
				Ref:        alchemy.Ref{Kind: alchemy.RefRelation, From: "e1", To: "e3", Type: "SHIPS", Key: "fk1"},
				Provenance: alchemy.Provenance{Source: "b.sql", Chunk: -1, Producer: alchemy.ProducerDDL},
			},
		},
		SuppressedBy: &review.Rule{
			Shape:  "duplicate/Product",
			Kind:   review.KindDuplicate,
			Origin: review.OriginAuthored,
			From: review.Decision{
				ItemID: "", Verb: review.VerbAlways, By: "liliang",
				Edit: &review.Edit{Type: "Product", Name: "Ravel", From: "e1", To: "e2", Into: "e3"},
				Note: "packages are the product", At: at,
			},
			Because: "declared in the rule file on 2026-08-01",
		},
		Provenance: alchemy.Provenance{
			Source: "c.md", Chunk: 9, Producer: alchemy.ProducerHuman,
			By: "liliang", At: "2026-08-30T09:00:00Z", ReviewedBy: "theodore",
			RuleSet: "rs-1", RuledBy: "r-1", Ontology: "o-1", Chunking: "heading",
		},
	}

	got := wire.ItemFromProto(wire.ItemToProto("job-42", want))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the review item did not survive the round trip\n got %#v\nwant %#v", got, want)
	}
	// The pointer has to stay a pointer and stay distinct: review.Open asks
	// exactly this field whether a person still has to answer the item.
	if got.SuppressedBy == want.SuppressedBy {
		t.Error("SuppressedBy came back as the same pointer; the message was not decoded at all")
	}

	// An item nobody has ruled on must not grow a rule on the way back.
	open := review.Item{ID: "item-2", Kind: review.KindConflict, Shape: "conflict/x"}
	if back := wire.ItemFromProto(wire.ItemToProto("job-42", open)); back.SuppressedBy != nil {
		t.Errorf("an unanswered item came back suppressed by %+v; review.Open would drop it "+
			"from the queue and the job would be held on a question nobody is shown",
			back.SuppressedBy)
	}
}
