package pipeline

import (
	"context"
	"fmt"
	"sync"

	"github.com/liliang-cn/alchemy/pkg/extract"
)

// extract runs the model over the prose.
//
// Per source, deliberately. The extractor merges what its chunks proposed and
// reports the disagreements it resolved, which is right within a document —
// chunk 3 and chunk 40 talking about one cluster are one node. Across
// documents it would be wrong: two sources disagreeing is the conflict §8.1
// says only the coordinator can notice, and an extractor handed both would
// have merged them into one node before the verifier ever saw two claims.
//
// Per source is not one at a time. That is the same sentence read twice: the
// documents are independent, which is exactly why they can be read together.
// Sixty-seven documents took twenty-nine minutes read one after another at
// four chunks in flight, most of them small enough that four was never
// reached — a corpus spending its afternoon waiting for a round trip.
func (r *run) extract(ctx context.Context) error {
	if len(r.docs) == 0 {
		return nil
	}
	r.stage = stageExtract
	r.emit(Event{Kind: EventStage, Stage: stageExtract})

	// Documents are extracted together, and their results are applied in
	// document order afterwards. Both halves matter. They can run together
	// because this function's own argument for keeping them apart says they
	// are independent — an extractor handed two documents would merge two
	// sources into one node, and here each still sees only its own — and
	// independent work run one item at a time is waiting, not caution.
	//
	// They are applied in order because a Result must not depend on a
	// scheduler. That is the property extract.run already keeps one level
	// down: the workers decide when a reply arrives, assemble decides where it
	// goes, and the two decisions never meet. Appending from inside the
	// workers would have made the order of a graph's entities, and the order
	// of the conflicts found in them, a different answer on every run of one
	// corpus.
	type outcome struct {
		res extract.Result
		err error
	}
	out := make([]outcome, len(r.docs))

	// One document at a time unless the caller asked for more, and that
	// default is load-bearing rather than timid.
	//
	// §6's first reason for choosing gRPC is that a decision reaches an
	// extraction that has not run yet, and the case it is about is exactly two
	// documents: a reviewer looking at the first one's queue while the second
	// is still to come. Start every document at once and there is no "still to
	// come" — the guarantee does not weaken, it stops existing. So a caller
	// who says nothing gets what they have always got, guarantee included.
	//
	// A caller who sets concurrency is buying wall-clock with that window, and
	// should know it: the wider the job runs, the fewer chunks are left for a
	// mid-run decision to reach. It is the same trade the knob has always
	// made one level down — four chunks in flight is already four chunks a
	// decision cannot reach — and it is now the same knob, which is the honest
	// arrangement. Sixty-seven independent documents read one after another
	// took twenty-nine minutes; nobody was reviewing them.
	//
	// The budget is the whole stage's, not each document's: "thirty-two calls
	// in flight" means thirty-two, not thirty-two times however many files
	// somebody uploaded. Each document gets a share of at least one, so a
	// corpus of many small documents spends its width across them and a corpus
	// of one large document spends it within.
	width, perDoc := 1, extract.DefaultConcurrency
	if r.req.Concurrency > 0 {
		width, perDoc = r.req.Concurrency, 1
		if width > len(r.docs) {
			width = len(r.docs)
		}
		if share := r.req.Concurrency / len(r.docs); share > 1 {
			perDoc = share
		}
	}

	sem := make(chan struct{}, width)
	var wg sync.WaitGroup
	for i, d := range r.docs {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, d docSource) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := extract.Extract(ctx, d.chunks, extract.Options{
				LLM:        r.req.Models.LLM,
				Vocabulary: r.vocabulary,
				OntologyID: r.ontologyID,
				// §8.2. Nil is caching off, and pkg/cache contracts that a
				// broken cache is a miss rather than a failed job, so there is
				// nothing for this package to decide about it.
				Cache: r.req.Cache,
				// §6's first reason for gRPC, reaching the one stage it is
				// about. The extractor asks this per chunk, so a rule recorded
				// while this source is being read applies to the rest of it.
				// See standing.go.
				Standing:    r.standing(),
				Concurrency: perDoc,
			})
			out[i] = outcome{res: res, err: err}
		}(i, d)
	}
	wg.Wait()

	for i, o := range out {
		res := o.res
		// The Result comes back whether or not the error did, and everything
		// in it is kept for the same reason: a failed run's cost, the chunks
		// it could not read and the disagreements it did see are exactly what
		// a caller needs in order to decide whether to run it again.
		r.spend(res.ModelCalls...)
		r.entities = append(r.entities, res.Entities...)
		r.relations = append(r.relations, res.Relations...)
		r.unread = append(r.unread, res.Unread...)
		r.chunksEmpty += res.ChunksEmpty
		// What the model chose between. The tabular and graph-import producers
		// have raised these since there were producers; this is the stage
		// where a model actually decides something and it had never reported
		// one, so a prose run's "guesses 0" said nobody had asked.
		r.guesses = append(r.guesses, res.Guesses...)
		r.found(res.Conflicts...)
		r.progress(stageExtract, r.docs[i].name)
		if o.err != nil {
			return fmt.Errorf("pipeline: extract %q: %w", r.docs[i].name, o.err)
		}
	}
	return nil
}
