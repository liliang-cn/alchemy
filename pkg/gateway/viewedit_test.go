package gateway_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/gateway"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// The four things a person can do to a graph from a browser, and the one fact
// that shapes all of them: §4 means the service holds no graph.
//
// Query is client-side over JSON the page already has, so there is no Go here
// for it. The other three are not. "Add" is an assertion, which is a job of its
// own. "Modify" and "delete" are two different operations wearing one word —
// on a job still held for review they are decisions on its queue, and on a job
// that has already been delivered the queue is closed and the honest act is a
// superseding assertion, which states that a record is over without deleting
// anything the service does not hold.
//
// These tests are about the three routes that carry those, and every one of
// them asserts the same property the rest of this package asserts: the answer
// is pkg/service's, re-spelled in HTTP, and the gateway decided none of it.

// post is a JSON request to a view route, returning the response for the test
// to judge. It exists because every route below takes the same shape and a
// test that spells out four lines of request construction each time is a test
// whose subject is buried.
func (f *fixture) post(t *testing.T, path, jsonBody string) *http.Response {
	t.Helper()
	return f.do(t, http.MethodPost, path, testToken, strings.NewReader(jsonBody),
		"Content-Type", "application/json")
}

// text reads a response body as the sentence it is, for the refusals, where
// what is being asserted is that the service's own words arrived.
func text(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

// A held job's queue has to reach the browser, because a decision names an
// item and item ids exist nowhere else.
//
// This is the route that makes "modify" and "delete" possible at all on a held
// job: the graph JSON carries conflicts, and a conflict is not a thing you can
// decide about — the queue's item id is. Without this the page would have to
// invent item ids, which is the shape of every bug where a client and a server
// disagree about what a record is called.
func TestTheViewCarriesTheReviewQueueOfAHeldJob(t *testing.T) {
	f := serve(t, harness{run: resultOf(disputed())})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	resp := f.do(t, http.MethodGet, gateway.ViewPrefix+"jobs/"+id+"/findings", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", resp.StatusCode, text(t, resp))
	}
	got := body(t, resp)
	if got["state"] != alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW.String() {
		t.Errorf("state = %v; the list alone cannot say why it is empty, so the state travels with it", got["state"])
	}
	items, _ := got["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no items; a job held on a conflict is held by a question, and the question is what the queue is")
	}
	first, _ := items[0].(map[string]any)
	if s, _ := first["id"].(string); s == "" {
		t.Error("the first item has no id; a decision names an item and there is nowhere else to learn its name")
	}
}

