package pgvector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// envDSN names the one environment variable that turns this suite on, and it
// is the same variable pkg/job's clustered store uses. A second name for the
// same database would mean a developer who set one up still gets a silent skip
// from the other.
//
// Gated on the DSN rather than on a boolean so that a machine with no database
// still passes `go test ./...`, and a machine that has one needs no second
// variable to say where it is.
const envDSN = "ALCHEMY_TEST_POSTGRES"

// fixture is one private schema on the shared database. Every test gets its
// own so the suite is re-runnable and so two tests in parallel cannot see each
// other's loads — truncating a shared schema would give the first property and
// not the second.
type fixture struct {
	dsn    string
	schema string
	admin  *pgxpool.Pool
}

var identifier = regexp.MustCompile(`[^a-z0-9_]+`)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv(envDSN)
	if dsn == "" {
		t.Skipf("no database: set %s to a Postgres DSN with the vector extension to run the pgvector connector's tests", envDSN)
	}
	var b [6]byte
	rand.Read(b[:])
	// The test name is in the schema so that a leaked schema — a panic between
	// creation and cleanup — says which test leaked it.
	name := identifier.ReplaceAllString(strings.ToLower(t.Name()), "_")
	if len(name) > 40 {
		name = name[:40]
	}
	f := &fixture{dsn: dsn, schema: fmt.Sprintf("t_%s_%s", name, hex.EncodeToString(b[:]))}

	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect %s: %v", envDSN, err)
	}
	f.admin = admin
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+f.schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+f.schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", f.schema, err)
		}
	})
	return f
}

// open returns a migrated loader on this fixture's schema.
func (f *fixture) open(t *testing.T, cfg Config) *Loader {
	t.Helper()
	l := f.openRaw(t, cfg)
	if err := l.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return l
}

// openRaw returns an unmigrated loader, for the tests that are about migration
// itself.
func (f *fixture) openRaw(t *testing.T, cfg Config) *Loader {
	t.Helper()
	cfg.Schema = f.schema
	l, err := Open(context.Background(), f.dsn, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(l.Close)
	return l
}

// scalar reads one value straight out of the database, for the assertions that
// have to look at the table rather than take the loader's word for it.
func (f *fixture) scalar(t *testing.T, dest any, sql string, args ...any) {
	t.Helper()
	sql = strings.ReplaceAll(sql, "{s}", f.schema)
	if err := f.admin.QueryRow(context.Background(), sql, args...).Scan(dest); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
}

func (f *fixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	f.scalar(t, &n, "SELECT count(*) FROM {s}."+table)
	return n
}

// prov builds a full Provenance so that a round-trip test asserts on every
// field rather than on the ones that happened to be easy to set.
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

// smallResult is a graph big enough to have edges and chunks and small enough
// to read in a failure message.
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

// unit is a deterministic embedding: mostly zero with a 1 at a known index, so
// that a nearest-neighbour assertion is arithmetic rather than a guess.
func unit(dim, at int) []float32 {
	v := make([]float32, dim)
	v[at%dim] = 1
	return v
}

// bigResult is a graph large enough that one COPY cannot carry it, so the
// batching and what a failure between batches leaves behind are testable
// rather than argued about.
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
