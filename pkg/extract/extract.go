// Package extract asks a caller's language model for the entities and
// relations in a chunk, under a vocabulary the model is not allowed to widen.
//
// It is the one stage where a model decides something, which is why every
// other stage exists to check it (DESIGN.md §3). Two obligations follow from
// that and shape this package more than anything else does:
//
//   - Nothing leaves here without provenance. An entity with no source, chunk,
//     producer and model is an assertion nobody can audit, and §5b makes
//     auditability a product guarantee rather than a debugging aid.
//   - A chunk that could not be read is never returned as a chunk that was
//     empty. The two are different facts — one is about the documents, one is
//     about the endpoint — and conflating them hides a broken endpoint behind
//     "there was nothing in them", which is the report that gets believed.
package extract

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// stage is what this package reports as in alchemy.ModelCall.Stage (§7.2).
const stage = "extract"

// Options is what one extraction run was given.
type Options struct {
	// LLM is the caller's model. §6: models are supplied per job, never
	// configured globally.
	LLM alchemy.LLM
	// Vocabulary is the part of the ontology this run extracts under. It is
	// one part, taken by name from ontology.Ontology.Vocabulary, so a prose
	// run cannot reach the code vocabulary (§2.1).
	Vocabulary ontology.Vocabulary
	// OntologyID lands in Provenance.Ontology and is required: provenance that
	// cannot name the vocabulary an extraction was constrained by does not
	// support the claim that it was constrained at all.
	OntologyID string
	// Concurrency bounds the calls in flight. See defaultConcurrency.
	Concurrency int
}

// Result is what one extraction run produced, and the numbers needed to
// distrust it (§5).
type Result struct {
	Entities  []alchemy.Entity
	Relations []alchemy.Relation
	// Unread names the chunks whose model call failed or whose reply could not
	// be read, with why. Never empty for a chunk that failed.
	Unread []alchemy.Unread
	// Conflicts is two chunks claiming different things about one entity.
	// It is reported from here because merging is where the losing claim
	// would otherwise disappear: after two proposals become one node, no later
	// stage can see that they ever disagreed. §7.3: a conflict always requires
	// a person, whether or not review mode is on.
	Conflicts []alchemy.Conflict
	// ModelCalls is what the run spent, by model and stage (§7.2).
	ModelCalls []alchemy.ModelCall
	// ChunksEmpty is chunks the model read and honestly found nothing in.
	ChunksEmpty int
}

func (o Options) check() error {
	if o.LLM == nil {
		return errors.New("extract: Options.LLM is nil, and this stage has nothing to degrade to")
	}
	if o.OntologyID == "" {
		return errors.New("extract: Options.OntologyID is empty; provenance that cannot name the vocabulary is not provenance")
	}
	// §5: "supplying an ontology is required for document sources. There is no
	// unconstrained mode." A vocabulary that declares no types constrains
	// nothing, so this is refused here rather than discovered as a run that
	// bought a call per chunk and returned a graph nobody may act on.
	if len(o.Vocabulary.Entities) == 0 {
		return errors.New("extract: the vocabulary declares no entity types, so it constrains nothing; there is no unconstrained mode")
	}
	return nil
}

// Extract runs every chunk through the model and returns what it proposed.
//
// One chunk failing does not lose the job: the other chunks still run, and the
// one that failed is named in Unread. Every chunk failing does fail the job —
// a result of nothing that took a thousand model calls is a failure wearing a
// success's clothes — but the Result is still returned alongside the error,
// because §7.2's promise that cost is never hidden is least dispensable on the
// run that spent the most and got the least.
func Extract(ctx context.Context, chunks []alchemy.Chunk, opts Options) (Result, error) {
	if err := opts.check(); err != nil {
		return Result{}, err
	}
	// No chunks is a fact, not a fault. An empty corpus that errored would
	// make every caller guard for it, and the guard would eventually also
	// swallow the case below.
	if len(chunks) == 0 {
		return Result{}, nil
	}

	outcomes := run(ctx, chunks, opts)
	res := assemble(outcomes, opts)

	// A cancelled run is not a short run. Whatever finished before the cancel
	// is returned — it was paid for and it is real — but it is returned with
	// the error, because a caller handed the chunks that happened to finish and
	// told nothing would read them as the corpus.
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("extract: cancelled after reading %d of %d chunks: %w",
			len(chunks)-len(res.Unread), len(chunks), err)
	}
	if len(res.Unread) == len(chunks) {
		return res, fmt.Errorf("extract: all %d chunks were unread, so this run produced nothing: %s",
			len(chunks), res.Unread[0].Reason)
	}
	return res, nil
}

