package rdf

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// values reads one column out of a result, for the assertions below.
func values(rows []map[string]binding, v string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r[v].Value)
	}
	return out
}

// Aliases are skos:altLabel, several triples under one predicate, because they
// are what SKOS is for and because an alias set genuinely is a set. Contrast
// the JSON array below, where the order is part of the value.
func TestAliasesBecomeAltLabelsAndTheNameStaysTheLabel(t *testing.T) {
	l, load := loaded(t, Options{})
	e := l.entityIRI(load, "e1")
	alts := values(l.ask(t, "SELECT ?a WHERE { GRAPH <"+l.loadIRI(load)+"> { <"+e+"> <"+skosAltLabel+"> ?a } }"), "a")
	if len(alts) != 2 {
		t.Errorf("skos:altLabel = %v, want the two aliases the source stated", alts)
	}
	labels := values(l.ask(t, "SELECT ?a WHERE { GRAPH <"+l.loadIRI(load)+"> { <"+e+"> <"+rdfsLabel+"> ?a } }"), "a")
	if len(labels) != 1 || labels[0] != "SuperAI" {
		t.Errorf("rdfs:label = %v, want exactly the name — an alias written as a second label would "+
			"make an anchor search report one entity under two names", labels)
	}
}

// A nested attribute has nowhere to go in RDF, so it is written as its JSON
// text and the key is named on the record. A conversion nobody can see from the
// data is the quiet rewrite this connector exists not to do.
func TestANestedAttributeIsWrittenAsJSONAndSaysThatItWas(t *testing.T) {
	l, load := loaded(t, Options{})
	e := l.entityIRI(load, "e3")
	got := values(l.ask(t, "SELECT ?v WHERE { GRAPH <"+l.loadIRI(load)+"> { <"+e+"> <"+l.attrIRI("address")+"> ?v } }"), "v")
	if len(got) != 1 || got[0] != `{"city":"Wien"}` {
		t.Errorf("the nested attribute = %v, want its JSON text", got)
	}
	marks := values(l.ask(t, "SELECT ?k WHERE { GRAPH <"+l.loadIRI(load)+"> { <"+e+"> <"+pJSONAttribute+"> ?k } }"), "k")
	if len(marks) != 1 || marks[0] != "address" {
		t.Errorf("al:jsonAttribute = %v, want the key whose value had to be re-encoded", marks)
	}
	// A scalar attribute is not marked, so the marker means something.
	e1 := l.entityIRI(load, "e1")
	if marks := l.ask(t, "SELECT ?k WHERE { GRAPH <"+l.loadIRI(load)+"> { <"+e1+"> <"+pJSONAttribute+"> ?k } }"); len(marks) != 0 {
		t.Errorf("an entity whose attributes are all scalars is marked as carrying JSON: %v", marks)
	}
}

// §5's obligation: "every returned graph is accompanied by the numbers needed
// to distrust it". They are on the load marker rather than in a file on
// somebody's laptop, and every one is written including the zeros — a missing
// predicate reads as "this loader did not know about that number", which is a
// different claim from "that number was nought".
func TestTheQualityNumbersAreOnTheLoadMarker(t *testing.T) {
	l, load := loaded(t, Options{})
	g := l.loadIRI(load)
	for _, tc := range []struct {
		pred string
		want string
	}{
		{countPreds.Entities, "3"},
		{countPreds.Relations, "2"},
		{countPreds.Violations, "1"},
		{countPreds.Duplicates, "1"},
		{countPreds.Guesses, "1"},
		// Zero, and present. A reader who cannot tell "no vectors" from "this
		// loader does not report vectors" cannot use the number at all.
		{countPreds.Vectors, "0"},
	} {
		got := values(l.ask(t, "SELECT ?n WHERE { GRAPH <"+g+"> { <"+g+"> <"+tc.pred+"> ?n } }"), "n")
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("<%s> = %v, want [%s]", tc.pred, got, tc.want)
		}
	}
}

