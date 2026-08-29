package pipeline

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// counts assembles §5's obligation: "Every returned graph is accompanied by
// the numbers needed to distrust it: how many edges were deterministic vs
// inferred, how many violated the ontology, how many chunks produced nothing,
// and which mappings were guessed."
//
// Every stage produces a part of it and no stage can produce the whole — the
// verifier cannot see the tabular guesses, the tabular reader cannot see the
// chunks, and neither can see what the embedder found blank. This is the only
// place that holds all of it, which is why §5's obligation is finally keepable
// here and nowhere earlier.
//
// Six of the nine fields are computed from the slices they sit next to rather
// than accumulated as the stages report them. That is deliberate and it is the
// whole point of the audit: a count incremented by a stage drifts away from
// its subject the first time a later stage drops a record — review rejecting
// an edge, the verifier canonicalising two spellings into one node — and then
// the block that exists to be trusted is the thing lying. Counted here, "the
// numbers do not add up" is not expressible.
func (r *run) counts(res alchemy.Result) alchemy.Counts {
	c := alchemy.Counts{
		Entities:   len(res.Entities),
		Relations:  len(res.Relations),
		Violations: len(res.Violations),
		Conflicts:  len(res.Conflicts),
		Guesses:    len(res.Guesses),
		// ChunksUnread is the length of Unread rather than a count of unread
		// pages, so the number and the list a reader checks it against are the
		// same fact. Every stage that can fail to read something puts it there
		// — a scanned page with no OCR model (§5), a chunk whose model call
		// failed, a batch the embedder lost — and a count that only counted
		// the first would make a broken endpoint look like a clean run.
		ChunksUnread: len(res.Unread),
		// ChunksEmpty is the one number that cannot be recomputed from the
		// result, because a chunk that produced nothing leaves nothing behind
		// to count. It is the sum of what the extractor and the embedder each
		// reported, and a chunk that was empty at both stages is counted
		// twice: pkg/embed reports a total rather than naming the chunks, so
		// the union is not available to add up. The field is a signal about
		// the corpus — §5b reads a high number as "the extraction is failing
		// quietly" — and it is honest at that job.
		ChunksEmpty: r.chunksEmpty,
	}
	for _, rel := range res.Relations {
		if rel.Provenance.Producer.Deterministic() {
			c.Deterministic++
		}
	}
	// Inferred is the remainder rather than a second tally, so that §5b's
	// 890 + 290 = 1180 is arithmetic rather than a coincidence two counters
	// have to keep agreeing on.
	c.Inferred = c.Relations - c.Deterministic
	return c
}
