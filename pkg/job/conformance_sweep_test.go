package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func conformanceSweep(t *testing.T, newStore factory) {
	ctx := context.Background()

	// The two timers again, this time as the sweep sees them: one pass of one
	// clock, and the review hold dies while the conflict hold is still waiting
	// for the person who has to answer it.
	t.Run("TheSweepHonoursBothHoldTimers", func(t *testing.T) {
		s, clock := newStore(t, Config{ReviewTTL: time.Hour, ConflictTTL: 72 * time.Hour})
		hold(t, s, "review", HoldReview)
		hold(t, s, "conflict", HoldConflict)

		clock.Advance(90 * time.Minute)
		swept, err := s.Expire(ctx)
		if err != nil {
			t.Fatalf("expire: %v", err)
		}
		if !has(swept.Expired, "review") {
			t.Errorf("expired = %v, want the review hold", swept.Expired)
		}
		if has(swept.Expired, "conflict") {
			t.Error("a job blocked on a real question did not survive 90 minutes")
		}
		if got, _ := s.Get(ctx, "review"); got.State != alchemy.JobExpired {
			t.Errorf("review state = %q, want EXPIRED", got.State)
		}

		clock.Advance(72 * time.Hour)
		swept, _ = s.Expire(ctx)
		if !has(swept.Expired, "conflict") {
			t.Errorf("expired = %v, want the conflict hold eventually", swept.Expired)
		}
	})

	// §5c: queued work nobody claimed ages out too, or the burst nobody had
	// capacity for is still occupying the store tomorrow.
	t.Run("QueuedWorkNobodyClaimedExpires", func(t *testing.T) {
		s, clock := newStore(t, Config{PendingTTL: time.Hour, Capacity: 1})
		s.Create(ctx, "queued")
		if swept, _ := s.Expire(ctx); len(swept.Expired) != 0 {
			t.Fatalf("expired %v before the timer ran out", swept.Expired)
		}
		clock.Advance(time.Hour)
		if swept, _ := s.Expire(ctx); !has(swept.Expired, "queued") {
			t.Fatalf("expired = %v, want the queued job", swept.Expired)
		}
		// Expiry frees the capacity it was holding, or a store that expires
		// everything still refuses everything.
		if _, err := s.Create(ctx, "next"); err != nil {
			t.Errorf("create after an expiry: %v", err)
		}
	})

	// A node that died does not take the job with it (§8.3). The sweep is the
	// path that matters in an idle cluster: nobody is asking for work, so
	// nobody would discover the dead lease by trying to claim it.
	t.Run("ADeadLeaseIsRequeuedRatherThanExpired", func(t *testing.T) {
		s, clock := newStore(t, Config{PendingTTL: time.Hour})
		s.Create(ctx, "only")
		dead, _, _ := s.Claim(ctx, "node-a", time.Minute)

		clock.Advance(2 * time.Minute)
		swept, _ := s.Expire(ctx)
		if !has(swept.Requeued, "only") {
			t.Fatalf("requeued = %v, want the abandoned job", swept.Requeued)
		}
		if has(swept.Expired, "only") {
			t.Error("work whose node died was discarded rather than retried")
		}
		got, _ := s.Get(ctx, "only")
		if got.State != alchemy.JobPending {
			t.Errorf("state = %q, want PENDING", got.State)
		}
		if want := epoch.Add(2*time.Minute + time.Hour); !got.ExpiresAt.Equal(want) {
			t.Errorf("requeued ExpiresAt = %v, want a fresh pending timer %v", got.ExpiresAt, want)
		}
		// The node that was presumed dead may still be alive and slow.
		if err := s.Transition(ctx, dead, alchemy.JobSucceeded); !errors.Is(err, ErrLeaseLost) {
			t.Errorf("the requeued job accepted a write from the old lease: %v", err)
		}
	})

	// The print queue must not become a filesystem by the slow route: a caller
	// that collects its result and forgets to Delete, forever.
	t.Run("FinishedWorkIsDroppedAfterItsRetention", func(t *testing.T) {
		s, clock := newStore(t, Config{DoneTTL: 30 * time.Minute})
		s.Create(ctx, "done")
		if err := s.Cancel(ctx, "done"); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		clock.Advance(29 * time.Minute)
		if swept, _ := s.Expire(ctx); len(swept.Reaped) != 0 {
			t.Fatalf("reaped %v while it was still collectable", swept.Reaped)
		}
		clock.Advance(time.Minute)
		swept, _ := s.Expire(ctx)
		if !has(swept.Reaped, "done") {
			t.Fatalf("reaped = %v, want the finished job", swept.Reaped)
		}
		if _, err := s.Get(ctx, "done"); !errors.Is(err, ErrNotFound) {
			t.Errorf("get after retention = %v, want ErrNotFound", err)
		}
		// Reaping is not expiry: a job that finished must never be reported as
		// having been abandoned.
		if has(swept.Expired, "done") {
			t.Error("a collected job was reported as expired")
		}
	})

	// An expired job is terminal, so the sweep must not keep re-reporting it. A
	// sweeper that emits the same ID every minute is a sweeper whose output is
	// ignored.
	t.Run("TheSweepReportsEachJobOnce", func(t *testing.T) {
		s, clock := newStore(t, Config{PendingTTL: time.Minute, DoneTTL: time.Hour})
		s.Create(ctx, "queued")
		clock.Advance(time.Minute)

		if swept, _ := s.Expire(ctx); len(swept.Expired) != 1 {
			t.Fatalf("first sweep expired %v", swept.Expired)
		}
		if swept, _ := s.Expire(ctx); len(swept.Expired) != 0 {
			t.Fatalf("second sweep expired %v again", swept.Expired)
		}
	})

	// One pass must not undo its own work. A requeued lease becomes PENDING
	// with a fresh timer, and an expired hold becomes a terminal job with a
	// fresh retention: a sweep whose three statements ran in the wrong order
	// would expire the job it had just requeued.
	t.Run("OnePassDoesNotSweepItsOwnOutput", func(t *testing.T) {
		s, clock := newStore(t, Config{PendingTTL: time.Hour, DoneTTL: time.Hour, ReviewTTL: time.Minute})
		s.Create(ctx, "requeued")
		s.Claim(ctx, "node-a", time.Minute)
		hold(t, s, "expiring", HoldReview)

		clock.Advance(2 * time.Minute)
		swept, err := s.Expire(ctx)
		if err != nil {
			t.Fatalf("expire: %v", err)
		}
		if !has(swept.Requeued, "requeued") || !has(swept.Expired, "expiring") {
			t.Fatalf("swept = %+v, want one requeue and one expiry", swept)
		}
		if has(swept.Expired, "requeued") || has(swept.Reaped, "expiring") {
			t.Errorf("swept = %+v, one pass swept its own output", swept)
		}
		if got, _ := s.Get(ctx, "requeued"); got.State != alchemy.JobPending {
			t.Errorf("state = %q, want PENDING", got.State)
		}
		if got, _ := s.Get(ctx, "expiring"); got.State != alchemy.JobExpired {
			t.Errorf("state = %q, want EXPIRED", got.State)
		}
	})
}
