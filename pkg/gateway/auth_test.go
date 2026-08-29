package gateway_test

import (
	"net/http"
	"strings"
	"testing"
)

// Authentication has to survive the translation, and the test for it is the
// same shape as pkg/service's: every route, every way of getting the
// credential wrong, one expected answer.
//
// The reason for the shape is the reason it is worth copying. A route added
// later without authentication does not fail somewhere it is obviously wrong;
// it works, for everybody, including the people it was not meant to work for.
// Deriving the table from the generated document (see routes_test.go) is what
// makes "added later" and "covered by this test" the same event.
func TestEveryRouteRefusesAnUnauthenticatedRequest(t *testing.T) {
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
	}

	for _, r := range routes(t) {
		for _, cred := range credentials {
			t.Run(r.rpc+"/"+cred.name, func(t *testing.T) {
				req, err := http.NewRequest(r.method, f.http.URL+r.path(id), strings.NewReader(r.body))
				if err != nil {
					t.Fatalf("new request: %v", err)
				}
				cred.apply(req)
				resp, err := f.http.Client().Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", r.method, r.path(id), err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s %s with %s: status = %d, want 401",
						r.method, r.path(id), cred.name, resp.StatusCode)
				}
			})
		}
	}
}

// The mirror, for the same reason pkg/service has one: a gateway that refused
// everything would pass the test above perfectly.
func TestEveryRouteAcceptsAValidToken(t *testing.T) {
	f := serve(t, harness{})
	id := f.aDDLJob(t)

	for _, r := range routes(t) {
		t.Run(r.rpc, func(t *testing.T) {
			req, err := http.NewRequest(r.method, f.http.URL+r.path(id), strings.NewReader(r.body))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+testToken)
			resp, err := f.http.Client().Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", r.method, r.path(id), err)
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("%s %s refused a valid token", r.method, r.path(id))
			}
		})
	}
}
