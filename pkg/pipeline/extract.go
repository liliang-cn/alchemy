package pipeline

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/extract"
)

// extract runs the model over the prose, one source at a time.
//
// Per source, deliberately. The extractor merges what its chunks proposed and
// reports the disagreements it resolved, which is right within a document —
// chunk 3 and chunk 40 talking about one cluster are one node. Across
// documents it would be wrong: two sources disagreeing is the conflict §8.1
// says only the coordinator can notice, and an extractor handed both would
// have merged them into one node before the verifier ever saw two claims.
func (r *run) extract(ctx context.Context) error {
	if len(r.docs) == 0 {
		return nil
	}
	r.stage = stageExtract
	r.emit(Event{Kind: EventStage, Stage: stageExtract})
	for _, d := range r.docs {
		res, err := extract.Extract(ctx, d.chunks, extract.Options{
			LLM:        r.req.Models.LLM,
			Vocabulary: r.vocabulary,
			OntologyID: r.ontologyID,
		})
		// The Result comes back whether or not the error did, and everything
		// in it is kept for the same reason: a failed run's cost, the chunks
		// it could not read and the disagreements it did see are exactly what
		// a caller needs in order to decide whether to run it again.
		r.spend(res.ModelCalls...)
		r.entities = append(r.entities, res.Entities...)
		r.relations = append(r.relations, res.Relations...)
		r.unread = append(r.unread, res.Unread...)
		r.chunksEmpty += res.ChunksEmpty
		r.found(res.Conflicts...)
		r.progress(stageExtract, d.name)
		if err != nil {
			return fmt.Errorf("pipeline: extract %q: %w", d.name, err)
		}
	}
	return nil
}
