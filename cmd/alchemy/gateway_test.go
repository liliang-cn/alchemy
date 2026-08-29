package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The gateway is off unless an operator asks for it.
//
// Off is the right default for the same reason -addr is loopback: DESIGN.md §6
// makes gRPC the service and the REST surface a convenience, and a second
// listening port that nobody asked for is a second thing to secure. A buyer
// evaluating the product turns it on with one flag; a deployment that only
// speaks gRPC never has it.
func TestTheGatewayIsOffUnlessAnAddressIsGiven(t *testing.T) {
	s, err := parseFlags(nil, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if s.httpAddr != "" {
		t.Fatalf("httpAddr = %q by default; a port nobody asked for is a port nobody is watching", s.httpAddr)
	}
}

func TestTheGatewayAddressComesFromTheFlagOrTheEnvironment(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"the flag", []string{"-http-addr", "127.0.0.1:6759"}, nil, "127.0.0.1:6759"},
		{"the environment", nil, map[string]string{"ALCHEMY_HTTP_ADDR": "127.0.0.1:43510"}, "127.0.0.1:43510"},
		{
			// The flag wins, for the reason config.go already gives: the flag
			// is on the command that started this process, where the
			// environment may have come from an image somebody else built.
			"the flag over the environment",
			[]string{"-http-addr", "127.0.0.1:6759"},
			map[string]string{"ALCHEMY_HTTP_ADDR": "127.0.0.1:43510"},
			"127.0.0.1:6759",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := parseFlags(c.args, env(c.env), io.Discard)
			if err != nil {
				t.Fatalf("parseFlags: %v", err)
			}
			if s.httpAddr != c.want {
				t.Errorf("httpAddr = %q, want %q", s.httpAddr, c.want)
			}
		})
	}
}

// The wiring test for the gateway, and it is the same kind of test as
// wiring_test.go's: the failure it exists to catch is a component the
// packages provide and the binary forgets to install. A gateway that is built
// and never served, or served without the credential reaching gRPC, is a
// program that starts, prints a green line, and answers a stranger.
func TestTheGatewayServesAndCarriesTheCredential(t *testing.T) {
	s := testSettings(t)
	sv, err := build(s, "the-secret")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + httpLis.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sv.serve(ctx, grpcLis, httpLis) }()

	t.Run("an anonymous caller is refused", func(t *testing.T) {
		if got := httpStatus(t, base+"/v1/jobs/nope", ""); got != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", got)
		}
	})
	t.Run("an authenticated caller reaches the service", func(t *testing.T) {
		// 404 rather than 200 because the job does not exist — which is the
		// point: the answer came from pkg/service, so the credential made it
		// from an HTTP header into gRPC metadata.
		if got := httpStatus(t, base+"/v1/jobs/nope", "the-secret"); got != http.StatusNotFound {
			t.Errorf("status = %d, want 404", got)
		}
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not return after its context was cancelled")
	}
	// Both listeners are shut, not just the one the gRPC server owned.
	if c, err := net.DialTimeout("tcp", httpLis.Addr().String(), 200*time.Millisecond); err == nil {
		c.Close()
		t.Error("the gateway is still accepting after a graceful stop")
	}
}

func httpStatus(t *testing.T, url, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// An operator has to be able to see, in the one line they read, whether the
// REST surface is on and where. "off" is spelled out for the same reason
// cacheLabel spells its zero: a blank is indistinguishable from a bug.
func TestStartupLineSaysWhereTheGatewayIs(t *testing.T) {
	on := startupLine(settings{addr: "127.0.0.1:7431", httpAddr: "127.0.0.1:7432"}, "t")
	if !strings.Contains(on, "http=127.0.0.1:7432") {
		t.Errorf("startup line %q does not say where the gateway is", on)
	}
	off := startupLine(settings{addr: "127.0.0.1:7431"}, "t")
	if !strings.Contains(off, "http=off") {
		t.Errorf("startup line %q does not say the gateway is off", off)
	}
}
