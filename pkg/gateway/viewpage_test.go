package gateway_test

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/gateway"
)

func pageBody(t *testing.T, f *fixture, path string) string {
	t.Helper()
	resp := f.do(t, http.MethodGet, path, testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", path, resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(raw)
}

// The offline guarantee, as a test rather than as a promise in a comment.
//
// This runs on a private VM. A page that pulls a graph library from a CDN
// renders a blank rectangle on a machine with no route out, and does it
// silently — a script that fails to load raises nothing a person sees, so the
// symptom is "the viewer is broken" and the cause is three networks away. The
// only way to be sure is to have nothing to fetch, so the assertion is that
// the document references no external origin at all.
func TestThePageFetchesNothingFromTheNetwork(t *testing.T) {
	f := serve(t, harness{})
	id := f.aDDLJob(t)

	external := regexp.MustCompile(`(?i)(src|href)\s*=\s*["']\s*(https?:)?//`)
	for _, page := range []string{
		pageBody(t, f, gateway.ViewPrefix),
		pageBody(t, f, gateway.ViewPrefix+"jobs/"+id),
	} {
		if m := external.FindString(page); m != "" {
			t.Errorf("the page references an external resource (%q); on a VM with no internet it renders nothing and says nothing", m)
		}
		for _, host := range []string{"cdn.", "unpkg", "jsdelivr", "cdnjs", "googleapis", "fonts.g"} {
			if strings.Contains(page, host) {
				t.Errorf("the page mentions %q", host)
			}
		}
	}
}

// The unauthenticated answer is a page a person can act on. It is the body of
// a 401 rather than a route of its own (see viewauth_test.go), so what is
// tested here is only that the body is usable: it has somewhere to type the
// token, and it says where the token goes.
func TestTheUnauthenticatedPageOffersAWayIn(t *testing.T) {
	f := serve(t, harness{})
	req, err := http.NewRequest(http.MethodGet, f.http.URL+gateway.ViewPrefix, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	page := string(raw)
	if !strings.Contains(page, `type="password"`) {
		t.Error("no password field; a person navigating here has no way to present a token")
	}
	if !strings.Contains(page, gateway.ViewPrefix+"session") {
		t.Errorf("the page does not name %ssession, which is where the token goes", gateway.ViewPrefix)
	}
	if strings.Contains(page, testToken) {
		t.Fatal("the 401 body contains the token")
	}
}

// §5b's obligations are what separate this viewer from one for a different
// product, so the page has to be able to say all of them. These are the words
// that must be in the document: the counts block §5 requires in full, and the
// producer split §5b calls the field that matters.
//
// Asserting on the markup is coarse and it is the right coarseness: a template
// that quietly loses the violations counter is a viewer that shows a run with
// four hundred violations as a success, and no Go test of a handler would
// catch it.
func TestThePageCanSayEverySectionFiveNumber(t *testing.T) {
	f := serve(t, harness{})
	page := pageBody(t, f, gateway.ViewPrefix)
	for _, name := range []string{
		"entities", "relations", "deterministic", "inferred",
		"violations", "conflicts", "duplicates", "chunks_empty", "guesses",
	} {
		if !strings.Contains(page, name) {
			t.Errorf("the page never mentions %q; §5 says every returned graph carries that number", name)
		}
	}
	for _, producer := range []string{"ddl", "graph-import", "llm-extract", "tabular"} {
		if !strings.Contains(page, producer) {
			t.Errorf("the page never mentions the %q producer; §5b's field that matters cannot be the primary encoding if the legend cannot name it", producer)
		}
	}
	if !strings.Contains(page, "<noscript") {
		t.Error("no <noscript>; a browser with scripting off must be told why the page is empty rather than shown an empty page")
	}
	if !strings.Contains(page, "prefers-color-scheme") {
		t.Error("the page has no dark-mode rule; it must work in both")
	}
}
