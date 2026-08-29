package main

import (
	"fmt"
	"strings"
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
	fmt.Fprintf(&b, " spool=%s", spoolLabel(s.spool))
	fmt.Fprintf(&b, " store=%s capacity=%d sweep=%s", storeMemory, s.capacity, s.sweepEvery)
	fmt.Fprintf(&b, " model-concurrency=%s", concurrencyLabel(s.modelConcurrency))
	fmt.Fprintf(&b, " extract-cache=%s", cacheLabel(s.extractCache))
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
