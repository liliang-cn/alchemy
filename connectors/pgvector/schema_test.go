package pgvector

import (
	"context"
	"strings"
	"testing"
)

// The tables have to exist before anything else is worth testing, and Migrate
// has to be safe to run from every process that starts — the same requirement
// pkg/job's store already meets, for the same reason.
func TestMigrateCreatesTablesAndIsIdempotent(t *testing.T) {
	f := newFixture(t)
	l := f.openRaw(t, Config{})
	ctx := context.Background()
	if err := l.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := l.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	for _, table := range []string{"loads", "chunks", "entities", "relations", "violations", "duplicates"} {
		var n int
		f.scalar(t, &n, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2`, f.schema, table)
		if n != 1 {
			t.Errorf("table %s: found %d, want 1", table, n)
		}
	}
}

// I hit this myself the first time I pointed the connector at a schema that
// was not there, and the message was "CREATE TABLE …loads (: schema does not
// exist" — the failing statement rather than the missing precondition. The
// connector does not create the schema, for the reason pkg/job's store does not
// either: creating one in a buyer's database is a surprise nobody asked for.
// Saying so plainly is the whole of what it owes them.
func TestMigrateSaysWhichSchemaIsMissing(t *testing.T) {
	f := newFixture(t)
	// Built directly rather than through the fixture, which exists to put every
	// test in a schema that does exist.
	l, err := Open(context.Background(), f.dsn, Config{Schema: f.schema + "_absent"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(l.Close)
	err = l.Migrate(context.Background())
	if err == nil {
		t.Fatal("migrating into a schema that does not exist succeeded")
	}
	if !strings.Contains(err.Error(), "CREATE SCHEMA") || !strings.Contains(err.Error(), f.schema+"_absent") {
		t.Errorf("err = %v; it has to name the schema and what to do about it", err)
	}
}

// TestEveryTableALoadOwnsIsOnTheDeletePath derives the child tables from the
// DDL and holds childTables to them.
//
// A table left off that list is not an error anywhere. Delete and Sweep walk
// the list, so its rows survive the load they belong to; the load row itself
// then fails its own DELETE on the foreign key, or -- for a buyer who deletes
// by hand and gets the ON DELETE CASCADE instead -- succeeds while the
// connector's own path does not. Either way the first symptom is a sweep that
// stopped working, months later, on somebody's disk usage graph.
//
// Derived from the DDL rather than written out twice, because a second hand-
// written list is the thing that went wrong the first time.
func TestEveryTableALoadOwnsIsOnTheDeletePath(t *testing.T) {
	l := &Loader{schema: "alchemy"}
	on := map[string]bool{}
	for _, table := range childTables {
		on[table] = true
	}
	for _, stmt := range l.ddl() {
		name, ok := strings.CutPrefix(firstLine(stmt), "CREATE TABLE IF NOT EXISTS alchemy.")
		if !ok {
			continue
		}
		name = strings.TrimSuffix(name, " (")
		if name == "loads" {
			continue // the row the children reference, deleted last and by name
		}
		if !on[name] {
			t.Errorf("%q references loads(id) and is not in childTables, so Delete and Sweep leave its rows "+
				"behind and the load they belong to cannot be deleted at all", name)
		}
		delete(on, name)
	}
	for name := range on {
		t.Errorf("childTables names %q and the DDL creates no such table", name)
	}
}