// chunkOutcome is what happened to one chunk. It is a value rather than a
// write into a shared result on purpose: the workers produce these in whatever
// order the endpoint answers, and assemble puts them back in chunk order, which
// is what makes the output independent of Concurrency (§7, §8.2).
type chunkOutcome struct {
	chunk alchemy.Chunk
	reply reply
	// unread is set when the call failed or the reply could not be read. When
	// it is set, nothing else on this outcome is used.
	unread *alchemy.Unread
	// calls and tokens are counted whether or not the call succeeded: a call
	// that failed was still paid for, and a cost report that counts only
	// successes understates the bill.
	calls  int
	tokens int
}

// one chunk, one call.
func extractChunk(ctx context.Context, c alchemy.Chunk, sys string, opts Options) chunkOutcome {
	out := chunkOutcome{chunk: c, calls: 1}
	resp, err := opts.LLM.Complete(ctx, alchemy.LLMRequest{System: sys, Prompt: userPrompt(c), JSON: true})
	out.tokens = resp.Tokens
	if err != nil {
		out.unread = unreadChunk(c, err.Error())
		return out
	}
	r, err := parseReply(resp.Text)
	if err != nil {
		// A reply that could not be read is not an empty chunk. This is the
		// line that keeps a model refusing, rambling or truncating from being
		// reported as "the documents had nothing in them".
		out.unread = unreadChunk(c, err.Error())
		return out
	}
	out.reply = r
	return out
}

func unreadChunk(c alchemy.Chunk, reason string) *alchemy.Unread {
	return &alchemy.Unread{
		Source:  c.Source,
		Locator: fmt.Sprintf("chunk %d (bytes %d-%d)", c.Index, c.Start, c.End),
		Reason:  reason,
	}
}

// assemble puts the outcomes back together in chunk order.
func assemble(outcomes []chunkOutcome, opts Options) Result {
	var res Result
	m := newMerger()
	spend := alchemy.ModelCall{Model: opts.LLM.Name(), Stage: stage}
	for _, o := range outcomes {
		spend.Calls += o.calls
		spend.Tokens += o.tokens
		if o.unread != nil {
			res.Unread = append(res.Unread, *o.unread)
			continue
		}
		ents := entitiesOf(o.chunk, o.reply, opts)
		if len(ents) == 0 && len(o.reply.Relations) == 0 {
			// Read, and honestly nothing in it. §5 wants this counted rather
			// than inferred from a short entity list.
			res.ChunksEmpty++
			continue
		}
		for _, e := range ents {
			m.add(e)
		}
	}
	// Relations are resolved only once every chunk's entities are known; see
	// merger.relationOf.
	for _, o := range outcomes {
		if o.unread != nil {
			continue
		}
		for _, rr := range o.reply.Relations {
			m.addRelation(m.relationOf(o.chunk, rr, opts))
		}
	}
	res.Entities = m.entities()
	res.Relations = m.relations()
	res.Conflicts = m.conflicts
	if spend.Calls > 0 {
		res.ModelCalls = []alchemy.ModelCall{spend}
	}
	return res
}

// defaultConcurrency is the number of chunks in flight when the caller names
// none.
//
// It is not derived from the CPU count, and that is the whole point of §8.2:
// the work here is a network call to the caller's endpoint, and the thing that
// breaks first is that endpoint's rate limit, not this process. A default tuned
// to our cores would scale itself straight into someone else's 429s.
//
// Four, because the two failure modes are asymmetric. Too low costs wall-clock
// on a large corpus, which is visible and adjustable. Too high costs a rate
// limit on the caller's first run, which looks like the service being broken.
// Four is fast enough that a corpus is not read one chunk at a time and low
// enough to sit under the smallest allowance a real endpoint is sold with.
// A caller who knows their budget should set Concurrency; §8.2 makes it a
// cluster-wide lease eventually, and this is the single-node stand-in for it.
const defaultConcurrency = 4

// run calls the model for every chunk, at most Concurrency at a time.
//
// Each worker writes to its own slot, and nothing is merged here. That is what
// makes the result independent of Concurrency: the workers decide when a reply
// arrives, assemble decides where it goes, and the two decisions never meet.
func run(ctx context.Context, chunks []alchemy.Chunk, opts Options) []chunkOutcome {
	sys := systemPrompt(opts.Vocabulary)
	out := make([]chunkOutcome, len(chunks))

	n := opts.Concurrency
	if n <= 0 {
		n = defaultConcurrency
	}
	if n > len(chunks) {
		n = len(chunks)
	}

	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for i, c := range chunks {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, c alchemy.Chunk) {
			defer wg.Done()
			defer func() { <-sem }()
			// Checked here rather than left to the model: a call this process
			// never made was never paid for, and counting it would overstate
			// the one number §7.2 promises is honest.
			if err := ctx.Err(); err != nil {
				out[i] = chunkOutcome{chunk: c, unread: unreadChunk(c, err.Error())}
				return
			}
			out[i] = extractChunk(ctx, c, sys, opts)
		}(i, c)
	}
	wg.Wait()
	return out
}
