package rdf

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// fullProvenance sets every field of alchemy.Provenance to a distinct value.
//
// Distinct is the point. Two fields written under each other's predicates would
// both come back non-empty, and only their contents would say so — which is the
// failure a length check cannot see and the reason pgvector's round-trip test
// is written the same way.
func fullProvenance() alchemy.Provenance {
	return alchemy.Provenance{
		Source:     "halcyon-profile.pdf",
		Chunk:      20,
		Producer:   alchemy.ProducerHuman,
		Model:      "gemini-3.6-flash-high",
		Ontology:   "sds@3",
		Chunking:   "heading",
		Confidence: 0.82,
		ReviewedBy: "ana@example.com",
		RuleSet:    "rs-9f21",
		RuledBy:    "authored/violation/type=Flag",
		By:         "liliang",
		At:         "2026-08-30T00:00:00Z",
	}
}

// TestAnEdgesWholeProvenanceSurvivesTheRoundTripThroughRDFStar is the one test
// this connector cannot be taken on trust without.
//
// The whole design rests on one claim: that an RDF triple cannot carry a
// property, that alchemy puts eleven of them on every edge, and that RDF-star
// carries all of them on the assertion itself where reification, singleton
// properties and a graph-per-source each lose or cost something. Everything
// else in this package follows from that claim. So it is written to a live
// store with every field distinct, read back through the interface an agent
// actually uses, and compared field for field.
//
// Read back through recall.Claims for the part the interface exposes — the
// producer, the source, the chunk and stated-or-inferred — and through
// Assertions for the eight fields recall.Claim deliberately does not carry. A
// test that only used recall.Claims would prove four fields survived and say
// nothing about the other eight, which is most of what was being claimed.
func TestAnEdgesWholeProvenanceSurvivesTheRoundTripThroughRDFStar(t *testing.T) {
	ctx := context.Background()
	l := liveLoader(t, Options{})
	prov := fullProvenance()

	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "p1", Type: "Person", Name: "Mira", Provenance: prov},
			{ID: "d1", Type: "Product", Name: "Ledger", Provenance: prov},
		},
		Relations: []alchemy.Relation{
			{From: "p1", To: "d1", Type: "DEVELOPS", Key: "fk_mira_ledger", Provenance: prov},
		},
	}
	res.Counts = res.Derivable()
	if _, err := l.Load(ctx, res); err != nil {
		t.Fatalf("Load: %v", err)
	}

	claims, err := l.Claims(ctx, l.opts.RunID, "p1")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("Claims returned %d rows, want the one edge: %+v", len(claims), claims)
	}
	c := claims[0]
	if c.From != "Mira" || c.Type != "DEVELOPS" || c.To != "Ledger" {
		t.Errorf("claim = %s, want Mira -[DEVELOPS]-> Ledger", c)
	}
	if c.Source != prov.Source || c.Chunk != prov.Chunk || c.Producer != prov.Producer {
		t.Errorf("citation and producer = %s#%d by %s, want %s#%d by %s",
			c.Source, c.Chunk, c.Producer, prov.Source, prov.Chunk, prov.Producer)
	}
	// alchemy.ProducerHuman is deterministic — a person signing their name to a
	// sentence is the clearest case of stating there is — so a claim that came
	// back inferred would mean the producer did not survive.
	if !c.Stated {
		t.Errorf("Stated = false for %s, which alchemy.Producer.Deterministic reports true for", prov.Producer)
	}

	full, err := l.Assertions(ctx, l.opts.RunID, "p1")
	if err != nil {
		t.Fatalf("Assertions: %v", err)
	}
	if len(full) != 1 {
		t.Fatalf("Assertions returned %d rows, want one", len(full))
	}
	if got := full[0].Provenance; got != prov {
		t.Errorf("the provenance did not survive RDF-star field for field\n got %+v\nwant %+v", got, prov)
		// Named one by one, because "not equal" on a twelve-field struct is a
		// message that sends a reader back to the debugger.
		for _, f := range []struct {
			name      string
			got, want any
		}{
			{"Source", got.Source, prov.Source}, {"Chunk", got.Chunk, prov.Chunk},
			{"Producer", got.Producer, prov.Producer}, {"Model", got.Model, prov.Model},
			{"Ontology", got.Ontology, prov.Ontology}, {"Chunking", got.Chunking, prov.Chunking},
			{"Confidence", got.Confidence, prov.Confidence}, {"ReviewedBy", got.ReviewedBy, prov.ReviewedBy},
			{"RuleSet", got.RuleSet, prov.RuleSet}, {"RuledBy", got.RuledBy, prov.RuledBy},
			{"By", got.By, prov.By}, {"At", got.At, prov.At},
		} {
			if f.got != f.want {
				t.Errorf("  %s = %#v, want %#v", f.name, f.got, f.want)
			}
		}
	}
}

