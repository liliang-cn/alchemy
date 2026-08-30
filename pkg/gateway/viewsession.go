package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// How a browser is authorised, which is the one question the view could not
// answer by copying something that already worked.
//
// Every RPC requires a bearer token and pkg/service's auth table asserts it. A
// browser navigating to a URL sends no Authorization header, and the two easy
// ways out are both wrong:
//
//   - A token in the URL. It lands in browser history, in every access log
//     between here and the process, in the Referer of anything the page loads,
//     and in whatever the person pastes into a chat window when they share the
//     link. It is the credential itself, it does not expire, and it is now in
//     four places nobody will think to clean.
//   - A route that skips authentication because "the page has no data in it".
//     That is true of the shell and false of the first fetch it makes, and the
//     distinction is one line of refactoring away from being wrong. The auth
//     table exists precisely because the route somebody exempts is the route
//     that matters later.
//
// So: the token is typed once, into a password field on a page that is served
// as the body of a 401, and POSTed same-origin to the sign-in route in an
// Authorization header. The service is asked whether it is real. If it is, the
// answer is a random ticket in an HttpOnly, SameSite=Strict cookie scoped to
// the view's own prefix, and the token itself is held in this process's memory
// for as long as the ticket lives.
//
// What that buys, point by point against the two above: the credential is
// never in a URL, so it is in no history, no log line and no Referer. The
// cookie is not the credential — it is a receipt, it stands for one token in
// one process, and it dies with the process (§4's "returns; does not store",
// applied to sessions). HttpOnly means the page's own script cannot read it,
// so an injection that gets to run cannot exfiltrate it. SameSite=Strict means
// no other site can make a browser spend it. And every route still refuses an
// unauthenticated request with 401, which is the property the auth table
// asserts — the sign-in form is the *body* of that 401, not an exemption from
// it.
//
// The cost, stated plainly: a person on a shared machine who does not sign out
// leaves a live cookie in that browser until it expires. That is what
// sessionTTL and the sign-out route are for, and it is a smaller cost than a
// token in a URL, which leaves the credential itself behind forever.

// sessionCookie is the ticket's name. It is scoped to ViewPrefix, so it is not
// sent to /v1 at all: the REST surface authenticates by header and a cookie
// arriving there would be a credential travelling somewhere nothing reads it.
const sessionCookie = "alchemy_view"

// sessionTTL is how long a signed-in browser stays signed in.
//
// Long enough to work a held job's conflicts without being interrupted, short
// enough that a laptop left open in a meeting room is not a standing grant.
// It is not refreshed on use, deliberately: a sliding window on a page that
// polls is a session that never ends.
const sessionTTL = 8 * time.Hour

// maxSessions bounds the map. Minting requires a valid token, so this is not a
// defence against a stranger; it is a defence against a script that signs in
// per request and would otherwise grow this process's memory without bound
// for as long as it ran.
const maxSessions = 1024

// sessions maps a ticket to the token it stands for.
//
// It holds the token rather than a digest of it, and that is forced rather than
// chosen: the token has to be presented to the service on every subsequent
// call, so something has to be able to reproduce it. The consolation is that
// this process already holds every caller's credential in flight and every
// job's model API keys, so a heap dump was never safe; what this must not do
// is make the credential *durable*, and it does not — there is no file, no
// store, and a restart signs everybody out.
type sessions struct {
	mu   sync.Mutex
	live map[string]session
	// now is time.Now except in a test that needs an expired ticket without
	// waiting eight hours for one.
	now func() time.Time
}

type session struct {
	token   string
	expires time.Time
}

func newSessions() *sessions {
	return &sessions{live: map[string]session{}, now: time.Now}
}

// mint records a token and returns the ticket that stands for it.
func (s *sessions) mint(token string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand failing is not a condition to carry on through with a
		// weaker ticket. Refusing means the person cannot sign in; guessing
		// means anybody can.
		return "", err
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	if len(s.live) >= maxSessions {
		// Full of live tickets rather than expired ones. Refusing the newest
		// rather than evicting an old one is the direction that fails safe:
		// evicting signs out somebody who is working, and the person refused
		// here can retry in a moment or use a bearer header.
		return "", errTooManySessions
	}
	s.live[ticket] = session{token: token, expires: s.now().Add(sessionTTL)}
	return ticket, nil
}

