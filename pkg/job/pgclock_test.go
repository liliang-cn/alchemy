package job

import (
	"context"
	"testing"
	"time"
)

// The clock is the decision this store had to make that the in-memory store
// did not: Postgres has a now() of its own, and if deadlines are computed in
// Go and sent as instants, then the answer to "is this lease dead" depends on
// which node asked. These two tests are the decision, written down so it
// cannot be reversed by accident.
//
// The rule: a nil Config.Clock — the production configuration — means every
// deadline and every comparison is evaluated by the database. A non-nil Clock
// is the test path, and the tests below are also the demonstration of what it
// would cost to ship it.

// With no clock injected, node time never enters the SQL. The evidence is that
// the two timestamps in a job are exactly one TTL apart and both agree with
// the database's own now(), which a Go-computed deadline could only manage by
// coincidence.
func TestWithNoClockInjectedTheDatabaseIsTheOnlyClock(t *testing.T) {
	f := newFixture(t)
	s := f.open(t, PGConfig{Config: Config{PendingTTL: 90 * time.Minute}})
	ctx := context.Background()

	before := f.dbNow(t)
	j, err := s.Create(ctx, "only")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	after := f.dbNow(t)

	if j.CreatedAt.Before(before) || j.CreatedAt.After(after) {
		t.Errorf("CreatedAt = %v, outside the database's own [%v, %v]: the row was "+
			"stamped by this node's clock, so two nodes will disagree about it",
			j.CreatedAt, before, after)
	}
	if got := j.ExpiresAt.Sub(j.CreatedAt); got != 90*time.Minute {
		t.Errorf("ExpiresAt - CreatedAt = %v, want the configured TTL exactly; a "+
			"deadline computed separately from the stamp drifts by the round trip", got)
	}
}

// The hazard itself, in one test: a node whose clock runs an hour fast sweeps
// a lease that has fifty-nine minutes left on it.
//
// Both halves are asserted, because the first half is what makes the second
// half worth having. With an injected clock the skewed node steals the job —
// that is not a bug in this store, it is what "the app computes deadlines"
// means. With the production configuration the same node, the same skew and
// the same sweep do nothing, because the only clock in the cluster is the one
// on the other side of the wire.
func TestASkewedNodeDoesNotGetToDecideWhenALeaseDies(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// The node doing the work is honest: it uses the database's clock.
	worker := f.open(t, PGConfig{Config: Config{PendingTTL: time.Hour}})
	// The sweeper's clock is an hour fast. Everything else about it is normal.
	skewed := f.open(t, PGConfig{Config: Config{
		PendingTTL: time.Hour,
		Clock:      NewManualClock(f.dbNow(t).Add(time.Hour)),
	}})
	honest := f.open(t, PGConfig{Config: Config{PendingTTL: time.Hour}})

	if _, err := worker.Create(ctx, "only"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok, err := worker.Claim(ctx, "node-a", time.Minute); !ok || err != nil {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	// The production configuration first: a fresh lease is not touched.
	swept, err := honest.Expire(ctx)
	if err != nil {
		t.Fatalf("honest sweep: %v", err)
	}
	if has(swept.Requeued, "only") {
		t.Fatal("a sweeper on the database's clock requeued a lease with 59 minutes left")
	}

	// And the hazard, so that the choice above is visibly a choice: hand a node
	// its own opinion about the time and it takes the job away.
	swept, err = skewed.Expire(ctx)
	if err != nil {
		t.Fatalf("skewed sweep: %v", err)
	}
	if !has(swept.Requeued, "only") {
		t.Fatal("expected the skewed node to steal the lease; if it no longer can, " +
			"the injected clock has stopped reaching the SQL and the test above " +
			"is proving nothing")
	}
}

// A store built by NewPG must not quietly substitute the wall clock the way
// Mem does. Mem is a single node, where the wall clock and "the store's clock"
// are the same thing; here they are not, and a default that filled the field
// in would put every node back on its own clock without anyone choosing it.
func TestTheClusteredStoreDoesNotDefaultToTheWallClock(t *testing.T) {
	f := newFixture(t)
	s := f.open(t, PGConfig{Config: Config{}})
	if s.cfg.Clock != nil {
		t.Errorf("Clock = %T, want nil so that the database decides", s.cfg.Clock)
	}
	if s.now() != nil {
		t.Errorf("now() = %v, want nil so that coalesce falls through to the database", s.now())
	}
}

// The schema name is the only thing in this store that reaches SQL as text
// rather than as a bound parameter, so it is the only place an injection could
// live. It is refused at construction, once, rather than escaped at each of
// the dozen call sites that interpolate it.
func TestASchemaNameThatIsNotAnIdentifierIsRefused(t *testing.T) {
	for _, bad := range []string{
		`public; DROP TABLE jobs; --`,
		`"public"`,
		`Public`, // the statements are built lowercase; a quoted mixed-case name would not match.
		`1jobs`,
		`jobs-1`,
	} {
		if _, err := NewPG(nil, PGConfig{Schema: bad}); err == nil {
			t.Errorf("schema %q was accepted", bad)
		}
	}
	if _, err := NewPG(nil, PGConfig{Schema: "alchemy_jobs_2"}); err != nil {
		t.Errorf("a perfectly good schema name was refused: %v", err)
	}
}
