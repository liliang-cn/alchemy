package gateway_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/gateway"
)

// The gateway has an invariant — TestEveryRPCHasExactlyOneHTTPAnswer — that no
// route may exist that no RPC backs, and the view breaks it: a page is not a
// translation of anything.
//
// It is handled the way review.go handles its refusal rather than by widening
// that test: the exception is declared, exported, and carries the sentence that
// justifies it. Refusals() says "this RPC has no honest HTTP translation";
// Views() says "this route translates no RPC, and here is why it is allowed to
// exist anyway". Both are lists the tests read, so an undeclared route added
// later is a failure here rather than a surprise in production.
func TestEveryViewRouteDeclaresWhyItBacksNoRPC(t *testing.T) {
	views := gateway.Views()
	if len(views) == 0 {
		t.Fatal("no view routes are declared; the exception to the coverage test must be visible in the code that takes it")
	}
	for _, v := range views {
		if !strings.HasPrefix(v.Path, gateway.ViewPrefix) {
			t.Errorf("view route %s %s is outside %q; the reserved prefix is what keeps a page from ever shadowing an RPC's path",
				v.Method, v.Path, gateway.ViewPrefix)
		}
		if strings.HasPrefix(v.Path, "/v1/") {
			t.Errorf("view route %s is under /v1/, where the translated RPCs live", v.Path)
		}
		if v.Because == "" {
			t.Errorf("view route %s %s declares no reason; an exception that does not explain itself is a hole with a comment missing", v.Method, v.Path)
		}
	}
}

// The reserved prefix must not collide with anything the generated document
// declares. This is the same check TestTheGatewaysOwnPathsAgreeWithTheGenerated
// Document makes for the refusals, from the other side: a refusal must not
// shadow a translation, and a view must not either.
func TestNoViewRouteShadowsAGeneratedRoute(t *testing.T) {
	declared := map[string]bool{}
	for _, r := range generatedRoutes(t) {
		declared[r.template] = true
		if strings.HasPrefix(r.template, gateway.ViewPrefix) {
			t.Errorf("generated route %s is under the view's reserved prefix %q", r.template, gateway.ViewPrefix)
		}
	}
	for _, v := range gateway.Views() {
		if declared[v.Path] {
			t.Errorf("view route %s is also a generated route; the page would be shadowing a translation", v.Path)
		}
	}
}

// A path under the reserved prefix that nobody declared must 404 rather than
// fall through to something. The prefix is routed as a whole, so this is the
// test that says the routing inside it is a table and not a catch-all.
func TestAnUndeclaredViewPathIsNotFound(t *testing.T) {
	f := serve(t, harness{})
	resp := f.do(t, http.MethodGet, gateway.ViewPrefix+"not-a-thing", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d for an undeclared view path, want 404", resp.StatusCode)
	}
}

// The prefix without its slash is the URL a person actually types.
//
// It is not a declared route and must not become one — a second spelling of a
// page is a second thing to authenticate — so it is a redirect. Getting this
// wrong costs a bare 404 to the one caller who typed the address by hand
// instead of pasting the startup line, which is the caller least equipped to
// guess what went wrong.
func TestThePrefixWithoutItsSlashRedirectsRatherThanVanishing(t *testing.T) {
	f := serve(t, harness{})
	bare := strings.TrimSuffix(gateway.ViewPrefix, "/")

	client := *f.http.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequest(http.MethodGet, f.http.URL+bare, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", bare, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("GET %s: status = %d, want 301", bare, resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != gateway.ViewPrefix {
		t.Errorf("Location = %q, want %q", got, gateway.ViewPrefix)
	}
}
