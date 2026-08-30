package wire_test

import (
	"encoding/json"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/wire"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// gatewayJSON renders a result the way pkg/gateway does: protojson over the
// generated message, with the same options the REST handler is built with.
// The point is that this is not a stand-in — it is the same two libraries in
// the same order, so what this test asserts is what a buyer's curl returns.
func gatewayJSON(t *testing.T, res alchemy.Result) []byte {
	t.Helper()
	b, err := protojson.MarshalOptions{
		UseProtoNames:     true,
		EmitDefaultValues: true,
	}.Marshal(wire.ResultToProto(res))
	if err != nil {
		t.Fatalf("marshalling the gateway's rendering: %v", err)
	}
	return b
}

// TestUnmarshallingTheGatewaysJSONIntoAResultReportsADeterministicGraphAsEntirelyInferred
// is the trap this package exists to make un-steppable.
//
// DESIGN.md §4 says the JSON is the contract, and the obvious reading of that
// is that the bytes the REST gateway returns are an alchemy.Result and can be
// unmarshalled into one. They are not. protojson renders an enum by its
// protobuf name, so alchemy.ProducerGraphImport — whose own JSON tag is
// "graph-import" — leaves the gateway as "PRODUCER_GRAPH_IMPORT", and
// encoding/json will happily assign that string to a Producer because Producer
// is a string.
//
// Nothing errors. The graph parses, every record has a producer, and every
// producer is one Producer.Deterministic() has never heard of — so a graph
// that was entirely read out of a schema reports itself as entirely inferred.
// That is the §5b guarantee inverted: a reader deciding what is worth a
// person's time is told the opposite of the truth, by a well-formed document,
// with no error anywhere to look for.
//
// The cost is in the name because the name is what somebody greps for at
// two in the morning. Use wire.ResultFromProto; do not unmarshal the HTTP body
// into an alchemy.Result.
func TestUnmarshallingTheGatewaysJSONIntoAResultReportsADeterministicGraphAsEntirelyInferred(t *testing.T) {
	source := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "Table", Name: "users", Provenance: alchemy.Provenance{
				Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}},
			{ID: "e2", Type: "Org", Name: "Northgate", Provenance: alchemy.Provenance{
				Source: "graph.json", Chunk: -1, Producer: alchemy.ProducerGraphImport}},
		},
	}
	for _, e := range source.Entities {
		if !e.Provenance.Producer.Deterministic() {
			t.Fatalf("fixture is wrong: %q is not deterministic to begin with", e.Provenance.Producer)
		}
	}

	body := gatewayJSON(t, source)

	var naive alchemy.Result
	if err := json.Unmarshal(body, &naive); err != nil {
		t.Fatalf("the gateway's body no longer parses as an alchemy.Result at all: %v\n"+
			"that would be a better failure than the one this test guards, but it is a "+
			"different one — reread the test before deleting it", err)
	}
	if len(naive.Entities) != len(source.Entities) {
		t.Fatalf("got %d entities, want %d", len(naive.Entities), len(source.Entities))
	}

	for i, e := range naive.Entities {
		want := source.Entities[i].Provenance.Producer
		if e.Provenance.Producer == want {
			t.Fatalf("entity %s came back as %q, which is alchemy's own spelling — "+
				"the gateway now emits alchemy's JSON dialect and this test is obsolete. "+
				"Check pkg/gateway's marshaller before removing it.", e.ID, want)
		}
		if e.Provenance.Producer.Deterministic() {
			t.Errorf("entity %s: %q was expected to be a producer nothing recognises", e.ID, e.Provenance.Producer)
		}
	}

	// And the same bytes, read the way this package says to read them.
	var msg = mustUnmarshalProto(t, body)
	fixed := wire.ResultFromProto(msg)
	for i, e := range fixed.Entities {
		want := source.Entities[i].Provenance.Producer
		if e.Provenance.Producer != want {
			t.Fatalf("wire.ResultFromProto: entity %s came back with producer %q, want %q",
				e.ID, e.Provenance.Producer, want)
		}
		if !e.Provenance.Producer.Deterministic() {
			t.Errorf("wire.ResultFromProto: entity %s lost its deterministic standing", e.ID)
		}
	}
}

// mustUnmarshalProto reads the gateway's body back as the message it is. It is
// two lines and it is the whole of the correct answer: the bytes are protobuf
// JSON, so protojson is what reads them, and wire.ResultFromProto is what turns
// the message into the Go types every connector in connectors/ already takes.
func mustUnmarshalProto(t *testing.T, body []byte) *alchemyv1.Result {
	t.Helper()
	var msg alchemyv1.Result
	if err := protojson.Unmarshal(body, &msg); err != nil {
		t.Fatalf("protojson.Unmarshal: %v", err)
	}
	return &msg
}