// Modifying a record on a held job is a decision, and the whole of what this
// route does is carry it.
//
// REVIEW_VERB_EDIT with an Edit is §5c's "retype, rename, redirect", and the
// browser has no second way to spell it: the same message, the same verb, the
// same code in pkg/service that the gRPC stream reaches. What is asserted here
// is that the round trip works and that the response says what the batch did,
// because a page that submitted a decision and could not tell whether it
// applied would leave a person clicking until something changed.
func TestAModificationOnAHeldJobIsCarriedAsADecision(t *testing.T) {
	f := serve(t, harness{run: resultOf(disputedRecord())})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	items, _ := body(t, f.do(t, http.MethodGet, gateway.ViewPrefix+"jobs/"+id+"/findings", testToken, nil))["items"].([]any)
	item, _ := items[0].(map[string]any)["id"].(string)

	resp := f.post(t, gateway.ViewPrefix+"jobs/"+id+"/decisions",
		`{"decisions":[{"item_id":"`+item+`","verb":"REVIEW_VERB_EDIT","by":"liliang","note":"the contract is newer","edit":{"type":"Supplier"}}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", resp.StatusCode, text(t, resp))
	}
	got := body(t, resp)
	if applied, _ := got["applied"].(float64); applied != 1 {
		t.Errorf("applied = %v, want 1; a decision the page cannot confirm is a decision the person will make twice", got["applied"])
	}
	if _, ok := got["remaining_holding"]; !ok {
		t.Error("no remaining_holding; it is the number that says whether the job can finish, and the page has nothing else to read it from")
	}
}

// A decision nobody signed must not leave the browser, and if it does it must
// be refused by the service rather than by this package.
//
// pkg/service refuses it in as many words — "a review with no reviewer is not
// a review" — and the value of asserting the sentence here rather than only
// the status is that it is the sentence the page shows. A gateway that
// invented a friendlier one would be a second source of truth about what a
// valid decision is, which is the thing this package exists not to be.
func TestADecisionThatNamesNobodyIsRefusedInTheServicesOwnWords(t *testing.T) {
	f := serve(t, harness{run: resultOf(disputed())})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	items, _ := body(t, f.do(t, http.MethodGet, gateway.ViewPrefix+"jobs/"+id+"/findings", testToken, nil))["items"].([]any)
	item, _ := items[0].(map[string]any)["id"].(string)

	resp := f.post(t, gateway.ViewPrefix+"jobs/"+id+"/decisions",
		`{"decisions":[{"item_id":"`+item+`","verb":"REVIEW_VERB_REJECT"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a decision with no reviewer on it", resp.StatusCode)
	}
	if got := text(t, resp); !strings.Contains(got, "no reviewer") {
		t.Errorf("body = %q, which is not the service's refusal; the page shows this sentence to the person who has to fix it", got)
	}
}

// The queue of a delivered job is closed, and the route says so rather than
// pretending.
//
// This is the fact the whole two-shaped design rests on. It was first written
// against the answer the service then gave — 200 with the decision reported
// back as rejected — which was honest for a job that never had review, because
// its queue is empty and an unknown item is a rejection. It was NOT honest for
// a job that had review and finished: hub.close leaves the items in place, so
// vet found the item, the hub recorded the answer, and resolve returned at its
// first line because the state is no longer NEEDS_REVIEW. That caller was
// handed "applied: 1" for a decision that changed nothing.
//
// Two answers to one question, and the wrong one arrived exactly where a
// person would trust it. The service now refuses both, with the sentence that
// says where corrections do go — because "you cannot edit this" without "here
// is how" is the answer that sends somebody to widen a rule they should not.
func TestADecisionOnADeliveredJobIsRefusedRatherThanQuietlyGoingNowhere(t *testing.T) {
	f := serve(t, harness{run: resultOf(mixedProducers())})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	resp := f.post(t, gateway.ViewPrefix+"jobs/"+id+"/decisions",
		`{"decisions":[{"item_id":"whatever","verb":"REVIEW_VERB_REJECT","by":"liliang","note":"wrong"}]}`)
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d (%s), want 412: a decision that goes nowhere and answers 200 is the "+
			"failure that looks like a success", resp.StatusCode, text(t, resp))
	}
	got := text(t, resp)
	if !strings.Contains(got, "supersedes") {
		t.Errorf("body = %q; the refusal has to name the route that does work, because the page "+
			"shows this sentence to the person who has to get their correction in somehow", got)
	}
}

// The decision route is about the job in its path, and a body that says
// otherwise does not get to move it.
//
// The generated route for Decide overwrites job_id from the path for exactly
// this reason, and this one does the same rather than something of its own: a
// URL that reads as one job and acts on another is the kind of difference
// between two spellings of a route that nobody finds until it matters.
func TestTheDecisionRouteIsAboutTheJobNamedInItsPath(t *testing.T) {
	f := serve(t, harness{run: resultOf(disputed())})
	held := f.aDDLJob(t)
	f.awaitState(t, held, alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	resp := f.post(t, gateway.ViewPrefix+"jobs/"+held+"/decisions",
		`{"job_id":"some-other-job","decisions":[{"item_id":"nothing","verb":"REVIEW_VERB_REJECT","by":"liliang"}]}`)
	// The path wins, so this is the held job answering about an item it does
	// not have — not a 404 for a job that was never named here.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200 from the job in the path", resp.StatusCode, text(t, resp))
	}
	if got, _ := body(t, resp)["job_id"].(string); got != held {
		t.Errorf("job_id = %q, want %q; the path names the job and the body does not get a second opinion", got, held)
	}
}

// Adding a fact from the browser is an assertion, and an assertion is a job of
// its own rather than a record joining the graph on screen.
//
// The two things asserted are the two the page has to be able to say. The
// record comes back stamped PRODUCER_HUMAN and named — §5b's field that
// matters, for the one producer whose warrant is that somebody can be asked —
// and the result carries a job id, which is what makes it traceable later and
// what makes it plainly not part of the job the person was looking at.
func TestAnAssertionFromTheBrowserComesBackStampedHumanWithItsOwnJob(t *testing.T) {
	f := serve(t, harness{})

	resp := f.post(t, gateway.ViewPrefix+"assertions",
		`{"by":"liliang","note":"Bruno took the office in March","entities":[{"id":"p:bruno","type":"Person","name":"Bruno"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", resp.StatusCode, text(t, resp))
	}
	got := body(t, resp)
	if job, _ := got["job"].(string); job == "" {
		t.Error("the assertion carries no job id; a record nobody can trace back is the record this endpoint exists to replace")
	}
	entities, _ := got["entities"].([]any)
	if len(entities) != 1 {
		t.Fatalf("%d entities came back, want 1", len(entities))
	}
	prov, _ := entities[0].(map[string]any)["provenance"].(map[string]any)
	if prov["producer"] != "PRODUCER_HUMAN" {
		t.Errorf("producer = %v, want PRODUCER_HUMAN", prov["producer"])
	}
	if prov["by"] != "liliang" {
		t.Errorf("by = %v, want the asserter; a fact whose only authority is a person has to name them", prov["by"])
	}
}

// Deleting on a delivered job is a supersession, and the service refuses one
// that does not explain itself.
//
// This is the refusal the page's "reason" field exists for. A correction with
// no reason cannot be argued with by whoever finds it next, and the browser
// must not be the one client that gets to skip it — so the route carries the
// refusal through with the service's own sentence rather than defaulting a
// reason in.
func TestASupersessionWithNoReasonIsRefusedThroughTheView(t *testing.T) {
	f := serve(t, harness{})

	resp := f.post(t, gateway.ViewPrefix+"assertions",
		`{"by":"liliang","entities":[{"id":"p:bruno","type":"Person","name":"Bruno"}],"supersedes":[{"retires":"p:ada"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := text(t, resp); !strings.Contains(got, "without saying why") {
		t.Errorf("body = %q, which is not the service's refusal", got)
	}
}

// An assertion nobody signed is refused, and the page must not be able to send
// one either.
func TestAnAssertionThatNamesNobodyAssertingIsRefused(t *testing.T) {
	f := serve(t, harness{})

	resp := f.post(t, gateway.ViewPrefix+"assertions",
		`{"entities":[{"id":"p:bruno","type":"Person","name":"Bruno"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := text(t, resp); !strings.Contains(got, "anonymous claim") {
		t.Errorf("body = %q, which is not the service's refusal", got)
	}
}

// The three routes that change something are exactly as authenticated as the
// two that read.
//
// A route added to a page is a route somebody can call with curl, and the one
// that writes is the one it matters for. This is auth_test.go's property
// asserted again on the new surface rather than assumed to have been inherited
// from the prefix.
func TestTheViewsWritingRoutesRefuseACallerWithNoCredential(t *testing.T) {
	f := serve(t, harness{})
	id := f.aDDLJob(t)

	for _, call := range []struct{ method, path string }{
		{http.MethodGet, gateway.ViewPrefix + "jobs/" + id + "/findings"},
		{http.MethodPost, gateway.ViewPrefix + "jobs/" + id + "/decisions"},
		{http.MethodPost, gateway.ViewPrefix + "assertions"},
	} {
		resp := f.do(t, call.method, call.path, "", strings.NewReader(`{}`), "Content-Type", "application/json")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", call.method, call.path, resp.StatusCode)
		}
		if got := resp.Header.Get("WWW-Authenticate"); got == "" {
			t.Errorf("%s %s: no WWW-Authenticate on the refusal", call.method, call.path)
		}
	}
}

// And they must not become an oracle either: a job that does not exist answers
// the way GetJob answers, and never differently because the route is new.
func TestTheViewsWritingRoutesDoNotLeakWhichJobsExist(t *testing.T) {
	f := serve(t, harness{})

	resp := f.do(t, http.MethodGet, gateway.ViewPrefix+"jobs/never-existed/findings", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("findings: status = %d, want 404", resp.StatusCode)
	}
	resp = f.post(t, gateway.ViewPrefix+"jobs/never-existed/decisions",
		`{"decisions":[{"item_id":"x","verb":"REVIEW_VERB_REJECT","by":"liliang"}]}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("decisions: status = %d, want 404", resp.StatusCode)
	}
}

// A body that is not JSON is a bad request and not a panic, and the refusal
// names the route's own vocabulary rather than a decoder's.
func TestAMalformedBodyIsRefusedRatherThanCarriedToTheService(t *testing.T) {
	f := serve(t, harness{})
	resp := f.post(t, gateway.ViewPrefix+"assertions", `{"by":`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a body that is not JSON", resp.StatusCode)
	}
}

// disputedRecord is disputed() with the record the conflict is about actually
// in the graph.
//
// The distinction is pkg/review's and it matters to anything that offers a
// "modify" button: a decision that edits acts on the records the dissenting
// side produced, and an item with no targets can only be accepted. disputed()
// carries a conflict about a subject no entity in the result has, so an edit
// on it is refused — "names no record in this result, so edit has nothing to
// act on" — which is the correct answer to that graph and the wrong fixture
// for this test.
func disputedRecord() alchemy.Result {
	res := disputed()
	// The right claim's own provenance, because that is what the queue matches
	// on: the item is about the newcomer, so the record it acts on is the one
	// the dissenting source produced.
	res.Entities = []alchemy.Entity{{
		ID: "Acme", Type: "Supplier", Name: "Acme",
		Provenance: res.Conflicts[0].Right.Provenance,
	}}
	res.Counts.Entities = 1
	res.Counts.Inferred = 1
	return res
}

// ---------------------------------------------------------------------------
// The page. Coarse assertions on markup, for the same reason
// TestThePageCanSayEverySectionFiveNumber makes them: a template that quietly
// loses the sentence saying which of two operations a button performs is a
// viewer that lies about what it just did, and no Go test of a handler catches
// it.
// ---------------------------------------------------------------------------

// alchemy.ProducerHuman is a producer like the other four, and a legend that
// cannot name it is a legend that will show an asserted record as an unknown
// one. §5b's field that matters cannot be the primary encoding if the key
// beside the picture is missing a word.
func TestTheLegendNamesTheHumanProducer(t *testing.T) {
	f := serve(t, harness{})
	page := pageBody(t, f, gateway.ViewPrefix)
	if !strings.Contains(page, "human") {
		t.Error("the legend never mentions the \"human\" producer; a record a person asserted would be drawn as an unknown one")
	}
	// And it must be drawn on the read side of §5b's split, because
	// alchemy.Producer.Deterministic() says it is: "a person signing their name
	// to a sentence is the clearest case of stating there is". A page whose own
	// list of deterministic producers left it out would draw a signed assertion
	// warm and dashed, and hide it behind "Only what was guessed" — which is
	// the page telling a person the opposite of what the record says.
	if !strings.Contains(page, "PRODUCER_HUMAN") {
		t.Error("the page's deterministic producers do not include PRODUCER_HUMAN; an assertion somebody signed would be drawn as a guess")
	}
}

// The page has to say, on screen and in one sentence, which of the two
// operations it is about to perform.
//
// A button that means "decide" on one job and "supersede" on another,
// depending on a state the person cannot see, is worse than two buttons: the
// first time it does the other thing, the person has already clicked it. So
// both words are in the document, and the page picks between them from the
// job's state.
func TestThePageNamesBothShapesOfACorrection(t *testing.T) {
	f := serve(t, harness{})
	page := pageBody(t, f, gateway.ViewPrefix)
	for _, word := range []string{"decision", "supersede", "REVIEW_VERB_EDIT", "REVIEW_VERB_REJECT"} {
		if !strings.Contains(page, word) {
			t.Errorf("the page never mentions %q; it cannot tell a person which of the two corrections it is making", word)
		}
	}
	if !strings.Contains(page, "never removes") && !strings.Contains(page, "never deletes") {
		t.Error("the page nowhere says that alchemy states a record is over rather than removing it; the word on the button has to say what actually happens")
	}
}

// The page must be able to show what an assertion came back with, and
// proposals are the half that is easy to drop: violations are per record and
// obvious, proposals are one entry per undeclared type and are the thing that
// tells a person their vocabulary is missing a word.
func TestThePageCanShowWhatAnAssertionCameBackWith(t *testing.T) {
	f := serve(t, harness{})
	page := pageBody(t, f, gateway.ViewPrefix)
	for _, word := range []string{"proposals", "violations", "assertions"} {
		if !strings.Contains(page, word) {
			t.Errorf("the page never mentions %q; an assertion whose answer is not shown is a fact sent into the dark", word)
		}
	}
}
