// Package service is DESIGN.md §6: "gRPC is the service. Anything else is a
// gateway in front of it."
//
// It owns no analysis. Everything it knows about a graph it learned from
// pkg/verify and pkg/review, everything it knows about a job's lifetime it
// learned from pkg/job, and the work itself belongs to a Runner it declares
// and does not import. What is left is the part a wire protocol is actually
// for: deciding which failures are the caller's fault, holding work in
// progress without becoming a database, and making sure a person's decision
// reaches work that has not run yet.
package service

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/liliang-cn/alchemy/pkg/job"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// Config is the operator's half of this package.
type Config struct {
	// Runner does the work. Required: a service with nothing behind it accepts
	// jobs it will never run, which is a worse failure than refusing to start.
	Runner Runner
	// Store holds jobs. Nil means the in-memory store of §8.3, which is the
	// single-node default and what a buyer evaluating the product runs.
	Store job.Store
	// Token is the bearer every call must present. It is required, and New
	// refuses without it: a service that starts with authentication off
	// because a field was left empty is the one configuration mistake nobody
	// notices until it matters.
	Token string
	// Spool is the directory uploaded sources are written to (§8.4). Empty
	// means the OS temporary directory.
	Spool string
	// MaxResultBytes is the size at which GetResult refuses and points at
	// StreamResult. Zero means the default below, which is under gRPC's own
	// 4MB limit so the refusal arrives as our sentence rather than as a
	// transport error the caller has to interpret.
	MaxResultBytes int
	// PageSize is how many records StreamResult puts in a page when the caller
	// does not say. Zero means the default below.
	PageSize int
	// SweepEvery is how often expired work is swept. Zero means the default
	// below. §5c gives held work a lifetime; a store whose Expire is never
	// called is the abandoned-review database that lifetime exists to prevent,
	// so the sweeper is started by New rather than left to a caller to
	// remember.
	SweepEvery time.Duration
}

// Defaults. They are guesses in the sense §7.4 means, and they are here rather
// than inline so a zero Config is a working service.
const (
	// Under gRPC's 4MB so the caller gets our refusal, which names the RPC
	// that will work, instead of a truncation or a transport-level error.
	defaultMaxResultBytes = 3 << 20
	defaultPageSize       = 500
	defaultSweepEvery     = time.Minute
	// uploadLimit caps one spooled source. §8.4 asks for admission control
	// rather than optimism, and a disk that fills is the same failure as a
	// queue that OOMs, one layer down.
	defaultUploadLimit = 64 << 30
)

// ErrNoToken is New refusing to start without authentication configured.
var ErrNoToken = errors.New("service: a bearer token is required; a service that starts unauthenticated is the mistake nobody notices")

// ErrNoRunner is New refusing to start with nothing behind it.
var ErrNoRunner = errors.New("service: a runner is required; accepting jobs nothing will run is worse than refusing to start")

// Server is the gRPC service.
//
// It embeds the generated Unimplemented struct on purpose: a method added to
// the proto and forgotten here is then a clear Unimplemented at runtime rather
// than a compile error that tempts somebody into a stub returning nil. The
// auth table test in auth_test.go is what makes sure the forgetting is caught
// before that.
type Server struct {
	alchemyv1.UnimplementedAlchemyServer

	cfg    Config
	store  job.Store
	spool  string
	tokens *tokens

	// ctx is cancelled by Close and is the parent of every job's context, so
	// shutting down stops the work rather than orphaning it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// node names this process in a lease. §8.3 has a cluster share a store, so
	// the holder of a lease has to be identifiable even when there is one.
	node string

	mu sync.Mutex
	// sources are the spooled uploads, by ID. They live here and not in the
	// job store because the store holds jobs and nothing else, deliberately.
	sources map[string]Source
	// runs is the live state of every job this node admitted: its spec, its
	// listeners and its pending result.
	runs map[string]*jobRun
}

// New builds a server. It refuses rather than defaults on the two settings
// whose absence is silent: no token and no runner.
func New(cfg Config) (*Server, error) {
	if cfg.Token == "" {
		return nil, ErrNoToken
	}
	if cfg.Runner == nil {
		return nil, ErrNoRunner
	}
	if cfg.Store == nil {
		cfg.Store = job.New(job.Config{})
	}
	if cfg.Spool == "" {
		dir, err := os.MkdirTemp("", "alchemy-spool-")
		if err != nil {
			return nil, err
		}
		cfg.Spool = dir
	}
	if cfg.MaxResultBytes <= 0 {
		cfg.MaxResultBytes = defaultMaxResultBytes
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = defaultPageSize
	}
	if cfg.SweepEvery <= 0 {
		cfg.SweepEvery = defaultSweepEvery
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:     cfg,
		store:   cfg.Store,
		spool:   cfg.Spool,
		tokens:  newTokens(cfg.Token),
		ctx:     ctx,
		cancel:  cancel,
		node:    "node-" + mintID()[:8],
		sources: map[string]Source{},
		runs:    map[string]*jobRun{},
	}
	s.wg.Add(1)
	go s.sweep()
	return s, nil
}

// sweep runs pkg/job's expiry against the clock and drops what it took.
//
// Both halves matter. The store forgets the job; this forgets the pending
// result and the corpus behind it, which is the part §5c actually warns about
// — a service that expired the row and kept the graph would be the knowledge
// base it says it is not, reached by the slowest possible route.
func (s *Server) sweep() {
	defer s.wg.Done()
	tick := time.NewTicker(s.cfg.SweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
			swept, err := s.store.Expire(s.ctx)
			if err != nil {
				continue
			}
			// Expired and Reaped are both "this job is over": the first
			// because nobody came for it, the second because it was
			// collected. Requeued is deliberately left alone — that job is
			// still work, and forgetting its source would requeue an import
			// with nothing to import.
			for _, id := range append(swept.Expired, swept.Reaped...) {
				s.drop(id)
			}
		}
	}
}

// Close stops every running job and waits for it. It is not a graceful drain:
// §4 says the service returns its output and forgets it, so there is nothing
// in flight worth preserving past the process.
func (s *Server) Close() {
	s.cancel()
	s.wg.Wait()
}
