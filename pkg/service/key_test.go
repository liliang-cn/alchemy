package service

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// An edge's key has to reach the wire. It is what makes two parallel edges two
// edges (see alchemy.Relation.Key), so a caller reading the result over gRPC or
// through the JSON gateway without it gets a graph in which one end of every
// connection is indistinguishable from the other — and a reviewer answering a
// decision built from such a Ref would act on both.
func TestTheEdgeKeyReachesTheWire(t *testing.T) {
	got := relationToProto(alchemy.Relation{
		From: "table:node_connections", To: "table:nodes", Type: "REFERENCES",
		Key: "FK_NC_NODES_SRC", Provenance: alchemy.Provenance{Source: "s.sql", Chunk: -1, Producer: alchemy.ProducerDDL},
	})
	if got.GetKey() != "FK_NC_NODES_SRC" {
		t.Fatalf("key = %q, want the constraint the schema named", got.GetKey())
	}
}

// A Ref goes out to a reviewer and comes back in a decision. Losing the key on
// either leg would put the decision back on an edge nobody chose.
func TestARefKeepsItsKeyBothWays(t *testing.T) {
	want := review.Ref{
		Kind: review.RefRelation, From: "table:node_connections", To: "table:nodes",
		Type: "REFERENCES", Key: "FK_NC_NODES_DST",
		Provenance: alchemy.Provenance{Source: "s.sql", Chunk: -1, Producer: alchemy.ProducerDDL},
	}
	if got := refFromProto(refToProto(want)); got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}
