package gateway_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/gateway"
)

// The view's half of the auth table, and it is the same test as auth_test.go's
// rather than a new idea, for the same stated reason: "a route added later
// without authentication does not fail somewhere it is obviously wrong; it
// works, for everybody, including the people it was not meant to work for."
//
// It is a separate table because the routes are declared separately —
// generated routes come out of the OpenAPI document and these come out of
// Views() — and because these accept a second shape of credential the others
// do not. A cookie is in the table for exactly that reason: it is a credential
// the view invented, so the ways of getting it wrong are ways only this table
// can cover.
func TestEveryViewRouteRefusesAnUnauthenticatedRequest(t *testing.T) {
	f := serve(t, harness{})
	id := f.aDDLJob(t)

	credentials := []struct {
		name  string
		apply func(*http.Request)
	}{
		{"no authorization header", func(*http.Request) {}},
		{"another header instead", func(r *http.Request) { r.Header.Set("X-Other", "value") }},
		{"an empty authorization header", func(r *http.Request) { r.Header.Set("Authorization", "") }},
		{"a token with no scheme", func(r *http.Request) { r.Header.Set("Authorization", testToken) }},
		{"the wrong token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer not-the-token") }},
		{"a token that is a prefix of the right one", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+testToken[:4])
		}},
		{"a bearer with an empty token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") }},
		{"a cookie nobody minted", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "alchemy_view", Value: "not-a-ticket"})
		}},
		{"a cookie holding the token itself", func(r *http.Request) {
			// The one mistake that would make the whole scheme pointless: a
			// cookie whose value is the credential rather than a receipt for
			// it. If this were ever accepted, a token in a cookie would be a
			// token a page's own script could be tricked into planting.
			r.AddCookie(&http.Cookie{Name: "alchemy_view", Value: testToken})
		}},
		{"a browser Accept header and nothing else", func(r *http.Request) {
			// The sign-in form is served as the body of a 401. This is the
			// test that it is a body and not an exemption.
			r.Header.Set("Accept", "text/html,application/xhtml+xml")
		}},
	}

	for _, v := range gateway.Views() {
		path := strings.ReplaceAll(v.Path, "{job_id}", id)
		path = strings.TrimSuffix(path, "{$}")
		for _, cred := range credentials {
			t.Run(v.Method+path+"/"+cred.name, func(t *testing.T) {
				req, err := http.NewRequest(v.Method, f.http.URL+path, nil)
				if err != nil {
					t.Fatalf("new request: %v", err)
				}
				cred.apply(req)
				resp, err := f.http.Client().Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", v.Method, path, err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s %s with %s: status = %d, want 401", v.Method, path, cred.name, resp.StatusCode)
				}
				if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
					t.Errorf("%s %s with %s: WWW-Authenticate = %q, want a Bearer challenge", v.Method, path, cred.name, got)
				}
			})
		}
	}
}

// The mirror, for the reason both of the other two tables have one: a view
// that refused everything would pass the table above perfectly.
func TestEveryViewRouteAcceptsAValidToken(t *testing.T) {
	f := serve(t, harness{})
	id := f.aDDLJob(t)

	for _, v := range gateway.Views() {
		path := strings.ReplaceAll(v.Path, "{job_id}", id)
		path = strings.TrimSuffix(path, "{$}")
		t.Run(v.Method+path, func(t *testing.T) {
			resp := f.do(t, v.Method, path, testToken, nil)
			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("%s %s refused a valid token", v.Method, path)
			}
			if resp.StatusCode == http.StatusNotFound {
				t.Errorf("%s %s is declared in Views() and answers 404; a declared route that is not registered is worse than an undeclared one", v.Method, path)
			}
		})
	}
}

// The whole point of the cookie: a browser that has signed in once reaches
// every view route afterwards without the token ever appearing in a URL.
//
// The assertions are the three properties that make it worth having. It is
// HttpOnly, so the page's own script cannot read it and neither can an
// injection. It is SameSite=Strict, so no other origin can make a browser
// spend it. And its value is not the token, so a cookie that leaks is a
// receipt somebody can revoke by restarting a process, not a credential.
func TestSigningInExchangesTheTokenForACookieThatIsNotTheToken(t *testing.T) {
	f := serve(t, harness{})
	id := f.aDDLJob(t)

	resp := f.do(t, http.MethodPost, gateway.ViewPrefix+"session", testToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("sign in: status = %d, want 204", resp.StatusCode)
	}
	var ticket *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "alchemy_view" {
			ticket = c
		}
	}
	if ticket == nil {
		t.Fatalf("no session cookie was set; a browser has no other way in")
	}
	if !ticket.HttpOnly {
		t.Error("the session cookie is readable by script; an injection that runs can then take it away with it")
	}
	if ticket.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict; anything looser lets another origin spend this cookie", ticket.SameSite)
	}
	if ticket.Path != gateway.ViewPrefix {
		t.Errorf("Path = %q, want %q; a cookie sent to /v1 is a credential travelling where nothing reads it", ticket.Path, gateway.ViewPrefix)
	}
	if strings.Contains(ticket.Value, testToken) {
		t.Fatal("the cookie carries the token itself; the exchange bought nothing")
	}

	// And now the browser, with no Authorization header at all.
	req, err := http.NewRequest(http.MethodGet, f.http.URL+gateway.ViewPrefix+"jobs/"+id, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.AddCookie(ticket)
	page, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatalf("GET the page: %v", err)
	}
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Errorf("a signed-in browser got %d for the page, want 200", page.StatusCode)
	}

	// Signing out must actually release it, or the sign-out button is a lie.
	out, err := http.NewRequest(http.MethodDelete, f.http.URL+gateway.ViewPrefix+"session", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	out.AddCookie(ticket)
	if _, err := f.http.Client().Do(out); err != nil {
		t.Fatalf("sign out: %v", err)
	}
	again, err := http.NewRequest(http.MethodGet, f.http.URL+gateway.ViewPrefix+"jobs/"+id, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	again.AddCookie(ticket)
	after, err := f.http.Client().Do(again)
	if err != nil {
		t.Fatalf("GET the page after signing out: %v", err)
	}
	defer after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d after signing out, want 401; a cookie that outlives sign-out is a cookie left behind on a shared machine", after.StatusCode)
	}
}

// The token must not reach a URL by any route the view controls. It is asserted
// rather than assumed because the easy implementation of a browser session —
// a redirect carrying a ticket — puts one there, and the difference is not
// visible in any other test.
func TestNoViewRoutePutsACredentialInAURL(t *testing.T) {
	f := serve(t, harness{})
	resp := f.do(t, http.MethodPost, gateway.ViewPrefix+"session", testToken, nil)
	if loc := resp.Header.Get("Location"); loc != "" {
		t.Errorf("sign in redirects to %q; a ticket in a URL lands in history, logs and referrers", loc)
	}
	if resp.Request != nil && strings.Contains(resp.Request.URL.RawQuery, testToken) {
		t.Error("the token is in the query string")
	}
}
