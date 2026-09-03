package dgraph

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
)

// envEndpoint names the one environment variable the live tests need. They skip
// without it so that `go test ./...` passes on a machine with no Dgraph — but a
// skipped test proves nothing, so the message says exactly what to set.
const envEndpoint = "ALCHEMY_TEST_DGRAPH"

func liveEndpoint(t *testing.T) string {
	t.Helper()
	e := os.Getenv(envEndpoint)
	if e == "" {
		t.Skipf("no live Dgraph: set %s to an alpha's HTTP address (e.g. %s=http://127.0.0.1:47080) "+
			"to run this test", envEndpoint, envEndpoint)
	}
	return e
}

// liveLoader opens a Loader against the live alpha under a name nobody else
// has.
//
// A prefix per test, not just a run name. Dgraph has one predicate namespace
// per cluster and no way to make a private one from the HTTP API, so two tests
// sharing a prefix share the schema — and a test that altered a predicate would
// alter it for every other test in the package. The run name keeps the DATA
// apart; the prefix keeps the SCHEMA apart, and they are two different
// collisions.
func liveLoader(t *testing.T, o Options) *Loader {
	t.Helper()
	if o.Endpoint == "" {
		o.Endpoint = liveEndpoint(t)
	}
	if o.RunID == "" {
		o.RunID = "ld-" + randomName(t)
	}
	if o.Prefix == "" {
		o.Prefix = "t" + randomName(t) + "_"
	}
	l, err := Open(context.Background(), o)
	if err != nil {
		t.Fatalf("Open(%s): %v", o.Endpoint, err)
	}
	t.Cleanup(func() {
		// The data goes; the predicates stay. Dropping a predicate is a
		// cluster-wide schema change and a test that made one would be
		// reindexing a shared server between cases.
		_ = l.dropLoad(context.Background(), o.RunID)
		_ = l.Close()
	})
	return l
}

func randomName(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}
