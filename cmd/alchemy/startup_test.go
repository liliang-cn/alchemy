package main

import (
	"strings"
	"testing"
	"time"
)

// What a person needs to see at startup, and the one thing they must never.
func TestStartupLineSaysWhatMattersAndNotTheToken(t *testing.T) {
	s := settings{
		addr: "127.0.0.1:3076", spool: "/var/spool/alchemy",
		capacity: 12, sweepEvery: 30 * time.Second, modelConcurrency: 4,
	}
	line := startupLine(s, "s3cret-token")

	for _, want := range []string{"127.0.0.1:3076", "/var/spool/alchemy", "capacity=12", "auth=on"} {
		if !strings.Contains(line, want) {
			t.Fatalf("startup line %q is missing %q", line, want)
		}
	}
	// The whole reason this is a function with a return value rather than a
	// few log calls: a test can hold the entire line and assert the credential
	// is not in it.
	if strings.Contains(line, "s3cret-token") {
		t.Fatalf("the startup line logged the token: %q", line)
	}
}

// A budget of zero is a real configuration — a single node with no declared
// endpoint limit — and the operator has to be able to tell it apart from a
// budget that is on.
func TestStartupLineSaysWhenTheBudgetIsOff(t *testing.T) {
	line := startupLine(settings{addr: "x", modelConcurrency: 0}, "t")
	if !strings.Contains(line, "model-concurrency=unlimited") {
		t.Fatalf("startup line %q does not say the model budget is off", line)
	}
}
