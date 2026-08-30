package neo4j

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// A connector that builds Cypher by concatenating a model's output is a remote
// code execution wearing a graph. An Entity.Type and a Relation.Type are the
// only strings this package ever concatenates, and both come out of a model,
// so the proof is run against a live server: the payload is loaded, the
// statement it tried to break out of still does exactly one thing, and a
// canary node it asked to delete is still there afterwards.
func TestTypesCannotInjectCypher(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-X"})
	base := mustQuote(t, l.opts.BaseLabel)

	canary := "Canary_" + l.opts.BaseLabel
	l.mustQuery(t, "CREATE (n:"+base+":"+mustQuote(t, canary)+" {`_run`:'canary'})", nil)

	badType := "Sys`) DETACH DELETE n WITH 1 AS x MATCH (m) DETACH DELETE m //"
	badRel := "USES`]->() DETACH DELETE a,b //"
	prov := alchemy.Provenance{Source: "evil.pdf", Chunk: 1, Producer: alchemy.ProducerLLMExtract}
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "e1", Type: badType, Name: "a", Provenance: prov},
			{ID: "e2", Type: badType, Name: "b", Provenance: prov},
		},
		Relations: []alchemy.Relation{{From: "e1", To: "e2", Type: badRel, Provenance: prov}},
	}
	if _, err := l.Load(context.Background(), res); err != nil {
		t.Fatalf("Load of a hostile type: %v", err)
	}

	// The canary is the actual proof: the payload asked for every node in the
	// database, twice.
	recs := l.mustQuery(t, "MATCH (n:"+mustQuote(t, canary)+") RETURN count(n) AS n", nil)
	if recs[0]["n"] != int64(1) {
		t.Fatalf("the canary node is gone: the payload executed")
	}

	// And the label is the type verbatim, not a stripped or mangled version of
	// it. A connector that silently rewrote the ontology type would be the one
	// liar the verification chain cannot catch, because verification already
	// happened upstream.
	recs = l.mustQuery(t, "MATCH (n:"+base+" {`_id`:'e1', `_run`:'run-X'}) RETURN labels(n) AS labels", nil)
	labels := recs[0]["labels"].([]any)
	found := false
	for _, x := range labels {
		if x == badType {
			found = true
		}
	}
	if !found {
		t.Fatalf("labels = %#v, want the ontology type verbatim", labels)
	}

	recs = l.mustQuery(t, "MATCH (a:"+base+" {`_id`:'e1',`_run`:'run-X'})-[r]->(b:"+base+" {`_id`:'e2',`_run`:'run-X'}) RETURN type(r) AS t", nil)
	if len(recs) != 1 || recs[0]["t"] != badRel {
		t.Fatalf("relationship = %#v, want exactly one of the hostile type", recs)
	}

	// Cleanup of the canary, which carries a different _run and so is not
	// covered by the run delete.
	t.Cleanup(func() {
		l.mustQuery(t, "MATCH (n:"+mustQuote(t, canary)+") DETACH DELETE n", nil)
	})
}

// The values are never concatenated either — they ride as parameters — but a
// hostile entity ID is cheap to prove and is the other half of the surface.
func TestValuesCannotInjectCypher(t *testing.T) {
	l := liveLoader(t, Options{RunID: "' OR 1=1 // "})
	evil := "e1'}) DETACH DELETE n //"
	res := alchemy.Result{Entities: []alchemy.Entity{
		{ID: evil, Type: "System", Name: "x'}) DETACH DELETE n //",
			Attributes: map[string]any{"note": "`) DETACH DELETE n //"},
			Provenance: alchemy.Provenance{Source: "evil.pdf", Chunk: 0, Producer: alchemy.ProducerLLMExtract}},
	}}
	if _, err := l.Load(context.Background(), res); err != nil {
		t.Fatalf("Load: %v", err)
	}
	recs := l.mustQuery(t, "MATCH (n:"+mustQuote(t, l.opts.BaseLabel)+") WHERE n.`_id` = $id RETURN n.name AS name", map[string]any{"id": evil})
	if len(recs) != 1 {
		t.Fatalf("%d nodes with the hostile ID, want 1", len(recs))
	}
}
