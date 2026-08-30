package cortexdb

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// nodeConnections is the shape Relation.Key exists for, in the words of its own
// doc comment: "a table that models a relationship between two rows of one
// table references that table twice, once per end, and Ravel's
// NODE_CONNECTIONS does exactly that. Both foreign keys are correct, they say
// different things about themselves — different columns, different constraint
// names."
func nodeConnections() alchemy.Result {
	ddl := alchemy.Provenance{Source: "ravel.sql", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "ravel@1"}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "t_nodes", Type: "Table", Name: "NODES", Provenance: ddl},
			{ID: "t_conns", Type: "Table", Name: "NODE_CONNECTIONS", Provenance: ddl},
		},
		Relations: []alchemy.Relation{
			{From: "t_conns", To: "t_nodes", Type: "REFERENCES", Key: "FK_NC_SRC",
				Attributes: map[string]any{"column": "NODE_NAME_SRC"}, Provenance: ddl},
			{From: "t_conns", To: "t_nodes", Type: "REFERENCES", Key: "FK_NC_DST",
				Attributes: map[string]any{"column": "NODE_NAME_DST"}, Provenance: ddl},
		},
		Counts: alchemy.Counts{Entities: 2, Relations: 2, Deterministic: 2},
	}
}

// The disagreement, stated as a test.
//
// CortexDB decided an edge is (from, to, type, document) — see
// graphrag_relation_identity_test.go, written after a store held 102,855 edges
// of which 61,197 were distinct. Alchemy added Relation.Key for the same
// question and answered it differently: identity is (from, to, type) plus the
// producer's own name for the edge, precisely because {from, to, type} could not
// tell the two foreign keys apart.
//
// The two answers agree everywhere except here, and here they cannot both be
// right: CortexDB has nowhere to put the key, so one of the two edges would win
// and the other would vanish with nothing said. Refused by default, for the same
// reason the Neo4j connector refuses a colliding attribute — "one of the two
// would have to win silently".
func TestParallelKeyedEdgesAreRefusedRatherThanFused(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-K"})
	_, err := l.Load(context.Background(), nodeConnections())
	if !errors.Is(err, ErrParallelEdges) {
		t.Fatalf("Load of two keyed parallel edges: err = %v, want ErrParallelEdges", err)
	}
	if got := countNodes(t, l); got != 0 {
		t.Fatalf("%d nodes written by a refused load, want none", got)
	}
}

// A caller who knows the cost can accept it. What they must not get is silence:
// the report names the fusion, and both keys and both provenances stay on the
// one edge CortexDB allows, so a reader can still see that two claims went in.
func TestFuseParallelEdgesKeepsBothClaimsOnTheOneEdge(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-K2", FuseParallelEdges: true})
	rep, err := l.Load(context.Background(), nodeConnections())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rep.FusedRelations) != 1 {
		t.Fatalf("FusedRelations = %v, want the one group that collapsed", rep.FusedRelations)
	}
	if got := countEdges(t, l); got != 1 {
		t.Fatalf("%d edges, want the 1 CortexDB's identity allows", got)
	}
	props := edgeProps(t, l, "REFERENCES")
	if props["_key"] != "FK_NC_DST,FK_NC_SRC" {
		t.Fatalf("_key = %#v, want both constraint names", props["_key"])
	}
	if props["_assertions"] != "2" {
		t.Fatalf("_assertions = %#v, want 2", props["_assertions"])
	}
	var provs []alchemy.Provenance
	if err := json.Unmarshal([]byte(props["_provenance"].(string)), &provs); err != nil {
		t.Fatalf("decode _provenance: %v", err)
	}
	if len(provs) != 2 {
		t.Fatalf("%d provenances on the fused edge, want one per claim", len(provs))
	}
	// The attributes are what the two foreign keys actually disagreed about, so
	// losing them would leave the fused edge unable to say what it fused.
	var attrs []map[string]any
	if err := json.Unmarshal([]byte(props["_attributes"].(string)), &attrs); err != nil {
		t.Fatalf("decode _attributes: %v", err)
	}
	if len(attrs) != 2 || attrs[0]["column"] == attrs[1]["column"] {
		t.Fatalf("_attributes = %v, want both columns", attrs)
	}
}

