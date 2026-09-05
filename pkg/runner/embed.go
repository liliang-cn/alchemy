package runner

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
	"github.com/liliang-cn/alchemy/pkg/embed"
	"github.com/liliang-cn/alchemy/pkg/service"
)

// Runner is also the optional half of what pkg/service needs: the part that
// can still buy something after a person has answered.
var _ service.Finisher = (*Runner)(nil)

// Embed spends the vectors a held job did not.
//
// It is here for the reason every other model call is here: this package owns
// the factory, and §6 hands the service endpoint descriptions rather than
// models. pkg/service decides *when* — after the decisions are applied, the
// graph re-verified, and nothing is left holding the job — and this decides
// nothing at all beyond how to buy what it was asked for.
//
// It is deliberately the same call pipeline.embedSurvivors makes, with the
// same options and the same wrapping, because it is the same stage arriving
// late: a second embedding path would be a second set of defaults, a second
// place for the budget to be forgotten, and two ways for one corpus to end up
// with vectors of different widths.
//
// The budget is the part it would be easiest to leave out and worst to. §8.2's
// arithmetic is about ten nodes each running "8 concurrent" against an
// endpoint that permits 20; ten held jobs released within a minute of each
// other — which is what a queue worked on a Monday morning looks like — is the
// same sum with a person as the trigger, and an unwrapped call here would be
// the one path in the process that does not lease a slot.
func (r *Runner) Embed(ctx context.Context, spec service.JobSpec, res alchemy.Result) (alchemy.Result, error) {
	models, err := buildModels(r.cfg.Factory, spec.Models)
	if err != nil {
		// The graph goes back with the error rather than being dropped: a
		// person made the decisions on it, and this is the last stage of a job
		// they have already finished working.
		return res, err
	}
	// A nil embedder is a job that ordered no vectors (§6), and an empty
	// corpus has nothing to describe. Both are pkg/embed's own no-ops; they
	// are answered here as well so that a job which asked for nothing does not
	// even build a model.
	if models.Embedder == nil || len(res.Chunks) == 0 {
		return res, nil
	}
	embedder := models.Embedder
	if r.cfg.Budget != nil {
		// Wrapped, not consulted — pkg/budget's own rule, and the reason
		// pkg/embed still knows only about alchemy.Embedder.
		embedder = budget.WrapEmbedder(embedder, r.cfg.Budget)
	}

	out, err := embed.Embed(ctx, res.Chunks, embed.Options{Embedder: embedder})
	res = withEmbedding(res, out)
	if err != nil {
		return res, fmt.Errorf("runner: embedding the chunks that survived review: %w", err)
	}
	return res, nil
}

// withEmbedding folds what the embedder produced into the reviewed graph.
//
// The numbers are the point of it. §5 makes Counts the obligation that
// justifies the release, and the three this stage can change are changed the
// way pipeline.counts changes them: Vectors and ChunksUnread are recomputed
// from the slices beside them, so a count and its subject cannot drift, and
// ChunksEmpty accumulates because a chunk that held no text leaves nothing
// behind to count. §7.2 gets the fourth: the calls are added to the bill the
// earlier stages already wrote, one line per model and stage.
func withEmbedding(res alchemy.Result, out embed.Result) alchemy.Result {
	res.Vectors = out.Vectors
	// Copied rather than appended in place. The result this was handed is the
	// one pkg/service is still holding for the reviewer, and append would
	// write through the shared backing array into a graph nobody has been
	// given yet.
	if len(out.Unread) > 0 {
		unread := make([]alchemy.Unread, 0, len(res.Unread)+len(out.Unread))
		res.Unread = append(append(unread, res.Unread...), out.Unread...)
	}
	res.ModelCalls = billed(res.ModelCalls, out.ModelCalls)
	res.Counts.Vectors = len(res.Vectors)
	res.Counts.ChunksUnread = len(res.Unread)
	res.Counts.ChunksEmpty += out.ChunksEmpty
	return res
}

// billed adds this stage's calls to the report, keeping one line per model and
// stage. §7.2 asks for a total rather than a log, and a job whose extraction
// and embedding ran hours apart is still one job with one bill.
func billed(have, spent []alchemy.ModelCall) []alchemy.ModelCall {
	out := make([]alchemy.ModelCall, len(have), len(have)+len(spent))
	copy(out, have)
	for _, c := range spent {
		merged := false
		for i := range out {
			if out[i].Model == c.Model && out[i].Stage == c.Stage {
				out[i].Calls += c.Calls
				out[i].Tokens += c.Tokens
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, c)
		}
	}
	return out
}
