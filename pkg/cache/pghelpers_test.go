package cache_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The database is named by an environment variable rather than assumed, so
// `go test ./...` still passes on a machine with no Postgres — but a shared
// cache that was never pointed at a database has not been tested at all.
const dsnEnv = "ALCHEMY_TEST_POSTGRES"

// pgTest is one test's private schema. Two tests sharing the cache table would
// share content addresses, and an address is global by construction: the whole
// point is that the same question has the same answer everywhere.
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
	p := &pgTest{t: t, dsn: dsn, schema: "cache_test_" + hex.EncodeToString(b[:])}

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
// is how a test gets two nodes over one store. readOnly refuses every write on
// the connection, which is how "a hit does not write" is proved rather than
// asserted.
func (p *pgTest) pool(readOnly bool) *pgxpool.Pool {
	p.t.Helper()
	cfg, err := pgxpool.ParseConfig(p.dsn)
	if err != nil {
		p.t.Fatalf("parse %s: %v", dsnEnv, err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = p.schema
	if readOnly {
		cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	}
	cfg.MaxConns = 8
	cfg.MaxConnLifetime = time.Minute
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		p.t.Fatalf("pool: %v", err)
	}
	p.t.Cleanup(pool.Close)
	return pool
}
