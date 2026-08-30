// Package runner is the adapter that makes pkg/pipeline satisfy
// service.Runner. It is the seam DESIGN.md §6 leaves open on purpose: the
// service declares what it needs from "whatever actually does the work" and
// never imports the thing that does it, so this package is where a transport
// vocabulary (spooled paths, JSON ontologies, endpoint descriptions) becomes a
// pipeline vocabulary (lazy readers, a loaded Ontology, live models).
//
// It owns no analysis and makes no decision the design gives to somebody else.
// In particular it does not decide what a result means: §7.3's rule that a
// conflict holds a job lives in pkg/service, which computes the hold from the
// result it is handed, so a held pipeline run comes back through here as a
// result and a nil error rather than as a failure. See Run.
package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
	"github.com/liliang-cn/alchemy/pkg/cache"
	"github.com/liliang-cn/alchemy/pkg/pipeline"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/service"
)

// eventBuffer is the pipeline's side of the event bridge. It is small: the
// pipeline never blocks on a full channel (see pipeline/emitter.go), so a
// deeper buffer would only delay how stale a superseded progress reading may
// be before it is dropped.
const eventBuffer = 32

// ErrNoFactory is New refusing to start with no way to build a model.
//
// It is refused here rather than at the first document job because the failure
// would otherwise arrive minutes into an import, after a corpus had been
// spooled and read, which is the expensive half of the run that could not
// possibly succeed.
var ErrNoFactory = errors.New("runner: a model factory is required; a runner that cannot build the caller's models cannot run a document job")

// Config is what a Runner is built from.
type Config struct {
	// Factory turns the endpoints a job supplied into live models. It is
	// injected rather than imported so that this package is testable without a
	// network, and so the choice of provider belongs to the binary.
	Factory Factory
	// Budget bounds how many calls are in flight against one model endpoint
	// (§8.2). Nil is a single node with no declared endpoint limit, which is
	// what a buyer evaluating the product runs.
	Budget budget.Budget
	// Cache is §8.2's content-addressed store for extraction results, and it
	// is optional. Nil is caching off.
	//
	// It is per-process rather than per-job because it is a property of the
	// deployment: one node evaluating the product has an in-process one, and a
	// cluster shares a store (§8.3). Nothing a caller sends can turn it on or
	// off, which is deliberate — a cache is an operator's decision about money,
	// and pkg/cache guarantees it cannot change what a job returns.
	Cache cache.Cache
	// Rules is the standing policy this deployment carries: §5c's `always`
	// rules written down by a person rather than produced by a queue, in force
	// on every job this runner executes.
	//
	// Per process rather than per job, and that is the point of it. §4 says the
	// service stores no policy between jobs, and it still does not — this is
	// configuration the operator started the process with, the same as the
	// budget and the cache, and it lives only as long as the process. What it
	// buys is the case a per-job rule list cannot reach: a nightly import that
	// nobody attends, whose policy has to live somewhere other than in the
	// request a scheduler writes.
	//
	// Unlike the cache, this does change what a job returns, so every rule in
	// it is refused at New unless it can explain itself, and every record it
	// removes is counted in alchemy.Counts.Dropped.
	Rules []review.Rule
}

// Runner implements service.Runner on top of pipeline.Run.
type Runner struct {
	cfg Config
}

// New builds a Runner.
func New(cfg Config) (*Runner, error) {
	if cfg.Factory == nil {
		return nil, ErrNoFactory
	}
	// Checked here for the same reason the factory is: a policy that cannot
	// explain itself must stop the process rather than the first job that
	// meets it, by which time the person who could fix it has gone home and
	// the night's import has not run.
	for i, rule := range cfg.Rules {
		if err := rule.Validate(); err != nil {
			return nil, fmt.Errorf("runner: configured rule %d: %w", i+1, err)
		}
	}
	return &Runner{cfg: cfg}, nil
}

// Runner satisfies the interface pkg/service declares.
var _ service.Runner = (*Runner)(nil)

// Run executes one job and returns the graph it produced.
//
// The one thing this method must get right is the last branch. pipeline.Run
// answers a held job with a *HeldError carrying the pending graph, and this
// returns that graph with a nil error, because §7.3 lives in exactly one place
// and it is not here: pkg/service computes the hold itself, from the result it
// is handed, with review.Queue and review.Held. A runner that passed the error
// through would take that decision away from the one place that owns it — the
// service would see a failure, never see the conflicts, and a job that needed a
// person would be reported as a defect. Every other error is a real failure and
// is returned as one.
//
// The inbox is handed through, not drained. pipeline.Request takes a live
// source rather than a slice, and the pipeline asks it per chunk, so an
// `always` rule recorded while this job is running reaches the chunks it has
// not extracted yet — §6's first reason for choosing gRPC, arriving at the one
// stage it was about. See buildRequest's inboxOf, and run_test.go for the
// contract that pins it.
func (r *Runner) Run(ctx context.Context, jobID string, spec service.JobSpec, events chan<- service.Event, in service.Inbox) (alchemy.Result, error) {
	req, err := buildRequest(spec, in, r.cfg.Rules)
	if err != nil {
		return alchemy.Result{}, err
	}
	if req.Models, err = buildModels(r.cfg.Factory, spec.Models); err != nil {
		return alchemy.Result{}, err
	}
	// §8.2: the budget is cluster-wide and reaches the stages the only way
	// pkg/budget allows — the pipeline wraps the models with it before the
	// first stage runs, so no stage learns a budget exists.
	req.Budget = r.cfg.Budget
	// §8.2: "paying twice for the identical call after a crash is a bug." The
	// pipeline hands this to the extractor, which is the only stage that buys
	// anything cacheable.
	req.Cache = r.cfg.Cache

	// The pipeline owns its own channel: it closes the one it is given, and
	// the service's is closed by the service. Bridging them is what keeps both
	// contracts, and the forwarder is waited for so that nothing is sent on
	// the service's channel after Run has returned.
	raw := make(chan pipeline.Event, eventBuffer)
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		forward(raw, events)
	}()

	res, err := pipeline.Run(ctx, req, raw)
	<-forwarded

	var held *pipeline.HeldError
	if errors.As(err, &held) {
		return held.Pending, nil
	}
	if err != nil {
		return res, fmt.Errorf("runner: job %s: %w", jobID, err)
	}
	return res, nil
}
