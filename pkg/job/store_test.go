package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// epoch is an arbitrary fixed instant. Every test drives time by hand from
// here: a test that sleeps to prove an expiry is a slow test that fails on a
// loaded CI box, which teaches everyone to re-run it until it passes.
var epoch = time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)

func testStore(t *testing.T, cfg Config) (*Mem, *ManualClock) {
	t.Helper()
	clock := NewManualClock(epoch)
	cfg.Clock = clock
	return New(cfg), clock
}

func TestCreateMintsAnIDAndReportsWhenTheWorkDies(t *testing.T) {
	s, _ := testStore(t, Config{PendingTTL: time.Hour})
	ctx := context.Background()

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
	// §5c: the expiry is reported in the job state, or the "stateless" service
	// grows a database of abandoned work nobody can see the age of.
	if want := epoch.Add(time.Hour); !j.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", j.ExpiresAt, want)
	}

	got, err := s.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != j {
		t.Errorf("get returned %+v, want %+v", got, j)
	}
}

func TestMintedIDsAreDistinct(t *testing.T) {
	s, _ := testStore(t, Config{})
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		j, err := s.Create(context.Background(), "")
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if seen[j.ID] {
			t.Fatalf("minted %q twice", j.ID)
		}
		seen[j.ID] = true
	}
}

func TestGetUnknownIsDistinguishable(t *testing.T) {
	s, _ := testStore(t, Config{})
	_, err := s.Get(context.Background(), "no-such-job")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A retrying client is the reason Create takes an ID at all. A client whose
// UploadSource timed out cannot know whether the job was admitted, and the two
// wrong answers are both expensive: retry blindly and a 10GB dump is imported
// twice, retry never and the work is silently lost.
func TestCreateWithACallerIDIsIdempotent(t *testing.T) {
	s, clock := testStore(t, Config{PendingTTL: time.Hour})
	ctx := context.Background()

	first, err := s.Create(ctx, "nightly-2026-08-29")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	clock.Advance(10 * time.Minute)
	again, err := s.Create(ctx, "nightly-2026-08-29")
	// The error is how a caller who was *not* retrying learns it collided; the
	// job is how a caller who was retrying carries on.
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second create err = %v, want ErrExists", err)
	}
	if again != first {
		t.Errorf("second create returned %+v, want the admitted job %+v", again, first)
	}
}

// A retry must not restart the clock. If it did, a client retrying every
// minute would hold a job open forever and the expiry §5c insists on would
// never fire.
func TestRetryingCreateDoesNotRefreshTheExpiry(t *testing.T) {
	s, clock := testStore(t, Config{PendingTTL: time.Hour})
	ctx := context.Background()
	first, _ := s.Create(context.Background(), "same")
	clock.Advance(30 * time.Minute)
	again, _ := s.Create(ctx, "same")
	if !again.ExpiresAt.Equal(first.ExpiresAt) {
		t.Errorf("ExpiresAt moved from %v to %v on retry", first.ExpiresAt, again.ExpiresAt)
	}
}

// §8.4: admission control, not optimism. The refusal has to be distinguishable
// from a real failure, or a client's retry loop treats "come back in a minute"
// the same as "this will never work".
func TestFullStoreRefusesWithATryLaterACallerCanRecognise(t *testing.T) {
	s, _ := testStore(t, Config{Capacity: 2})
	ctx := context.Background()

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
}

// Capacity is about work the node has to hold, not bytes it has already
// finished with. A finished job waiting to be collected must not wedge the
// queue, or a caller that forgets one Delete takes the node down.
func TestFinishedJobsDoNotCountAgainstCapacity(t *testing.T) {
	s, _ := testStore(t, Config{Capacity: 1})
	ctx := context.Background()
	j, _ := s.Create(ctx, "")
	if err := s.Cancel(ctx, j.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := s.Create(ctx, ""); err != nil {
		t.Fatalf("create after the first finished: %v", err)
	}
}

// A retry of work already admitted is not new work, so a full store must still
// answer it. Refusing here would turn a client's duplicate into a failure at
// exactly the moment the operator is least able to look at it.
func TestRetryIsAnsweredEvenWhenFull(t *testing.T) {
	s, _ := testStore(t, Config{Capacity: 1})
	ctx := context.Background()
	first, _ := s.Create(ctx, "nightly")
	again, err := s.Create(ctx, "nightly")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if again.ID != first.ID {
		t.Errorf("got %q, want the admitted job %q", again.ID, first.ID)
	}
}

// The context is in every signature because a Postgres store will need it.
// Ignoring it here would mean code written and tested against the in-memory
// store meets cancellation for the first time in a cluster.
func TestADeadContextIsRefusedEverywhere(t *testing.T) {
	s, _ := testStore(t, Config{})
	live := context.Background()
	s.Create(live, "only")
	l, _, _ := s.Claim(live, "node-a", time.Minute)

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
	if _, err := s.Expire(dead); !errors.Is(err, context.Canceled) {
		t.Errorf("expire: %v", err)
	}
	if got, _ := s.Get(live, "only"); got.State != alchemy.JobRunning {
		t.Errorf("state = %q, a call on a dead context changed the job", got.State)
	}
}

// A lease that is already dead when it is granted is a job that is claimable
// by two nodes at once from the first instant, which is the one case §8.3's
// "survivable" argument does not cover: nobody was ever slow.
func TestClaimRefusesALeaseThatIsAlreadyOver(t *testing.T) {
	s, _ := testStore(t, Config{})
	ctx := context.Background()
	s.Create(ctx, "only")
	if _, _, err := s.Claim(ctx, "node-a", 0); err == nil {
		t.Fatal("want a refusal for a zero-length lease")
	}
	if got, _ := s.Get(ctx, "only"); got.State != alchemy.JobPending {
		t.Errorf("state = %q, want the job left in the queue", got.State)
	}
}
