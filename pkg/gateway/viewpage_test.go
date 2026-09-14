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

// Every verb the review queue takes has a control on the page.
//
// This is the test that would have caught a page with no Accept. pkg/review
// takes four verbs; three of them name a queue item, and the page could send
// two. That gap is not cosmetic: a duplicate pair is answered "these are two
// things" with accept, merge.go refuses reject on a duplicate item outright,
// and edit means merge — so with accept missing there was no way to keep two
// records that a reviewer believes in, and the job stayed held until somebody
// left the browser and used the RPC.
//
// Asserting on the markup is coarse, and it is the right coarseness for the
// same reason the counts test gives: no Go test of a handler notices that a
// button is gone.
func TestThePageCanSendEveryVerbThatNamesAQueueItem(t *testing.T) {
	f := serve(t, harness{})
	page := pageBody(t, f, gateway.ViewPrefix)
	for _, verb := range []string{"REVIEW_VERB_ACCEPT", "REVIEW_VERB_EDIT", "REVIEW_VERB_REJECT"} {
		if !strings.Contains(page, verb) {
			t.Errorf("the page never sends %s; it is a verb the queue takes and a reviewer who needs it has to leave the browser", verb)
		}
	}
}

// A held job's findings reach the page from the queue, not only from the graph.
//
// WatchJob carries counts and conflicts and nothing else, which is viewdata.go's
// own decision and a sound one. The consequence is that a job held with
// duplicates rather than conflicts drew an empty canvas and a findings list
// reading "(none)", while the banner told the reviewer to click the side they
// believe. ListFindings answers over HTTP for exactly this state, the page
// already fetches it, and this asserts it is used for drawing and for listing
// rather than only for resolving an item id.
func TestAHeldJobDrawsTheQueueItCanStillBeAskedAbout(t *testing.T) {
	f := serve(t, harness{})
	page := pageBody(t, f, gateway.ViewPrefix)
	for _, needed := range []string{"buildHeldPairs", "FINDINGS"} {
		if !strings.Contains(page, needed) {
			t.Errorf("the page has no %s; a job held by anything but a conflict has nothing to draw and nothing to list", needed)
		}
	}
}

// TestAConflictPanelIsTitledByWhatAnAnswerActsOn is the defect a reviewer
// would only find by destroying the wrong fact.
//
// A held job draws each conflict as two claim nodes, one per side. The queue
// holds one item per conflict and that item names the subject, so
// selectedRecord returns the subject whichever side was clicked — both sides'
// buttons act on the same record. The panel, though, was titled with the
// clicked side. Click the losing claim, read its statement at the top of the
// panel, press Reject meaning "take this one out", and the record taken out is
// the other one: the fact you meant to keep.
//
// Asserted against the page source because the bug is in which value reaches
// the heading, and that is one line. The page is served whole, so the line is
// in the body of every held job.
func TestAConflictPanelIsTitledByWhatAnAnswerActsOn(t *testing.T) {
	f := serve(t, harness{})
	id := f.aDDLJob(t)
	page := pageBody(t, f, gateway.ViewPrefix+"jobs/"+id)

	// The claim branch has to exist at all: without it every node, conflict
	// sides included, is titled by its own label again.
	if !strings.Contains(page, `if (n.kind === "claim")`) {
		t.Fatal("showDetail does not distinguish a conflict's claim node, so both sides are titled by the side clicked while the buttons act on the subject")
	}
	// And the side that was clicked must still be somewhere, or the panel
	// stops saying which of the two the reader is looking at.
	if !strings.Contains(page, `row("side you clicked", n.label)`) {
		t.Error("the clicked side is not shown, so the panel cannot say which claim the reader opened")
	}
}
