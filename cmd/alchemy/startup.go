package main

import (
	"fmt"
	"net"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/gateway"
)

// startupLine is the one line an operator reads to know what they started.
//
// It is a function returning a string rather than a handful of log calls for
// one reason: a test can hold the whole line and assert that the credential is
// not anywhere in it. A token that reaches a log reaches every place logs are
// shipped to, and it is still valid when it gets there.
//
// The token is taken as an argument and deliberately not printed. Passing it
// in is what lets the line say auth=on truthfully — the alternative, a boolean
// computed by the caller, is a claim about authentication made by something
// that never saw the credential.
func startupLine(s settings, token string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "alchemy listening on %s", s.addr)
	fmt.Fprintf(&b, " http=%s", httpLabel(s.httpAddr))
	fmt.Fprintf(&b, " ui=%s", uiLabel(s.httpAddr))
	fmt.Fprintf(&b, " spool=%s", spoolLabel(s.spool))
	fmt.Fprintf(&b, " store=%s capacity=%d sweep=%s", storeMemory, s.capacity, s.sweepEvery)
	fmt.Fprintf(&b, " model-concurrency=%s", concurrencyLabel(s.modelConcurrency))
	fmt.Fprintf(&b, " extract-cache=%s", cacheLabel(s.extractCache))
	fmt.Fprintf(&b, " rules=%s", rulesLabel(s.rules))
	// The only thing said about the token is that there is one.
	fmt.Fprintf(&b, " auth=%s", authLabel(token))
	return b.String()
}

// httpLabel spells the empty address out. A blank after "http=" reads as a
// truncated log line rather than as a decision, and the decision — that the
// REST surface is not listening — is one an operator debugging a refused
// connection needs to see stated.
func httpLabel(addr string) string {
	if addr == "" {
		return "off"
	}
	return addr
}

// uiLabel is the URL a person opens.
//
// It is a whole URL rather than a path because the line is read by somebody
// who is about to paste it somewhere, and a path is the half of the answer
// they already had. A wildcard bind is rewritten to loopback for the reason
// dialTarget rewrites it: 0.0.0.0 is an address to listen on and not one to
// open, and a startup line that printed http://0.0.0.0:6759/ui/ would be
// handing an operator a URL that fails on the platforms where it is not
// routable — which reads as a broken view rather than as an address that was
// never meant to be typed.
func uiLabel(addr string) string {
	if addr == "" {
		return "off"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr + gateway.ViewPrefix
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + gateway.ViewPrefix
}

func spoolLabel(dir string) string {
	if dir == "" {
		return "(temporary directory)"
	}
	return dir
}

// concurrencyLabel spells out the zero. §8.2's budget being off is a real
// configuration — one node with no declared endpoint limit — and "0" reads
// like "no calls allowed" to the person who has to decide whether that is what
// they meant.
func concurrencyLabel(n int) string {
	if n <= 0 {
		return "unlimited"
	}
	return fmt.Sprint(n)
}

// cacheLabel spells the zero out too, and spells it "off" rather than
// "unlimited" — the opposite of concurrencyLabel, because the two zeroes mean
// opposite things. A budget of zero is no ceiling; a cache of zero is no cache.
// Printing "0" for both would let one glance answer the wrong question.
func cacheLabel(n int) string {
	if n <= 0 {
		return "off"
	}
	return fmt.Sprint(n)
}

// rulesLabel says how much standing policy is in force. It is on the startup
// line because a rule set changes what a graph contains — it can drop records
// (§5c's `reject`) — and that is not something an operator should have to read
// a configuration file to discover. Zero is spelled out for the same reason
// the other zeroes are: "0" reads as a truncated line rather than a decision.
func rulesLabel(n int) string {
	if n <= 0 {
		return "off"
	}
	return fmt.Sprint(n)
}

// authLabel exists so that "on" is derived from the credential rather than
// asserted next to it. service.New refuses an empty token, so off is
// unreachable — which is the point: the line cannot claim authentication that
// is not there.
func authLabel(token string) string {
	if token == "" {
		return "off"
	}
	return "on"
}
