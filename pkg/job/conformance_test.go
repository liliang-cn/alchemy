package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The in-memory store's tests were not written to be shared. They take a *Mem,
// they reach into cfg, live and order, and they compare alchemy.Job values
// with == — which a round trip through a database cannot survive, for reasons
// about time.Time and not about jobs (see sameJob).
//
// So the assertions are restated here against the Store interface, and then
// run against *both* stores. Running the suite against Mem is not ceremony: it
// is the evidence that this transcription still says what the original said.
// A shared suite that only ever ran against the new implementation would be a
// suite the new implementation was allowed to define.
//
// What could not be shared is listed where it is replaced: the two race tests
// (they count a private field), and the defaults test (it reads cfg).

// factory builds a store and the clock that drives its expiry. Every store
// here is driven by a ManualClock for the same reason the in-memory package
// takes one — a test that proves an expiry by sleeping is a test that fails on
// a loaded machine for reasons unrelated to the code. Whether the clustered
// store should use one in production is a different question, answered in
// pgclock_test.go.
type factory func(t *testing.T, cfg Config) (Store, *ManualClock)

func memFactory(t *testing.T, cfg Config) (Store, *ManualClock) {
	t.Helper()
	clock := NewManualClock(epoch)
	cfg.Clock = clock
	return New(cfg), clock
}

func pgFactory(t *testing.T, cfg Config) (Store, *ManualClock) {
	t.Helper()
	f := newFixture(t)
	s, clock := f.store(t, cfg)
	return s, clock
}

func TestMemConformance(t *testing.T) { conformance(t, memFactory) }
func TestPGConformance(t *testing.T)  { conformance(t, pgFactory) }

