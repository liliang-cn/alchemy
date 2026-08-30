package qdrant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// envURL names the one environment variable that turns this suite on. It holds
// the REST base URL rather than a boolean so that a machine with a Qdrant
// needs no second variable to say where it is, and a machine without one still
// passes `go test ./...` instead of failing on somebody else's infrastructure.
const envURL = "ALCHEMY_TEST_QDRANT"

// fixture is one collection on the shared server. Every test gets its own,
// because Qdrant has no schema-like namespace inside a collection and two
// tests sharing one would see each other's points; a per-test collection is
// also what makes the suite re-runnable after a crash.
type fixture struct {
	url        string
	collection string
}

var identifier = regexp.MustCompile(`[^a-z0-9_]+`)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	url := os.Getenv(envURL)
	if url == "" {
		t.Skipf("no server: set %s to a Qdrant REST URL (e.g. http://host:6333) to run the qdrant connector's tests", envURL)
	}
	var b [6]byte
	rand.Read(b[:])
	// The test's name is in the collection name so that a collection leaked by
	// a panic between creation and cleanup says which test leaked it.
	name := identifier.ReplaceAllString(strings.ToLower(t.Name()), "_")
	if len(name) > 40 {
		name = name[:40]
	}
	f := &fixture{url: url, collection: fmt.Sprintf("t_%s_%s", name, hex.EncodeToString(b[:]))}
	t.Cleanup(func() {
		l, err := Open(context.Background(), f.url, Config{Collection: f.collection})
		if err != nil {
			t.Errorf("cleanup: open: %v", err)
			return
		}
		if err := l.DropCollection(context.Background()); err != nil {
			t.Errorf("cleanup: dropping %s: %v", f.collection, err)
		}
	})
	return f
}

// open returns a loader whose collection exists.
func (f *fixture) open(t *testing.T, cfg Config) *Loader {
	t.Helper()
	l := f.openRaw(t, cfg)
	if err := l.EnsureCollection(context.Background()); err != nil {
		t.Fatalf("ensure collection: %v", err)
	}
	return l
}

// openRaw returns a loader on a collection that may not exist yet, for the
// tests that are about creating it.
func (f *fixture) openRaw(t *testing.T, cfg Config) *Loader {
	t.Helper()
	cfg.Collection = f.collection
	l, err := Open(context.Background(), f.url, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return l
}

// prov builds a full Provenance, every field set, so that a round-trip
// assertion covers the fields that are easy to lose rather than the ones that
// were easy to fill in.
func prov(chunk int) alchemy.Provenance {
	return alchemy.Provenance{
		Source:     "architecture.pdf",
		Chunk:      chunk,
		Producer:   alchemy.ProducerLLMExtract,
		Model:      "gemini-3.6-flash-high",
		Ontology:   "sds@3",
		Chunking:   "semantic",
		Confidence: 0.82,
		ReviewedBy: "ada@example.com",
		RuleSet:    "rs-9f21",
		RuledBy:    "authored/type:Service",
	}
}

// smallResult is a graph with edges, chunks and vectors, small enough to read
// in a failure message.
func smallResult(dim int) alchemy.Result {
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "SuperAI", Type: "Service", Name: "SuperAI", Attributes: map[string]any{"lang": "go"}, Provenance: prov(0)},
			{ID: "CortexDB", Type: "Store", Name: "CortexDB", Provenance: prov(1)},
		},
		Relations: []alchemy.Relation{
			{From: "SuperAI", To: "CortexDB", Type: "USES", Attributes: map[string]any{"since": "2025"}, Provenance: prov(1)},
		},
		Chunks: []alchemy.Chunk{
			{Index: 0, Text: "SuperAI is a service.", Source: "architecture.pdf", Strategy: "semantic", Heading: "Overview", Start: 0, End: 21},
			{Index: 1, Text: "SuperAI uses CortexDB.", Source: "architecture.pdf", Strategy: "semantic", Heading: "Stores", Start: 21, End: 43},
		},
		Counts: alchemy.Counts{Entities: 2, Relations: 1, Inferred: 3},
	}
	for i := range res.Chunks {
		res.Vectors = append(res.Vectors, alchemy.Vector{Chunk: i, Values: unit(dim, i), Model: "embed-4"})
	}
	return res
}

// unit is a deterministic embedding: zero everywhere but one axis, so that a
// nearest-neighbour assertion is arithmetic rather than a hope.
func unit(dim, at int) []float32 {
	v := make([]float32, dim)
	v[at%dim] = 1
	return v
}

// bigResult is a graph too large for one upsert, so that the batching and what
// a failure between batches leaves behind are testable rather than argued
// about.
func bigResult(n, dim int) alchemy.Result {
	res := alchemy.Result{Counts: alchemy.Counts{Entities: n, Relations: n - 1}}
	for i := range n {
		id := fmt.Sprintf("e%05d", i)
		res.Entities = append(res.Entities, alchemy.Entity{
			ID: id, Type: "Service", Name: id, Provenance: prov(i),
		})
		res.Chunks = append(res.Chunks, alchemy.Chunk{
			Index: i, Text: fmt.Sprintf("chunk %d talks about %s", i, id),
			Source: "big.pdf", Strategy: "fixed", Start: i * 32, End: i*32 + 32,
		})
		res.Vectors = append(res.Vectors, alchemy.Vector{Chunk: i, Values: unit(dim, i), Model: "embed-4"})
		if i > 0 {
			res.Relations = append(res.Relations, alchemy.Relation{
				From: fmt.Sprintf("e%05d", i-1), To: id, Type: "CALLS", Provenance: prov(i),
			})
		}
	}
	return res
}

// pointsOf counts every point of one load, complete or not. It reads past the
// visibility rule on purpose: a test about what a broken load leaves behind
// cannot use the mechanism that hides it.
func (f *fixture) pointsOf(t *testing.T, l *Loader, id string) int {
	t.Helper()
	pts, err := l.scroll(context.Background(),
		map[string]any{"must": []map[string]any{match(keyLoad, id)}}, 0)
	if err != nil {
		t.Fatalf("scroll: %v", err)
	}
	return len(pts)
}
