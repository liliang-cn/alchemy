package pipeline

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// reviewJob ranks what is worth a person's time and carries onto the graph
// whatever the caller already had answered.
//
// The conversation §6 describes — items out, decisions in — outlives one call:
// a job that has questions holds and hands back its queue, a person answers,
// and the answers are read from Request.Inbox. That is why the inbox is asked
// rather than blocked on: a callback would make Run wait on a human, and §5c
// is explicit that what is held between the question and the answer is a job
// in a store, not a goroutine.
//
// This is the end of the job, so it reads the conversation as it now stands —
// including whatever arrived while the corpus was being extracted. Chunks
// extracted before a rule existed are decided by it here, at the end, which is
// where deciding has always happened; what the rule did to the chunks that ran
// after it is in their provenance and not in the graph's shape. The two are
// different facts and both are kept.
//
// The `always` rules the decisions produce are deliberately not returned. A
// rule outlives the job it was made in — §5c records it so that a later reader
// can see why it exists — and this package stores nothing (§4). The service
// that holds the review conversation is what records them; what Run puts in
// the result is the other half of §5c's obligation, the reviewer's name in the
// provenance of what they decided.
func (r *run) reviewJob(rep verify.Report, res alchemy.Result) (alchemy.Result, []review.Item, error) {
	r.decided = r.decisions()
	items := review.Queue(rep, res, review.Options{
		Reviewing:     r.req.Reviewing,
		MinConfidence: r.req.MinConfidence,
		Rules:         r.rules(),
	})
	if len(items) == 0 {
		return res, nil, nil
	}
	r.stage = stageReview
	r.emit(Event{Kind: EventStage, Stage: stageReview})

	// Which chunks had produced something is measured before the decisions
	// land, because that is the only moment at which "this chunk's records
	// were thrown away" is distinguishable from "this chunk never produced
	// any".
	before := chunksWithRecords(res)
	out, _, err := review.Apply(res, items, r.decided)
	if err != nil {
		return res, items, fmt.Errorf("pipeline: review: %w", err)
	}
	out.Chunks = survivingChunks(res.Chunks, before, chunksWithRecords(out))
	return out, items, nil
}

// openQuestions is what a person still has to answer: the queue, less the
// items a recorded rule already answered (review.Open), less the ones this
// request brought a decision for.
func openQuestions(items []review.Item, decisions []review.Decision) []review.Item {
	answered := make(map[string]bool, len(decisions))
	for _, d := range decisions {
		answered[d.ItemID] = true
	}
	var out []review.Item
	for _, it := range review.Open(items) {
		if !answered[it.ID] {
			out = append(out, it)
		}
	}
	return out
}

// chunksWithRecords reports which chunks the graph currently has an entity or
// a relation from.
func chunksWithRecords(res alchemy.Result) map[int]bool {
	out := make(map[int]bool, len(res.Chunks))
	for _, e := range res.Entities {
		out[e.Provenance.Chunk] = true
	}
	for _, rel := range res.Relations {
		out[rel.Provenance.Chunk] = true
	}
	return out
}

// survivingChunks drops the chunks whose every record a reviewer threw away.
//
// §5c: "embedding rejected content wastes the call". A chunk that produced
// three entities and lost all three is text a person looked at and did not
// keep, and a vector of it would put rejected content in the index the review
// was supposed to keep it out of.
//
// A chunk that never produced anything is kept, which is the asymmetry that
// matters: an empty chunk is not rejected content, it is prose the vocabulary
// had nothing to say about, and it is still the document a search should find.
func survivingChunks(chunks []alchemy.Chunk, before, after map[int]bool) []alchemy.Chunk {
	out := make([]alchemy.Chunk, 0, len(chunks))
	for _, c := range chunks {
		if before[c.Index] && !after[c.Index] {
			continue
		}
		out = append(out, c)
	}
	return out
}