// A retirement is filed and never acted on. A triple store is the store that
// COULD act — DELETE WHERE over the retired subject is one statement — which is
// exactly why it must not: a producer able to delete another producer's fact by
// naming it would be an unreviewed writer with write access.
func TestARetirementIsFiledAndTheRecordItNamesIsUntouched(t *testing.T) {
	l, load := loaded(t, Options{})
	g := l.loadIRI(load)
	rows := l.ask(t, "SELECT ?r ?by WHERE { GRAPH <"+g+"> { ?s <"+rdfType+"> <"+clSupersession+"> ; <"+
		pRetires+"> ?r ; <"+pReplacedBy+"> ?by } }")
	if len(rows) != 1 || rows[0]["r"].Value != "e-from-last-month" {
		t.Fatalf("the retirement was not filed: %v", rows)
	}
	// It names a record this result does not contain, which is the ordinary
	// case — the thing being retired is in the store from a run that finished
	// last month — so it is a literal and not an IRI this load minted.
	if rows[0]["by"].Value != l.entityIRI(load, "e3") {
		t.Errorf("al:replacedBy = %q, want the entity that replaces it", rows[0]["by"].Value)
	}
	// And Ada is still exactly as she was.
	if got := values(l.ask(t, "SELECT ?n WHERE { GRAPH <"+g+"> { <"+l.entityIRI(load, "e3")+"> <"+
		rdfsLabel+"> ?n } }"), "n"); len(got) != 1 || got[0] != "Ada" {
		t.Errorf("the record named by a retirement changed: %v", got)
	}
}

// Replace is DROP GRAPH, which is the one place a triple store is plainly
// better at something than a property graph: neo4j deletes a run in bounded
// bites because a single DETACH DELETE is a transaction the server has to hold
// in memory. What matters for correctness is that nothing of the old load
// survives into the new one.
func TestReplacingALoadLeavesNothingOfTheOldOneBehind(t *testing.T) {
	ctx := context.Background()
	l := liveLoader(t, Options{})

	// A fresh name per run rather than a fixed one. The load name used to be
	// "ld-replace" and the test passed for as long as every run got a
	// repository of its own — which is a property of how GraphDB is driven
	// here, not of anything this test is about. Against a store that serves one
	// dataset the second run met its own first load and was refused with
	// "already written from a different result", which is the connector doing
	// exactly what it promises. The isolation the test needs is a name nobody
	// else has, and that is what every load has in production.
	load := "ld-replace-" + randomName(t)
	first := fixture()
	if _, err := sink.Load(ctx, l, first, sink.Options{Load: load}); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	second := fixture()
	second.Entities = second.Entities[:1]
	second.Entities[0].Name = "SuperAI, renamed"
	second.Relations = nil
	second.Duplicates = nil
	second.Counts = second.Derivable()
	if _, err := sink.Load(ctx, l, second, sink.Options{Load: load, Replace: true}); err != nil {
		t.Fatalf("Load with Replace: %v", err)
	}

	found, err := l.Find(ctx, load, "cortexdb", 10)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(found.Nodes) != 0 {
		t.Errorf("an entity from the replaced load is still readable: %+v", found.Nodes)
	}
	qs, err := l.Unanswered(ctx, load, "")
	if err != nil {
		t.Fatalf("Unanswered: %v", err)
	}
	if len(qs) != 0 {
		t.Errorf("a duplicate finding from the replaced load survived: %+v", qs)
	}
}

// What this store cannot hold, said out loud. A load that returned success
// without naming the embeddings it dropped would be lying by omission, which is
// what sink.Loss exists to prevent.
func TestALoadWithVectorsReportsThatTheyWereNotKept(t *testing.T) {
	ctx := context.Background()
	l := liveLoader(t, Options{})
	res := fixture()
	res.Vectors = []alchemy.Vector{{Chunk: 14, Model: "embed-3", Values: []float32{0.1, 0.2, 0.3, 0.4}}}
	res.Counts = res.Derivable()

	// Fresh per run, for the reason the replace test above gives: a second run
	// against a one-dataset store meets its own first load, and a replay
	// reports nothing lost because it wrote nothing.
	rep, err := sink.Load(ctx, l, res, sink.Options{Load: "ld-vectors-" + randomName(t)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var found bool
	for _, loss := range rep.Lost {
		if loss.What == "vectors" {
			found = true
			if loss.Count != 1 {
				t.Errorf("Loss.Count = %d, want the one vector the result carried", loss.Count)
			}
		}
	}
	if !found {
		t.Fatalf("Report.Lost = %+v, want an entry for the embeddings a triple store cannot hold", rep.Lost)
	}
	// And the round trips are counted on this path too. An operator holding a
	// load that died halfway needs to know how much work it had done, and the
	// number must not depend on which entry point the caller used.
	if rep.Batches == 0 {
		t.Error("Report.Batches = 0 after a load that made several requests")
	}
}
