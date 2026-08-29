package gateway_test

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/gateway"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// The route table is read out of the generated OpenAPI document rather than
// written down here, for the same reason pkg/service's auth table walks the
// generated service descriptor: a hand-written list is correct until the day a
// route is added, and the route added on that day is the one nobody remembers
// to authenticate.
//
// docs/alchemy/v1/alchemy.swagger.json is produced by `make generate` from
// alchemy.proto. Reading it here means the two cannot disagree: an annotation
// added to the proto is in this table the moment the document is regenerated,
// and a stale document fails the coverage test below instead of passing
// quietly.
const openAPIDoc = "../../docs/alchemy/v1/alchemy.swagger.json"

// route is one HTTP entry point, with everything a test needs to call it.
type route struct {
	method string
	// template is the path as the document declares it, {job_id} and all.
	template string
	// rpc is the RPC it translates, for the coverage test.
	rpc string
	// body is what a request to it must carry, or empty for the verbs that
	// carry none. It is minimal on purpose: authentication runs before a body
	// is ever read, so a route that let an anonymous caller through would fail
	// on the body instead, which is a different error and still a failure.
	body string
}

// path substitutes a job ID into the template.
func (r route) path(jobID string) string {
	return strings.ReplaceAll(r.template, "{job_id}", jobID)
}

// routes is every path the gateway answers: the generated ones, plus the
// refusals the gateway declares for the RPCs that have no honest translation.
func routes(t *testing.T) []route {
	t.Helper()
	out := generatedRoutes(t)
	for _, ref := range gateway.Refusals() {
		out = append(out, route{method: ref.Method, template: ref.Path, rpc: ref.RPC, body: "{}"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rpc < out[j].rpc })
	return out
}

func generatedRoutes(t *testing.T) []route {
	t.Helper()
	raw, err := os.ReadFile(openAPIDoc)
	if err != nil {
		t.Fatalf("reading the generated OpenAPI document: %v (run `make generate`)", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Parameters  []struct {
				In string `json:"in"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the generated OpenAPI document is not JSON: %v", err)
	}

	var out []route
	for path, ops := range doc.Paths {
		for method, op := range ops {
			r := route{method: strings.ToUpper(method), template: path, rpc: rpcOf(op.OperationID)}
			for _, p := range op.Parameters {
				if p.In == "body" {
					r.body = "{}"
				}
			}
			out = append(out, r)
		}
	}
	return out
}

// rpcOf turns an operationId ("Alchemy_CreateJob") back into the RPC name.
func rpcOf(operationID string) string {
	if _, name, ok := strings.Cut(operationID, "_"); ok {
		return name
	}
	return operationID
}

// Every RPC must have an HTTP answer, and no HTTP route may exist that no RPC
// backs. §6: the gateway is a translation, so a path with nothing behind it is
// an invention and an RPC with no path is a promise the document does not keep
// — including the promise to say plainly that Review cannot be translated.
func TestEveryRPCHasExactlyOneHTTPAnswer(t *testing.T) {
	answered := map[string]int{}
	for _, r := range routes(t) {
		answered[r.rpc]++
	}

	declared := map[string]bool{}
	for _, m := range alchemyv1.Alchemy_ServiceDesc.Methods {
		declared[m.MethodName] = true
	}
	for _, s := range alchemyv1.Alchemy_ServiceDesc.Streams {
		declared[s.StreamName] = true
	}

	for name := range declared {
		switch answered[name] {
		case 1:
		case 0:
			t.Errorf("rpc %s has no HTTP route and no declared refusal; a buyer curling it gets a 404 that explains nothing", name)
		default:
			t.Errorf("rpc %s has %d HTTP routes; two ways in are two contracts to keep in step", name, answered[name])
		}
	}
	for name := range answered {
		if !declared[name] {
			t.Errorf("route for %q, which is not an RPC; the gateway is a translation and has invented an endpoint", name)
		}
	}
}

// The paths this package spells out itself must be paths the generated
// document declares.
//
// There are two of them — the upload route, because the raw-body form has to
// recognise it before the mux routes it, and the refusals, because they stand
// in for RPCs the document deliberately does not carry. Both are string
// literals that no compiler checks, and a literal that drifts from the
// annotation fails in the least visible way available: raw uploads quietly
// become JSON parse errors, and the refusal quietly becomes a 404.
func TestTheGatewaysOwnPathsAgreeWithTheGeneratedDocument(t *testing.T) {
	declared := map[string]bool{}
	for _, r := range generatedRoutes(t) {
		declared[r.template] = true
	}

	if !declared[gateway.UploadPath] {
		t.Errorf("gateway.UploadPath is %q, which the generated document does not declare; "+
			"raw uploads would fall through to the JSON mapping and fail as a parse error", gateway.UploadPath)
	}
	for _, ref := range gateway.Refusals() {
		if declared[ref.Path] {
			t.Errorf("%s is both a refusal and a generated route; the RPC it stands for has been annotated, "+
				"so the refusal is now shadowing a translation", ref.Path)
		}
	}
}
