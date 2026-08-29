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
	"github.com/liliang-cn/alchemy/pkg/pipeline"
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
// Decisions are read once, before the first stage. That is what pipeline.Run
// can accept today: Request.Decisions is an input to the run, and there is no
// channel into a run in progress. See the package's run_test.go for what a
// decision made mid-run does and does not reach.
func (r *Runner) Run(ctx context.Context, jobID string, spec service.JobSpec, events chan<- service.Event, in service.Inbox) (alchemy.Result, error) {
	req, err := buildRequest(spec, in)
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
