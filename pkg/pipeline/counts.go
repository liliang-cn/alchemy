package pipeline

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

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
		Entities:  len(res.Entities),
		Relations: len(res.Relations),
		// Chunks and Vectors are counted here for the reason everything else
		// is: they are a function of the slices next to them. What they add is
		// a denominator §8.4's reader had none of — a paged consumer meets the
		// counts on page 0 and the records afterwards, so "23 chunks produced
		// nothing" was a number with nothing to be 23 out of.
		Chunks:     len(res.Chunks),
		Vectors:    len(res.Vectors),
		Violations: len(res.Violations),
		Conflicts:  len(res.Conflicts),
		Duplicates: len(res.Duplicates),
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
		// Dropped is the second number that cannot be recomputed from the
		// result, and for the same reason: a record a standing rule removed is
		// not in the slices this function counts, and by the time anything
		// gets here there is nothing left to subtract. Every stage that lets a
		// rule act — the settle inside extraction, and the review at the end —
		// adds to it as it goes.
		Dropped: int(r.dropped.Load()),
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

// ownChunkNumbering is this package's post-condition on its own renumbering.
//
// adopt() gives every chunk a job-wide index, and everything downstream depends
// on that: alchemy.Provenance.Chunk and alchemy.Vector.Chunk are plain ints
// with no source beside them, so a chunk index that named two chunks would make
// a citation point at two pages and an embedding describe the wrong text. §8.4
// makes it worse rather than better — a paged result hands a consumer vectors
// and chunks in separate messages, and the index is the only thing joining
// them.
//
// It is checked rather than asserted in a comment because the invariant was
// exactly that until now: true, load-bearing, and written down nowhere. One of
// four stores discovered it as a live data-loss path and refused it by name;
// the others were exposed to the same overwrite and had no way to know. A
// producer that asks its consumers to rely on something owes them the check.
//
// It is a failure and not a finding. Nothing in a corpus can cause it — the
// caller supplied files, not chunk numbers — so it is this package being wrong
// about its own work, and returning a graph would be handing a store two chunks
// that will be written as one.
func ownChunkNumbering(res alchemy.Result) error {
	seen := make(map[int]string, len(res.Chunks))
	for _, c := range res.Chunks {
		prev, dup := seen[c.Index]
		if !dup {
			seen[c.Index] = c.Source
			continue
		}
		return fmt.Errorf("pipeline: chunk %d was given to both %q and %q; a chunk index has to name one chunk across the whole job, "+
			"or a citation points at two pages and a vector describes the wrong text", c.Index, prev, c.Source)
	}
	return nil
}
