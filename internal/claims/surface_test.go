package claims_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/gateway"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// DESIGN.md §6: the REST gateway is "a translation, never a second source of
// truth about what the service does".
//
// A translation can be wrong in a way neither document shows: an RPC added to
// the proto with no google.api.http option and no entry in gateway.Refusals
// has no HTTP presence at all, and nothing says so. It is not a 501 and it is
// not a route — it is a 404 that looks exactly like a typo, and the person who
// finds it is a buyer who read the proto.
//
// That is not hypothetical. Review has been unannotated since §6 was written,
// deliberately, and it is visible only because somebody wrote a Refusal for it
// by hand. Three RPCs were added in one afternoon; two got routes and the
// third would have been silent.
//
// So every method on the service must be one of the two: translated, or
// refused in writing.
func TestEveryRPCIsEitherRoutedOrRefusedInWriting(t *testing.T) {
	routed := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(root(t), "docs", "alchemy", "v1", "alchemy.swagger.json"))
	if err != nil {
		t.Fatalf("reading the generated OpenAPI document: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parsing the OpenAPI document: %v", err)
	}
	for _, methods := range doc.Paths {
		for _, op := range methods {
			// operationId is "Alchemy_CreateJob"; the RPC is the half after
			// the service name.
			if _, rpc, ok := strings.Cut(op.OperationID, "_"); ok {
				routed[rpc] = true
			}
		}
	}

	refused := map[string]bool{}
	for _, r := range gateway.Refusals() {
		refused[r.RPC] = true
		if r.Because == "" {
			t.Errorf("the refusal of %s says nothing; a 501 with no sentence in it tells a "+
				"buyer the product has no such feature, which is the more expensive wrong answer", r.RPC)
		}
	}

	var silent []string
	for _, m := range alchemyv1.Alchemy_ServiceDesc.Methods {
		if !routed[m.MethodName] && !refused[m.MethodName] {
			silent = append(silent, m.MethodName)
		}
	}
	for _, sd := range alchemyv1.Alchemy_ServiceDesc.Streams {
		if !routed[sd.StreamName] && !refused[sd.StreamName] {
			silent = append(silent, sd.StreamName)
		}
	}
	if len(silent) > 0 {
		sort.Strings(silent)
		t.Fatalf("%v have no HTTP route and no written refusal: over HTTP they are a 404 that "+
			"looks like a typo. Give each one a google.api.http option, or a gateway.Refusal "+
			"saying why it cannot have one.", silent)
	}
	t.Logf("%d RPCs routed, %d refused in writing", len(routed), len(refused))
}
