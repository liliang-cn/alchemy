package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// The token is not a flag and has no default. A default token is a service
// everyone can reach with a value that is in the source; a flag is a token in
// the process table and in shell history.
func TestTokenComesFromTheEnvironment(t *testing.T) {
	got, err := readToken(settings{}, env(map[string]string{"ALCHEMY_TOKEN": "s3cret"}))
	if err != nil {
		t.Fatalf("readToken: %v", err)
	}
	if got != "s3cret" {
		t.Fatalf("token = %q, want the one in the environment", got)
	}
}

// A file is the better of the two: it is what a secret mount and a systemd
// credential both look like, and it does not survive in a child process.
func TestTokenComesFromAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("  from-a-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readToken(settings{tokenFile: path}, env(nil))
	if err != nil {
		t.Fatalf("readToken: %v", err)
	}
	// Trailing newlines are what a file written by `echo` has, and a token that
	// silently differs from what the operator typed is a whole afternoon.
	if got != "from-a-file" {
		t.Fatalf("token = %q, want the file's contents trimmed", got)
	}
}

// service.New already refuses without a token. This makes the refusal one a
// person can act on: it names the two ways to supply one.
func TestNoTokenIsAnActionableRefusal(t *testing.T) {
	_, err := readToken(settings{}, env(nil))
	if err == nil {
		t.Fatal("readToken accepted a service with no authentication")
	}
	msg := err.Error()
	for _, want := range []string{"ALCHEMY_TOKEN", "-token-file"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not tell the operator about %s", msg, want)
		}
	}
}

// A file that was named and cannot be read is a misconfiguration, not a
// fallback to the environment: silently starting on a different credential
// than the operator mounted is the mistake nobody notices.
func TestAnUnreadableTokenFileIsRefused(t *testing.T) {
	_, err := readToken(settings{tokenFile: filepath.Join(t.TempDir(), "missing")},
		env(map[string]string{"ALCHEMY_TOKEN": "would-have-worked"}))
	if err == nil {
		t.Fatal("readToken fell back to the environment for a named file it could not read")
	}
}

// An empty file is not a token. Without this the service would start with
// authentication that accepts the empty string.
func TestAnEmptyTokenFileIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(settings{tokenFile: path}, env(nil)); err == nil {
		t.Fatal("readToken accepted an empty token file")
	}
}

// Every setting has a flag and an environment variable, because a container
// gets one and a systemd unit gets the other, and an operator should not have
// to pick the deployment style the binary happens to prefer.
func TestFlagsAndEnvironment(t *testing.T) {
	got, err := parseFlags([]string{"-addr", "127.0.0.1:3076", "-capacity", "12"}, env(map[string]string{
		"ALCHEMY_SPOOL":             "/var/spool/alchemy",
		"ALCHEMY_SWEEP":             "30s",
		"ALCHEMY_MODEL_CONCURRENCY": "8",
		"ALCHEMY_TOKEN_FILE":        "/run/secrets/token",
	}), io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.addr != "127.0.0.1:3076" {
		t.Fatalf("addr = %q", got.addr)
	}
	if got.capacity != 12 {
		t.Fatalf("capacity = %d", got.capacity)
	}
	if got.spool != "/var/spool/alchemy" {
		t.Fatalf("spool = %q", got.spool)
	}
	if got.sweepEvery != 30*time.Second {
		t.Fatalf("sweep = %v", got.sweepEvery)
	}
	if got.modelConcurrency != 8 {
		t.Fatalf("model concurrency = %d", got.modelConcurrency)
	}
	if got.tokenFile != "/run/secrets/token" {
		t.Fatalf("token file = %q", got.tokenFile)
	}
}

// A flag is the more specific statement: it is on the command that started
// this process, where the environment may have come from an image built by
// somebody else.
func TestAFlagBeatsTheEnvironment(t *testing.T) {
	got, err := parseFlags([]string{"-addr", "127.0.0.1:6759"},
		env(map[string]string{"ALCHEMY_ADDR": "0.0.0.0:1"}), io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.addr != "127.0.0.1:6759" {
		t.Fatalf("addr = %q, want the flag to win", got.addr)
	}
}

// In-memory is the only store there is (§8.3 designs Postgres and defers it).
// Naming another one is refused rather than quietly ignored: an operator who
// asked for a shared store and got a private one has a cluster whose nodes
// each think they are alone.
func TestAnUnknownStoreIsRefused(t *testing.T) {
	_, err := parseFlags([]string{"-store", "postgres"}, env(nil), io.Discard)
	if err == nil {
		t.Fatal("parseFlags accepted a store that does not exist")
	}
	if !strings.Contains(err.Error(), "memory") {
		t.Fatalf("error %q does not say which store does exist", err)
	}
}

// A malformed number in the environment is a misconfiguration and must stop
// the process. Treating it as zero would silently turn off the budget of §8.2
// or the capacity of §8.4.
func TestAMalformedEnvironmentValueIsRefused(t *testing.T) {
	if _, err := parseFlags(nil, env(map[string]string{"ALCHEMY_CAPACITY": "lots"}), io.Discard); err == nil {
		t.Fatal("parseFlags accepted a non-numeric capacity")
	}
}

// -model-concurrency 0 is an operator turning §8.2's budget off on purpose,
// and it has to be distinguishable from not having said anything. Without the
// distinction the flag could only ever raise the limit, never remove it.
func TestModelConcurrencyCanBeTurnedOffExplicitly(t *testing.T) {
	got, err := parseFlags([]string{"-model-concurrency", "0"}, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.modelConcurrency != 0 {
		t.Fatalf("model concurrency = %d, want 0 (the budget off)", got.modelConcurrency)
	}
	unset, err := parseFlags(nil, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if unset.modelConcurrency != defaultModelConcurrency {
		t.Fatalf("unset model concurrency = %d, want the default %d", unset.modelConcurrency, defaultModelConcurrency)
	}
}
