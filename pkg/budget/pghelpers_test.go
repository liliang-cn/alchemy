package budget_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The database is named by an environment variable rather than assumed. A
// shared budget that was never pointed at a database is a guess with a
// compiler's approval, so these tests are real; but `go test ./...` on a
// machine with no Postgres must still pass, so they skip loudly instead of
// failing.
const dsnEnv = "ALCHEMY_TEST_POSTGRES"

// pgTest is one test's private schema in the shared database. Every test gets
// its own, because these tables are cluster state: two tests sharing them would
// be two nodes disagreeing about a budget, which is a real failure mode but not
// the one under test.
type pgTest struct {
	t      *testing.T
	dsn    string
	schema string
}

func newPG(t *testing.T) *pgTest {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("set %s to a Postgres DSN to run the shared-store tests", dsnEnv)
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	p := &pgTest{t: t, dsn: dsn, schema: "budget_test_" + hex.EncodeToString(b[:])}

	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to %s: %v", dsnEnv, err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+p.schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		drop, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return
		}
		defer drop.Close()
		_, _ = drop.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+p.schema+" CASCADE")
	})
	return p
}

// pool returns a connection pool pinned to the test's schema. Calling it twice
// is how a test gets two nodes: separate pools, separate in-process state, one
// database.
func (p *pgTest) pool() *pgxpool.Pool {
	p.t.Helper()
	cfg, err := pgxpool.ParseConfig(p.dsn)
	if err != nil {
		p.t.Fatalf("parse %s: %v", dsnEnv, err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = p.schema
	// Room for the listener's held connection plus the waiters' short
	// transactions; a pool of one would deadlock the moment a listener existed.
	cfg.MaxConns = 12
	cfg.MaxConnLifetime = time.Minute
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		p.t.Fatalf("pool: %v", err)
	}
	p.t.Cleanup(pool.Close)
	return pool
}