// Where the two answers agree, this connector takes CortexDB's.
//
// Two chunks asserting the same edge from the same file are one edge with both
// chunk ids — CortexDB's decision, tested in its own suite, and a deliberate
// difference from the Neo4j connector, which keeps them as two edges because
// Neo4j has no union to fold them into. Nothing is lost either way: there the
// two edges each name their chunk, here the one edge names both.
func TestTwoChunksAssertingOneEdgeBecomeOneEdgeNamingBothChunks(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-K3"})
	res := fixture()
	twin := res.Relations[0]
	twin.Provenance.Chunk = 1
	res.Relations = append(res.Relations, twin)
	if _, err := l.Load(context.Background(), res); err != nil {
		t.Fatalf("Load: %v", err)
	}
	fp, err := l.db().FactProvenanceFor(context.Background(), edgeID(t, l, "USES"), false)
	if err != nil {
		t.Fatalf("FactProvenanceFor: %v", err)
	}
	if len(fp.ChunkIDs) != 2 {
		t.Fatalf("chunk_ids = %v, want one per chunk that asserted the edge", fp.ChunkIDs)
	}
}

// Two files asserting the same edge stay two edges, because the document is
// part of CortexDB's identity. That is the axis alchemy does not have and
// CortexDB does — the mirror image of Relation.Key — and mapping
// Provenance.Source onto the document is what makes it work rather than a
// coincidence.
func TestTwoSourcesAssertingOneEdgeStayTwoEdges(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-K4"})
	res := fixture()
	twin := res.Relations[0]
	twin.Provenance.Source = "design.md"
	twin.Provenance.Chunk = -1
	res.Relations = append(res.Relations, twin)
	if _, err := l.Load(context.Background(), res); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := countRows(t, l, "SELECT COUNT(*) FROM graph_edges WHERE edge_type = 'USES'"); got != 2 {
		t.Fatalf("%d USES edges, want one per source that asserted it", got)
	}
}

// The crux for nodes. CortexDB's own entity identity is the folded *name*,
// which is the right answer to CortexDB's question and the wrong one to
// alchemy's: §5 defers entity resolution, and two runs that both mention
// "SuperAI" are two claims until somebody decides otherwise. Passing an
// "entity:"-prefixed id is how CortexDB lets a caller say identity is already
// settled, and this asserts the store took it.
func TestTwoRunsAreTwoGraphs(t *testing.T) {
	a := openLocal(t, Options{RunID: "run-a"})
	// The second run shares the file, so both Loaders point at one database.
	b := New(a.db(), Options{RunID: "run-b"})
	ctx := context.Background()

	prov := alchemy.Provenance{Source: "a.pdf", Chunk: -1, Producer: alchemy.ProducerLLMExtract}
	first := alchemy.Result{Entities: []alchemy.Entity{{ID: "e1", Type: "System", Name: "SuperAI", Provenance: prov}}}
	second := alchemy.Result{Entities: []alchemy.Entity{{ID: "e1", Type: "Person", Name: "SuperAI", Provenance: prov}}}
	if _, err := a.Load(ctx, first); err != nil {
		t.Fatalf("Load a: %v", err)
	}
	if _, err := b.Load(ctx, second); err != nil {
		t.Fatalf("Load b: %v", err)
	}
	if got := countRows(t, a, "SELECT COUNT(*) FROM graph_nodes WHERE node_type IN ('System','Person')"); got != 2 {
		t.Fatalf("%d nodes for two runs' e1, want 2: a name that means nothing across runs was used to join them", got)
	}
}
