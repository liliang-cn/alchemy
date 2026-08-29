package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"

	"github.com/jackc/pgx/v5/pgxpool"
)

// envDSN names the one environment variable that turns this suite on.
//
// Gated rather than deleted, and gated on the DSN itself rather than on a
// boolean: a machine with no database must still pass `go test ./...`, and a
// machine that has one must not need a second variable to say where it is.
// The skip message names the variable because a developer who does not know
// this suite exists is exactly the person who will see the skip.
const envDSN = "ALCHEMY_TEST_POSTGRES"

// fixture is one private schema on the shared database, plus the pools opened
// against it. Every test gets its own schema so the suite is re-runnable and
// so two tests running in parallel cannot see each other's jobs — truncation
// would give the first property but not the second.
type fixture struct {
	dsn    string
	schema string
}

var identifier = regexp.MustCompile(`[^a-z0-9_]+`)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv(envDSN)
	if dsn == "" {
		t.Skipf("no database: set %s to a Postgres DSN to run the clustered store's tests", envDSN)
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

// open returns a Store on this fixture's schema with its own connection pool.
// Its own pool is the point: a "second node" that shares the first node's pool
// shares its transactions' visibility in ways a second process would not.
func (f *fixture) open(t *testing.T, cfg PGConfig) *PG {
	t.Helper()
	cfg.Schema = f.schema
	p, err := OpenPG(context.Background(), f.dsn, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(p.Close)
	if err := p.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return p
}

// store is the common case: one node, one schema, a manual clock so that
// expiry is proved instantly rather than by sleeping.
func (f *fixture) store(t *testing.T, cfg Config) (*PG, *ManualClock) {
	t.Helper()
	clock := NewManualClock(epoch)
	cfg.Clock = clock
	return f.open(t, PGConfig{Config: cfg}), clock
}

// sameJob compares two jobs the way a caller cares about.
//
// The in-memory tests use ==, which a round trip through Postgres cannot
// survive: time.Time's == compares the monotonic reading and the *Location
// pointer as well as the instant, and a timestamptz read back from the
// database has neither the monotonic clock the Go value was born with nor the
// same Location. That is a property of the transport, not a disagreement about
// what time it is, so the shared assertions compare instants.
func sameJob(a, b alchemy.Job) bool {
	return a.ID == b.ID && a.State == b.State && a.Stage == b.Stage && a.Error == b.Error &&
		a.CreatedAt.Equal(b.CreatedAt) && a.ExpiresAt.Equal(b.ExpiresAt)
}

// count reports how many job rows this fixture's schema holds, for the tests
// that care that the table is empty rather than that the store says it is.
func (f *fixture) count(t *testing.T, into *int) error {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), f.dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	return pool.QueryRow(context.Background(), "SELECT count(*) FROM "+f.schema+".jobs").Scan(into)
}

// dbNow is the database's own clock, read directly, for the tests that have to
// compare against it rather than take the store's word for it.
func (f *fixture) dbNow(t *testing.T) time.Time {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), f.dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	var now time.Time
	if err := pool.QueryRow(context.Background(), "SELECT now()").Scan(&now); err != nil {
		t.Fatalf("select now(): %v", err)
	}
	return now.UTC()
}
