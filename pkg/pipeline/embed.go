package pipeline

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/embed"
)

// embedSurvivors is §3's last stage and §5c's ordering: "Vectors are not
// reviewable and should not be in the queue — nobody can eyeball a
// 768-dimensional vector. They are recomputed for whatever text survives
// review, which is the only sensible ordering: embedding rejected content
// wastes the call, and embedding before edits means the vectors describe text
// that has since changed."
//
// So it runs on the result that came out of review rather than the one that
// went in, and it runs after the hold: a job that is going to need a person is
// a job whose embedding bill has not been paid yet.
func (r *run) embedSurvivors(ctx context.Context, res alchemy.Result) (alchemy.Result, error) {
	if r.req.Models.Embedder == nil || len(res.Chunks) == 0 {
		return res, nil
	}
	r.stage = stageEmbed
	r.emit(Event{Kind: EventStage, Stage: stageEmbed})
	out, err := embed.Embed(ctx, res.Chunks, embed.Options{Embedder: r.req.Models.Embedder})
	r.spend(out.ModelCalls...)
	r.unread = append(r.unread, out.Unread...)
	r.chunksEmpty += out.ChunksEmpty
	res.Vectors = out.Vectors
	r.progress(stageEmbed, "")
	if err != nil {
		return res, fmt.Errorf("pipeline: embed: %w", err)
	}
	return res, nil
}
