package job

import (
	"context"
	"testing"
	"time"
)

// The in-memory store found this bug and paid for it with a name: the fence is
// store-wide, not per job, because a job ID outlives the job.
//
// In Postgres the shape of the mistake is different and easier to make. A
// SEQUENCE is store-wide; `token = token + 1` and `token bigserial` both look
// like the same thing and are per row. This test is written to fail against
// either of those and to pass only for a fence that is one counter for the
// whole store.
//
// It asserts the strong property rather than the weak one. "Two claims of the
// same job get different tokens" is true of a per-row counter, so it is not
// the test; "no two claims anywhere ever share a token, and the numbers only
// go up" is false of a per-row counter on its second row.
func TestTheFenceIsStoreWideAndNotAColumn(t *testing.T) {
	f := newFixture(t)
	s, clock := f.store(t, Config{})
	ctx := context.Background()

	const jobs = 5
	for i := 0; i < jobs; i++ {
		if _, err := s.Create(ctx, string(rune('a'+i))); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	seen := map[uint64]string{}
	var last uint64
	for round := 0; round < 3; round++ {
		// Claim all five before releasing any. Releasing each one as it is
		// claimed would just hand the oldest job straight back and re-claim it,
		// which exercises one row three times and is precisely the case a
		// per-row counter survives.
		var held []Lease
		for i := 0; i < jobs; i++ {
			l, ok, err := s.Claim(ctx, "node-a", time.Minute)
			if err != nil || !ok {
				t.Fatalf("round %d claim %d: ok=%v err=%v", round, i, ok, err)
			}
			if who, dup := seen[l.token]; dup {
				t.Fatalf("token %d issued to %s and again to %s: the fence is per row, "+
					"so a lease for one job is valid for another", l.token, who, l.Job.ID)
			}
			seen[l.token] = l.Job.ID
			if l.token <= last {
				t.Fatalf("token %d after %d: the fence went backwards, so a retired "+
					"lease can become current again", l.token, last)
			}
			last = l.token
			held = append(held, l)
		}
		for _, l := range held {
			if err := s.Release(ctx, l); err != nil {
				t.Fatalf("release: %v", err)
			}
		}
		clock.Advance(time.Second)
	}
	if len(seen) != jobs*3 {
		t.Errorf("%d distinct tokens for %d claims", len(seen), jobs*3)
	}
}

// The fence must survive the thing a counter in a row cannot: the row going
// away. A sequence is a database object of its own, so a job that is reaped
// and recreated under the same ID cannot reset it — which is the exact
// corruption §8.3's "the second writer loses harmlessly" depends on not
// happening.
func TestTheFenceOutlivesTheRowsItFences(t *testing.T) {
	f := newFixture(t)
	s, clock := f.store(t, Config{DoneTTL: time.Minute, PendingTTL: time.Hour})
	ctx := context.Background()

	s.Create(ctx, "nightly")
	zombie, _, _ := s.Claim(ctx, "node-a", time.Minute)
	s.Cancel(ctx, "nightly")
	clock.Advance(2 * time.Minute)
	if swept, _ := s.Expire(ctx); !has(swept.Reaped, "nightly") {
		t.Fatalf("swept = %+v, want the finished job dropped", swept)
	}

	// Every row is gone. A per-row counter has nothing left to remember.
	var rows int
	if err := f.count(t, &rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%d rows left; the table was supposed to be empty", rows)
	}

	s.Create(ctx, "nightly")
	fresh, ok, _ := s.Claim(ctx, "node-a", time.Minute)
	if !ok {
		t.Fatal("the replacement job was not claimable")
	}
	if fresh.token <= zombie.token {
		t.Fatalf("token %d after an empty table, but the reaped job held %d: "+
			"the fence restarted and the zombie's lease is valid again",
			fresh.token, zombie.token)
	}
}
