package service

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// embedSurvivors spends the vectors a held job did not.
//
// §5c puts the embedder after review — "embedding rejected content wastes the
// call, and embedding before edits means the vectors describe text that has
// since changed" — and pkg/pipeline keeps that promise by stopping before its
// last stage whenever a job is held. Nothing then spent them afterwards, and
// the graph a reviewer released came back with chunks and no vectors: a store
// loading it reports the chunks lost, and fact_provenance cannot cite the text
// a fact came from, which is the one thing provenance is for.
//
// It is called from resolve and nowhere else, after the two guards that say
// the job is no longer held. That placement is the rule §7.3 asks for at this
// stage: a decision that leaves another conflict open leaves a graph that may
// still change, and a graph that may still change must not be paid to embed.
func (s *Server) embedSurvivors(r *jobRun, res alchemy.Result) alchemy.Result {
	// A job the pipeline finished on its own already has its vectors, and a
	// job with no chunks has nothing to describe. §8.2: paying twice for the
	// identical call is a bug.
	if len(res.Chunks) == 0 || len(res.Vectors) > 0 {
		return res
	}
	// A job that named no embedder wanted a graph and no vectors, which §6
	// calls a job rather than a misconfiguration. Nothing is missing, so
	// nothing is reported — an Unread line here would teach every caller that
	// the list is noise.
	if !wantsVectors(r.spec.Models.Embedder) {
		return res
	}
	// Claimed before the call and not after it: two reviewers answering the
	// last two questions at once would otherwise both find the job unheld,
	// both find no vectors, and both buy them.
	if !r.claimEmbed() {
		return res
	}

	f, ok := s.cfg.Runner.(Finisher)
	if !ok {
		// See Finisher: the old behaviour, said out loud.
		return unvectored(res, "the job supplied an embedder, but the runner that finished it after "+
			"review cannot embed (it does not implement service.Finisher), so this chunk has no vector")
	}
	// s.ctx rather than the reviewer's stream: a person who answered the last
	// question has finished the job, not abandoned it, and an embed cancelled
	// because they closed the tab would leave the same missing vectors this
	// exists to prevent. Close still stops it, because a process shutting down
	// should stop spending.
	out, err := f.Embed(s.ctx, r.spec, res)
	if err != nil {
		// The reviewer's work is not thrown away for an endpoint that was
		// down: the decisions were made by a person and the graph is finished
		// without the vectors. What the failure buys is the sentence — the
		// chunks that got nothing are named with the endpoint's own words, so
		// "no vectors" arrives with its reason attached rather than as a
		// silence somebody has to reconstruct from a log.
		return unvectored(out, fmt.Sprintf("the embedder failed after review: %s", err))
	}
	return out
}

// wantsVectors reports that the caller named an embedder at all. Either half
// is enough, for the reason pkg/runner's supplied gives: a name with no URL is
// a model the provider reaches by name, and a URL with no name is an endpoint
// that reports its own.
func wantsVectors(m Model) bool { return m.Name != "" || m.Endpoint != "" }

// unvectored names every chunk that has no vector, and why.
//
// One line per chunk rather than one line per job, because that is what
// pkg/embed already writes when a batch fails — same fields, same locator — so
// a reader chasing a chunk with no vector finds one answer in one place
// whatever prevented it. The count is refilled from the list so the two cannot
// drift; ChunksUnread is documented as the length of Unread rather than a
// second tally, and this is the last stage that can add to either.
func unvectored(res alchemy.Result, reason string) alchemy.Result {
	has := make(map[int]bool, len(res.Vectors))
	for _, v := range res.Vectors {
		has[v.Chunk] = true
	}
	// Copied rather than appended in place: the pending result a reviewer is
	// still holding shares this slice, and growing it under them is how a
	// caller sees findings change without anything having been decided.
	unread := make([]alchemy.Unread, len(res.Unread), len(res.Unread)+len(res.Chunks))
	copy(unread, res.Unread)
	for _, c := range res.Chunks {
		if has[c.Index] {
			continue
		}
		// A blank chunk has no vector because there was nothing to embed, not
		// because anything failed. pkg/embed counts those in ChunksEmpty and
		// deliberately does not name them: reporting one as unreadable would
		// make a chunker producing rubbish look like an endpoint going down.
		if strings.TrimSpace(c.Text) == "" {
			continue
		}
		unread = append(unread, alchemy.Unread{
			Source:  c.Source,
			Locator: fmt.Sprintf("chunk %d (bytes %d-%d)", c.Index, c.Start, c.End),
			Reason:  reason,
		})
	}
	res.Unread = unread
	res.Counts.ChunksUnread = len(unread)
	res.Counts.Vectors = len(res.Vectors)
	return res
}