// An entity's provenance is annotated onto its rdf:type statement rather than
// written as properties of the entity, so that §5b's "an entity and a relation
// can both name their producer" is one query shape. This checks the other half
// of that: it has to come back too, and it has to be the entity's own.
func TestAnEntitysProvenanceIsCarriedOnTheStatementThatTypesIt(t *testing.T) {
	ctx := context.Background()
	l := liveLoader(t, Options{})
	prov := fullProvenance()
	res := alchemy.Result{
		Entities: []alchemy.Entity{{ID: "p1", Type: "Person", Name: "Mira", Provenance: prov}},
	}
	res.Counts = res.Derivable()
	if _, err := l.Load(ctx, res); err != nil {
		t.Fatalf("Load: %v", err)
	}

	rows := l.ask(t, "SELECT"+provVars()+" WHERE { GRAPH <"+l.loadIRI(l.opts.RunID)+"> {\n"+
		provPattern("<< <"+l.entityIRI(l.opts.RunID, "p1")+"> <"+rdfType+"> <"+l.classIRI("Person")+"> >>")+"} }")
	if len(rows) != 1 {
		t.Fatalf("the entity's type statement carries %d provenance rows, want one", len(rows))
	}
	if got := provDecode(rows[0]); got != prov {
		t.Errorf("an entity's provenance did not survive\n got %+v\nwant %+v", got, prov)
	}
}

// TestTwoRecordsAssertingOneEdgeAreReportedRatherThanMerged is the cost of
// RDF-star, made visible.
//
// A property graph keeps two parallel edges with two provenances — that is
// exactly what neo4j's relationKey buys. An RDF graph is a set of triples, so
// the second assertion IS the first triple, and annotating it with both
// provenances would leave a walk returning their cross product as claims the
// corpus never made.
//
// So the connector keeps the first and says so. What this asserts is the
// saying-so: the edge is in the store once, the report names the assertion that
// could not be kept, and sink.Report.Lost carries it in the shape the envelope
// specifies. A store that silently kept one of two would be the quiet loss this
// whole design refuses.
func TestTwoRecordsAssertingOneEdgeAreReportedRatherThanMerged(t *testing.T) {
	ctx := context.Background()
	l := liveLoader(t, Options{})

	first := fullProvenance()
	second := fullProvenance()
	second.Chunk = 21
	second.Model = "a-different-model"

	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "p1", Type: "Person", Name: "Mira", Provenance: first},
			{ID: "d1", Type: "Product", Name: "Ledger", Provenance: first},
		},
		Relations: []alchemy.Relation{
			{From: "p1", To: "d1", Type: "DEVELOPS", Provenance: first},
			{From: "p1", To: "d1", Type: "DEVELOPS", Provenance: second},
		},
	}
	res.Counts = res.Derivable()
	rep, err := l.Load(ctx, res)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rep.MergedRelations) != 1 {
		t.Fatalf("Report.MergedRelations = %v, want the one assertion RDF cannot keep apart", rep.MergedRelations)
	}
	if rep.Relations != 1 {
		t.Errorf("Report.Relations = %d, want 1: the report must count what was written", rep.Relations)
	}
	claims, err := l.Claims(ctx, l.opts.RunID, "p1")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	// One claim and not two, and not four. Two provenances on one quoted triple
	// would have produced the cross product of the two chunks and the two
	// models, which is the wrong answer this refusal exists to prevent.
	if len(claims) != 1 {
		t.Fatalf("Claims returned %d rows, want exactly one: %+v", len(claims), claims)
	}
	if claims[0].Chunk != first.Chunk {
		t.Errorf("the surviving claim cites chunk %d, want the first assertion's %d", claims[0].Chunk, first.Chunk)
	}
}
