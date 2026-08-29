package main

import (
	"io"
	"strings"
	"testing"
)

// TestExtractCacheIsConfigurable. §8.2's cache is a deployment decision about
// money, so it is an operator's setting and it obeys the same rule as every
// other one here: a flag, an environment variable, and the flag wins.
func TestExtractCacheIsConfigurable(t *testing.T) {
	got, err := parseFlags([]string{"-extract-cache", "128"}, env(map[string]string{
		"ALCHEMY_EXTRACT_CACHE": "9999",
	}), io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.extractCache != 128 {
		t.Fatalf("extract cache = %d, want the flag to beat the environment", got.extractCache)
	}

	fromEnv, err := parseFlags(nil, env(map[string]string{"ALCHEMY_EXTRACT_CACHE": "512"}), io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if fromEnv.extractCache != 512 {
		t.Fatalf("extract cache = %d, want the environment's 512", fromEnv.extractCache)
	}

	unset, err := parseFlags(nil, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if unset.extractCache != defaultExtractCache {
		t.Fatalf("unset extract cache = %d, want the default %d", unset.extractCache, defaultExtractCache)
	}
}

// TestExtractCacheOffIsNoCacheAtAll. cache.NewMemory(0) is a working Cache that
// stores nothing, and it would be the easy thing to build here. It is the wrong
// thing: the extractor would then consult a store on every chunk, hit the
// interface, miss, and store nothing, which is a per-chunk cost that buys
// nothing and a Get that a shared implementation might one day put on a
// network. Off is expressed by having no cache, the same way §8.2's budget off
// is expressed by having no budget.
func TestExtractCacheOffIsNoCacheAtAll(t *testing.T) {
	for _, n := range []int{0, -1} {
		if c := extractCache(settings{extractCache: n}); c != nil {
			t.Errorf("extractCache(%d) = %v, want nil", n, c)
		}
	}
	if extractCache(settings{extractCache: 16}) == nil {
		t.Error("extractCache(16) = nil, want a cache")
	}
}

// An operator turning the cache off on purpose must be distinguishable from an
// operator who said nothing, or the flag could only ever enlarge it.
func TestExtractCacheCanBeTurnedOffExplicitly(t *testing.T) {
	got, err := parseFlags([]string{"-extract-cache", "0"}, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got.extractCache != 0 {
		t.Fatalf("extract cache = %d, want 0 (the cache off)", got.extractCache)
	}
}

// The startup line is the one line an operator reads to know what they
// started, and a setting that changes the bill belongs in it. §7.2 makes cost
// the number a caller is promised is honest; whether this process is deduping
// its calls is the first thing somebody comparing two invoices needs to know.
func TestTheStartupLineSaysWhetherTheCacheIsOn(t *testing.T) {
	if line := startupLine(settings{addr: "x", extractCache: 4096}, "t"); !strings.Contains(line, "extract-cache=4096") {
		t.Errorf("startup line does not say the cache size: %s", line)
	}
	if line := startupLine(settings{addr: "x", extractCache: 0}, "t"); !strings.Contains(line, "extract-cache=off") {
		t.Errorf("startup line does not say the cache is off: %s", line)
	}
}