// conformance is the whole of the in-memory store's observable behaviour.
func conformance(t *testing.T, newStore factory) {
	ctx := context.Background()

	t.Run("CreateMintsAnIDAndReportsWhenTheWorkDies", func(t *testing.T) {
		s, _ := newStore(t, Config{PendingTTL: time.Hour})
		j, err := s.Create(ctx, "")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if j.ID == "" {
			t.Error("a job with no ID cannot be asked about later")
		}
		if j.State != alchemy.JobPending {
			t.Errorf("state = %q, want PENDING", j.State)
		}
		if !j.CreatedAt.Equal(epoch) {
			t.Errorf("CreatedAt = %v, want the store's clock %v", j.CreatedAt, epoch)
		}
		// §5c: the expiry is reported in the job state, or the "stateless"
		// service grows a database of abandoned work nobody can see the age of.
		if want := epoch.Add(time.Hour); !j.ExpiresAt.Equal(want) {
			t.Errorf("ExpiresAt = %v, want %v", j.ExpiresAt, want)
		}
		got, err := s.Get(ctx, j.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !sameJob(got, j) {
			t.Errorf("get returned %+v, want %+v", got, j)
		}
	})

	t.Run("MintedIDsAreDistinct", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		seen := map[string]bool{}
		for i := 0; i < 100; i++ {
			j, err := s.Create(ctx, "")
			if err != nil {
				t.Fatalf("create %d: %v", i, err)
			}
			if seen[j.ID] {
				t.Fatalf("minted %q twice", j.ID)
			}
			seen[j.ID] = true
		}
	})

	t.Run("GetUnknownIsDistinguishable", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		if _, err := s.Get(ctx, "no-such-job"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	// A retrying client is the reason Create takes an ID at all. A client whose
	// UploadSource timed out cannot know whether the job was admitted, and the
	// two wrong answers are both expensive: retry blindly and a 10GB dump is
	// imported twice, retry never and the work is silently lost.
	t.Run("CreateWithACallerIDIsIdempotent", func(t *testing.T) {
		s, clock := newStore(t, Config{PendingTTL: time.Hour})
		first, err := s.Create(ctx, "nightly-2026-08-29")
		if err != nil {
			t.Fatalf("first create: %v", err)
		}
		clock.Advance(10 * time.Minute)
		again, err := s.Create(ctx, "nightly-2026-08-29")
		if !errors.Is(err, ErrExists) {
			t.Fatalf("second create err = %v, want ErrExists", err)
		}
		if !sameJob(again, first) {
			t.Errorf("second create returned %+v, want the admitted job %+v", again, first)
		}
	})

	// A retry must not restart the clock. If it did, a client retrying every
	// minute would hold a job open forever and the expiry §5c insists on would
	// never fire.
	t.Run("RetryingCreateDoesNotRefreshTheExpiry", func(t *testing.T) {
		s, clock := newStore(t, Config{PendingTTL: time.Hour})
		first, _ := s.Create(ctx, "same")
		clock.Advance(30 * time.Minute)
		again, _ := s.Create(ctx, "same")
		if !again.ExpiresAt.Equal(first.ExpiresAt) {
			t.Errorf("ExpiresAt moved from %v to %v on retry", first.ExpiresAt, again.ExpiresAt)
		}
	})

	// §8.4: admission control, not optimism. The refusal has to be
	// distinguishable from a real failure, or a client's retry loop treats
	// "come back in a minute" the same as "this will never work".
	t.Run("FullStoreRefusesWithATryLaterACallerCanRecognise", func(t *testing.T) {
		s, _ := newStore(t, Config{Capacity: 2})
		for i := 0; i < 2; i++ {
			if _, err := s.Create(ctx, ""); err != nil {
				t.Fatalf("create %d: %v", i, err)
			}
		}
		_, err := s.Create(ctx, "one-too-many")
		if !errors.Is(err, ErrAtCapacity) {
			t.Fatalf("err = %v, want ErrAtCapacity", err)
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrExists) {
			t.Errorf("a capacity refusal must not look like %v", err)
		}
		var full *CapacityError
		if !errors.As(err, &full) {
			t.Fatalf("err = %v, want a *CapacityError carrying the numbers", err)
		}
		if full.Capacity != 2 || full.Live != 2 {
			t.Errorf("CapacityError = %+v, want capacity 2 and 2 live", full)
		}
		if _, err := s.Get(ctx, "one-too-many"); !errors.Is(err, ErrNotFound) {
			t.Error("a refused job must not be stored")
		}
	})

	// Capacity is about work the store has to hold, not work it has finished
	// with. A finished job waiting to be collected must not wedge the queue.
	t.Run("FinishedJobsDoNotCountAgainstCapacity", func(t *testing.T) {
		s, _ := newStore(t, Config{Capacity: 1})
		j, _ := s.Create(ctx, "")
		if err := s.Cancel(ctx, j.ID); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if _, err := s.Create(ctx, ""); err != nil {
			t.Fatalf("create after the first finished: %v", err)
		}
	})

	// A retry of work already admitted is not new work, so a full store must
	// still answer it.
	t.Run("RetryIsAnsweredEvenWhenFull", func(t *testing.T) {
		s, _ := newStore(t, Config{Capacity: 1})
		first, _ := s.Create(ctx, "nightly")
		again, err := s.Create(ctx, "nightly")
		if !errors.Is(err, ErrExists) {
			t.Fatalf("err = %v, want ErrExists", err)
		}
		if again.ID != first.ID {
			t.Errorf("got %q, want the admitted job %q", again.ID, first.ID)
		}
	})

	// The context is in every signature because a Postgres store needs it.
	// Ignoring it in the in-memory store would mean code written against it
	// meets cancellation for the first time in a cluster.
	t.Run("ADeadContextIsRefusedEverywhere", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		s.Create(ctx, "only")
		l, _, _ := s.Claim(ctx, "node-a", time.Minute)

		dead, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := s.Create(dead, ""); !errors.Is(err, context.Canceled) {
			t.Errorf("create: %v", err)
		}
		if _, err := s.Get(dead, "only"); !errors.Is(err, context.Canceled) {
			t.Errorf("get: %v", err)
		}
		if _, _, err := s.Claim(dead, "node-b", time.Minute); !errors.Is(err, context.Canceled) {
			t.Errorf("claim: %v", err)
		}
		if _, err := s.Heartbeat(dead, l, ""); !errors.Is(err, context.Canceled) {
			t.Errorf("heartbeat: %v", err)
		}
		if err := s.Transition(dead, l, alchemy.JobSucceeded); !errors.Is(err, context.Canceled) {
			t.Errorf("transition: %v", err)
		}
		if err := s.Fail(dead, l, "cause"); !errors.Is(err, context.Canceled) {
			t.Errorf("fail: %v", err)
		}
		if err := s.Release(dead, l); !errors.Is(err, context.Canceled) {
			t.Errorf("release: %v", err)
		}
		if err := s.Hold(dead, l, HoldConflict); !errors.Is(err, context.Canceled) {
			t.Errorf("hold: %v", err)
		}
		if err := s.Cancel(dead, "only"); !errors.Is(err, context.Canceled) {
			t.Errorf("cancel: %v", err)
		}
		if err := s.Resolve(dead, "only", alchemy.JobSucceeded); !errors.Is(err, context.Canceled) {
			t.Errorf("resolve: %v", err)
		}
		if err := s.Delete(dead, "only"); !errors.Is(err, context.Canceled) {
			t.Errorf("delete: %v", err)
		}
		if _, err := s.Expire(dead); !errors.Is(err, context.Canceled) {
			t.Errorf("expire: %v", err)
		}
		if got, _ := s.Get(ctx, "only"); got.State != alchemy.JobRunning {
			t.Errorf("state = %q, a call on a dead context changed the job", got.State)
		}
	})

	// A lease that is already dead when it is granted is a job that is
	// claimable by two nodes at once from the first instant, which is the one
	// case §8.3's "survivable" argument does not cover: nobody was ever slow.
	t.Run("ClaimRefusesALeaseThatIsAlreadyOver", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		s.Create(ctx, "only")
		if _, _, err := s.Claim(ctx, "node-a", 0); err == nil {
			t.Fatal("want a refusal for a zero-length lease")
		}
		if got, _ := s.Get(ctx, "only"); got.State != alchemy.JobPending {
			t.Errorf("state = %q, want the job left in the queue", got.State)
		}
	})

	t.Run("DeleteRemovesAJobAndSaysSoTwice", func(t *testing.T) {
		s, _ := newStore(t, Config{})
		s.Create(ctx, "only")
		if err := s.Delete(ctx, "only"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := s.Get(ctx, "only"); !errors.Is(err, ErrNotFound) {
			t.Errorf("get after delete = %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, "only"); !errors.Is(err, ErrNotFound) {
			t.Errorf("second delete = %v, want ErrNotFound", err)
		}
	})

	// Deleting live work frees the capacity it was holding, or a caller that
	// tidies up its own queue still finds the store full.
	t.Run("DeletingLiveWorkFreesItsCapacity", func(t *testing.T) {
		s, _ := newStore(t, Config{Capacity: 1})
		s.Create(ctx, "only")
		if _, err := s.Create(ctx, "second"); !errors.Is(err, ErrAtCapacity) {
			t.Fatalf("err = %v, want the store to be full", err)
		}
		if err := s.Delete(ctx, "only"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := s.Create(ctx, "second"); err != nil {
			t.Errorf("create after delete: %v", err)
		}
	})

	conformanceLease(t, newStore)
	conformanceSelfTransition(t, newStore)
	conformanceReview(t, newStore)
	conformanceSweep(t, newStore)
}