// resolve turns a ticket back into the token it stands for.
func (s *sessions) resolve(ticket string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.live[ticket]
	if !ok || !sess.expires.After(s.now()) {
		// An expired ticket is deleted on the way out rather than left for the
		// next sweep, so a browser that keeps presenting one stops holding a
		// slot open against maxSessions.
		delete(s.live, ticket)
		return "", false
	}
	return sess.token, true
}

// forget drops a ticket. Signing out is the only way a credential this process
// holds is released early, so it is worth having.
func (s *sessions) forget(ticket string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.live, ticket)
}

// sweep drops what has expired. It runs on mint rather than on a timer because
// a goroutine per gateway is a thing to shut down, and the only moment this
// map can grow is the only moment it needs pruning.
func (s *sessions) sweep() {
	now := s.now()
	for ticket, sess := range s.live {
		if !sess.expires.After(now) {
			delete(s.live, ticket)
		}
	}
}

var errTooManySessions = errors.New(
	"too many browser sessions are open; sign one out, or call with an Authorization header")

// signIn exchanges a bearer token for a cookie.
//
// The token must arrive in a header rather than in the form body, and that is
// the point rather than an inconvenience: a body is logged by proxies that log
// bodies, and a header named Authorization is the one thing every piece of
// infrastructure between here and the browser already knows to redact.
func (v *viewer) signIn(w http.ResponseWriter, r *http.Request) {
	if _, ok := v.authorize(r); !ok {
		v.refuse(w, r)
		return
	}
	// The token is re-read rather than carried out of authorize(): authorize
	// answers "will the service talk to this caller", and the credential it
	// found is a separate fact. Threading it through the return would make one
	// function answer two questions, and the second one — "what should be
	// stored under this ticket" — is the one that must not be got wrong.
	token, _ := v.credential(r)
	ticket, err := v.sessions.mint(token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: ticket,
		// Scoped to the view. Nothing under /v1 reads a cookie, and a
		// credential that travels where nothing reads it is a credential
		// exposed for no reason.
		Path:     ViewPrefix,
		HttpOnly: true,
		// Strict rather than Lax: every navigation into the view is one the
		// person types or follows from the view itself, so nothing legitimate
		// arrives cross-site, and Lax would let another origin's top-level
		// link spend this cookie.
		SameSite: http.SameSiteStrictMode,
		// Secure only under TLS. Setting it unconditionally would make the
		// cookie undeliverable over the plain HTTP a buyer evaluates the
		// product on, and the failure would look like a broken sign-in rather
		// than like a policy.
		Secure: r.TLS != nil,
		MaxAge: int(sessionTTL / time.Second),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNoContent)
}

// signOut releases the ticket and tells the browser to drop the cookie.
func (v *viewer) signOut(w http.ResponseWriter, r *http.Request) {
	if _, ok := v.authorize(r); !ok {
		v.refuse(w, r)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		v.sessions.forget(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: ViewPrefix,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// refuse is every view route's answer to a caller it has no credential for.
//
// One status, one header, two bodies. 401 and WWW-Authenticate are what the
// route means and are the same for everyone; the body differs only in
// spelling, because a browser that is handed a JSON error shows a person a
// wall of punctuation and no way forward, and a curl user handed a page of
// HTML has to read markup to find the sentence. The HTML body carries the
// sign-in form, which is what makes this a 401 a person can act on rather than
// a dead end — and it carries no information about the job, the service or the
// caller, so serving it to a stranger tells them nothing they did not know
// from the port being open.
func (v *viewer) refuse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="alchemy"`)
	// A referrer would carry the job ID to whatever the page links to. It
	// links to nothing, and that is today's fact rather than a guarantee.
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !wantsHTML(r) {
		http.Error(w, "a valid bearer token is required; POST it to "+ViewPrefix+
			"session in an Authorization header to get a browser session", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write(signInPage())
}

// wantsHTML reports whether this looks like a browser navigating rather than a
// program calling. It is a spelling decision and never an authorisation one:
// both branches of refuse() answer 401.
func wantsHTML(r *http.Request) bool {
	for _, accept := range r.Header.Values("Accept") {
		if strings.Contains(accept, "text/html") {
			return true
		}
	}
	return false
}
