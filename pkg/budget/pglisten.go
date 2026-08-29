package budget

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgListener turns Postgres notifications into wakeups for the goroutines
// parked in Acquire.
//
// It exists because the alternative scales the wrong way. A waiter could hold
// its own listening connection, but a node running sixty-four chunk workers
// would then hold sixty-four connections doing nothing but waiting — and a
// pool sized for the transactions would deadlock the first time it tried. One
// connection per process, fanned out in memory, is the same information at a
// sixty-fourth of the cost.
//
// It is also, deliberately, only an optimisation. Every waiter polls as well,
// because a slot reclaimed from a dead node changes the answer without anybody
// sending a notification, and because a listener whose connection has dropped
// must degrade to "slower" rather than to "never". Nothing here reports an
// error upward for that reason: the correctness path is the poll.
type pgListener struct {
	pool    *pgxpool.Pool
	channel string

	mu sync.Mutex
	// waiting holds one channel per model, closed and replaced on every
	// notification. Closing is the broadcast: every waiter that took the
	// channel before the notification sees it, and one that arrives after
	// takes a fresh one and misses nothing.
	waiting map[string]chan struct{}

	cancel context.CancelFunc
	done   chan struct{}
}

// listenerRetry is how long the listener waits before rebuilding a connection
// that failed. It is longer than a poll interval on purpose: while the listener
// is down the waiters are still polling, so reconnecting in a tight loop would
// spend connections to shorten a latency nobody is waiting on.
const listenerRetry = time.Second

func newPGListener(pool *pgxpool.Pool, channel string) *pgListener {
	ctx, cancel := context.WithCancel(context.Background())
	l := &pgListener{
		pool:    pool,
		channel: channel,
		waiting: map[string]chan struct{}{},
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go l.run(ctx)
	return l
}

// watch returns the channel that closes when something about model changes. It
// must be called before the caller asks the store, or a release landing between
// the answer and the wait is a wakeup slept through.
func (l *pgListener) watch(model string) <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.waiting[model]
	if !ok {
		ch = make(chan struct{})
		l.waiting[model] = ch
	}
	return ch
}

func (l *pgListener) wake(model string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ch, ok := l.waiting[model]; ok {
		close(ch)
		delete(l.waiting, model)
	}
}

func (l *pgListener) close() {
	l.cancel()
	<-l.done
}

// run holds one connection outside the pool and republishes what arrives on it.
//
// The connection is its own rather than borrowed from the pool for two reasons:
// a connection parked in WaitForNotification is a connection the pool cannot
// hand to a waiter's transaction, and a connection that has run LISTEN is not
// clean to give back.
func (l *pgListener) run(ctx context.Context) {
	defer close(l.done)
	for ctx.Err() == nil {
		l.session(ctx)
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(listenerRetry)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

func (l *pgListener) session(ctx context.Context) {
	cfg := l.pool.Config().ConnConfig.Copy()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return
	}
	defer func() {
		// Its own context: the session usually ends because ctx was cancelled,
		// and a close that needed a live context would leak the connection
		// exactly then.
		closing, cancel := context.WithTimeout(context.WithoutCancel(ctx), listenerRetry)
		defer cancel()
		_ = conn.Close(closing)
	}()

	if _, err := conn.Exec(ctx, "LISTEN "+l.channel); err != nil {
		return
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return
		}
		l.wake(n.Payload)
	}
}
