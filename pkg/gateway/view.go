package gateway

import (
	"context"
	"net/http"
	"strings"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The browser view: a job's graph, drawn in three dimensions and turnable.
//
// This is the one part of the package that is not a translation, and saying so
// is the whole of how it is allowed to exist. routes_test.go asserts that no
// HTTP route exists that no RPC backs, and a page backs no RPC. Widening that
// test would have cost exactly the property it protects — that a route added
// later is either a translation or an announced exception — so the exception is
// declared instead, in Views() below, in the same shape review.go declares its
// refusal and for the same reason: a list a test reads cannot be forgotten.
//
// What keeps the exception narrow is that the view is a *client* of the RPCs
// and never a second way into the service. Every one of these handlers ends in
// a gRPC call carrying the caller's own credential, exactly as a generated
// route does. There is no query the page can ask that a buyer with curl and a
// token could not ask, and no answer it gets that pkg/service did not decide.
// The page holds opinions the gateway does not — what a browser can draw, what
// a person can read at a glance — and those opinions are about pixels.

// ViewPrefix is the path everything the browser view serves lives under.
//
// It is reserved as a whole rather than route by route, so that a page can
// never shadow an RPC's path: /v1 is the translated service, /ui is the
// viewer, and a request cannot be ambiguous between them. It is exported
// because the tests build their tables from it, the same way they build the
// route table from the generated document rather than from a list.
const ViewPrefix = "/ui/"

// View is a route the gateway serves that translates no RPC.
//
// Because is not documentation. §6 makes the gateway a translation, so a route
// that translates nothing is a deliberate breach of the package's own rule,
// and a breach without a stated reason is indistinguishable from a mistake. It
// is a field rather than a comment so a test can insist every one of them has
// one.
type View struct {
	Method string
	// Path is an http.ServeMux pattern, wildcards and all. The generated
	// routes use grpc-gateway templates; these do not, because they are not
	// generated and the standard library's router is the one thing here that
	// is neither invented nor a dependency.
	Path string
	// Because says why this route may exist without an RPC behind it.
	Because string
}

// Views is every route the browser view answers.
//
// Two serve HTML and hold no data (the landing page and the page shell); two
// are the credential exchange; one is the graph, and it is the only one that
// reads a result. Keeping the data on its own route is what lets the page be
// cached, reloaded and resized without asking the service for four hundred
// thousand records again, and it is what makes the size and held-job decisions
// testable as JSON rather than as pixels.
//
// The last three are the ones that change something, and they are proxies of
// one RPC each rather than pages. They exist because the page cannot reach
// /v1 at all: the session cookie is scoped to this prefix and is HttpOnly, so
// the browser will not send it there and the page cannot read it in order to
// build a header — see viewedit.go for why the two ways round that are both
// worse than a proxy. They are still clients of the service in the sense this
// file's opening paragraph means: every one ends in the same RPC a buyer with
// curl would call, carrying the same caller's credential, and decides nothing
// the service did not decide.
func Views() []View {
	return []View{{
		Method: http.MethodGet,
		Path:   ViewPrefix + "{$}",
		Because: "The landing page. It backs no RPC because there is no ListJobs to back it — §4 means the " +
			"service holds work in progress and not a catalogue — so this asks which job to open and nothing more.",
	}, {
		Method: http.MethodPost,
		Path:   ViewPrefix + "session",
		Because: "The credential exchange. A browser navigating to a URL sends no Authorization header, and a token " +
			"in a URL lands in history, logs and referrers; this takes the token in a header, asks pkg/service " +
			"whether it is real, and hands back a short-lived cookie that stands for it. It backs no RPC because " +
			"it decides nothing about the token — the service does — and holds nothing but a receipt.",
	}, {
		Method:  http.MethodDelete,
		Path:    ViewPrefix + "session",
		Because: "Signing out. A cookie that cannot be given back is a cookie somebody leaves behind on a shared machine.",
	}, {
		Method: http.MethodGet,
		Path:   ViewPrefix + "jobs/{job_id}",
		Because: "The page itself: one self-contained document with no external script, because §5b's obligations " +
			"are not met by a viewer that renders nothing on a machine with no internet access.",
	}, {
		Method: http.MethodGet,
		Path:   ViewPrefix + "jobs/{job_id}/graph",
		Because: "The JSON the page draws. It backs no RPC because it is not one RPC: it is StreamResult read until " +
			"a browser's budget is spent, or — for a job held on a conflict, which GetResult refuses on purpose " +
			"(§7.3) — WatchJob, which is what the service is willing to say about a held job.",
	}, {
		Method: http.MethodGet,
		Path:   ViewPrefix + "jobs/{job_id}/findings",
		Because: "ListFindings, reachable by a browser. It is a second spelling of a translated route rather than a " +
			"new answer, and it exists because the cookie this view mints is scoped to this prefix and is " +
			"HttpOnly: the page can neither send it to /v1/jobs/{id}/findings nor read it to build a header. " +
			"The queue is what a decision names — an item id exists nowhere else — so without this the page " +
			"could show a finding and could not act on one.",
	}, {
		Method: http.MethodPost,
		Path:   ViewPrefix + "jobs/{job_id}/decisions",
		Because: "Decide, reachable by a browser, for the same cookie reason. This is what 'modify' and 'delete' " +
			"are on a job still held for review: REVIEW_VERB_EDIT retypes, renames, redirects or merges, and " +
			"REVIEW_VERB_REJECT takes a record out before the graph is delivered. It adds nothing to the RPC — " +
			"the verb, the signature and the queue are all pkg/service's to judge.",
	}, {
		Method: http.MethodPost,
		Path:   ViewPrefix + "assertions",
		Because: "Assert, reachable by a browser, for the same cookie reason. It is how the view adds a fact, and " +
			"— because §4 means a delivered result is the output of a job and not state on a server — it is " +
			"also the only honest way to correct or retire a record of a job that has already finished: an " +
			"assertion that supersedes says the old record is over and names who said so, rather than deleting " +
			"something nothing here holds.",
	}}
}

// viewRoutes mounts the view in front of the translated ones.
//
// It is a wrapper rather than a route on the generated mux for the same reason
// rawUploads is one: the standard library's router understands the patterns
// these routes are written in, grpc-gateway's understands the ones the
// generated routes are written in, and mixing the two spellings in one table
// is how a path stops meaning what it reads as. A request outside the reserved
// prefix is passed through untouched.
func viewRoutes(next http.Handler, v *viewer) http.Handler {
	mux := http.NewServeMux()
	handlers := map[string]http.HandlerFunc{
		http.MethodGet + " " + ViewPrefix + "{$}":                      v.landing,
		http.MethodPost + " " + ViewPrefix + "session":                 v.signIn,
		http.MethodDelete + " " + ViewPrefix + "session":               v.signOut,
		http.MethodGet + " " + ViewPrefix + "jobs/{job_id}":            v.page,
		http.MethodGet + " " + ViewPrefix + "jobs/{job_id}/graph":      v.graph,
		http.MethodGet + " " + ViewPrefix + "jobs/{job_id}/findings":   v.findings,
		http.MethodPost + " " + ViewPrefix + "jobs/{job_id}/decisions": v.decisions,
		http.MethodPost + " " + ViewPrefix + "assertions":              v.assertions,
	}
	for _, view := range Views() {
		pattern := view.Method + " " + view.Path
		h, ok := handlers[pattern]
		if !ok {
			// Unreachable unless Views() and the table above disagree, which is
			// the one way this file can grow a route nobody declared — the
			// exact failure the declaration exists to prevent. Panicking at
			// construction is the loudest available answer and it happens in
			// every test that builds a gateway.
			panic("gateway: view route " + pattern + " is declared and not handled")
		}
		mux.HandleFunc(pattern, h)
	}
	bare := strings.TrimSuffix(ViewPrefix, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == bare {
			// The address a person types, rather than the one the startup line
			// prints. It is a redirect and not a route: a second spelling of a
			// page is a second thing to authenticate, and this way there is
			// still exactly one of each.
			http.Redirect(w, r, ViewPrefix, http.StatusMovedPermanently)
			return
		}
		if !strings.HasPrefix(r.URL.Path, ViewPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// viewer is the state the view needs: a way to call the service, and the
// sessions it has minted.
type viewer struct {
	client   alchemyv1.AlchemyClient
	sessions *sessions
}

func newViewer(client alchemyv1.AlchemyClient) *viewer {
	return &viewer{client: client, sessions: newSessions()}
}

// authorize turns a request into a context that carries a credential the
// service will accept, or refuses.
//
// The refusal is written here and the *decision* is not: this function never
// judges a token. It finds one — in an Authorization header, or in a cookie
// that stands for one — and asks the service, with the cheapest question whose
// answer is exactly the one needed. GetJob with no job ID never touches the
// store: authentication runs in an interceptor before the method, so a bad
// credential comes back Unauthenticated and a good one comes back
// InvalidArgument. Anything but Unauthenticated means the service was willing
// to talk to this caller.
//
// Doing it this way rather than comparing tokens here is not fastidiousness.
// review.go already names the cost of the alternative: a gateway that decided
// for itself what a valid token looks like would be a second source of truth
// about authentication, "which is the one place that phrase is not a design
// nicety but a vulnerability". A rotated token invalidates every cookie for
// free, because the cookie was only ever a receipt for a question the service
// answered.
func (v *viewer) authorize(r *http.Request) (context.Context, bool) {
	token, ok := v.credential(r)
	if !ok {
		return nil, false
	}
	ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+token))
	_, err := v.client.GetJob(ctx, &alchemyv1.GetJobRequest{})
	if status.Code(err) == codes.Unauthenticated {
		return nil, false
	}
	return ctx, true
}

// credential finds the token a request is offering.
//
// The header comes first so that curl behaves here exactly as it does on every
// other route — a buyer testing the view with a token in a header should not
// have to learn a second scheme — and so that the sign-in route, which is the
// one place a cookie cannot help, is served by the same lookup as the rest.
func (v *viewer) credential(r *http.Request) (string, bool) {
	if token, ok := bearerHeader(r.Header.Get("Authorization")); ok {
		return token, true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	return v.sessions.resolve(c.Value)
}

// bearerHeader is pkg/service's bearer() again, and the duplication is
// deliberate: this one is not deciding anything, only spelling out where in
// the header the credential sits, and importing the service's unexported
// parser would be the coupling §6 spends a paragraph refusing.
func bearerHeader(value string) (string, bool) {
	const scheme = "bearer "
	if len(value) <= len(scheme) || !strings.EqualFold(value[:len(scheme)], scheme) {
		return "", false
	}
	token := strings.TrimSpace(value[len(scheme):])
	return token, token != ""
}
