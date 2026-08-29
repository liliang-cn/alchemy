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
	"github.com/liliang-cn/alchemy/pkg/cache"
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
	// Cache is §8.2's content-addressed store, and it is optional. Nil is
	// caching off — the buyer running this for the first time, and every test
	// that has no opinion about it — because a cache is an optimisation and an
	// optimisation that has to be configured before the thing works is not
	// optional at all. See cache.Fetch for what a nil one does.
	Cache cache.Cache
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
	// entities and relations are this chunk's proposal, already carrying
	// identity and provenance. They are held in the finished types rather than
	// as the raw reply because that is what the cache stores: a cache.Entry is
	// entities and relations, so the conversion has to happen before the entry
	// is written and cannot happen again after one is read.
	entities  []alchemy.Entity
	relations []alchemy.Relation
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
	out.entities = entitiesOf(c, r, opts)
	out.relations = relationsOf(c, r, opts)
	return out
}

// errNotBought is how the producer below tells cache.Fetch not to store what
// it just returned. It never reaches a caller: Fetch returns a producer's
// error unchanged, and cachedOutcome turns it back into the outcome the chunk
// actually had.
//
// It exists because a chunk that could not be read has no answer to cache, and
// caching the absence would make that failure permanent for the content
// address — a rate limit at 3am becoming "this paragraph contains nothing",
// for every job that ever sees this paragraph again. That is the one bug worse
// than paying for the call twice.
var errNotBought = errors.New("extract: this chunk produced no answer to cache")

// cachedOutcome is one chunk's work, taken from the cache when it is there and
// bought when it is not.
//
// The address is the key §8.2 names: the chunk text, the model, the ontology
// version and the prompt version. Everything else about a chunk — which source
// it came from, its index, the strategy that cut it — is provenance rather than
// question, so it is restamped on the way out rather than keyed on. See
// adoptedBy.
func cachedOutcome(ctx context.Context, c alchemy.Chunk, sys string, opts Options) chunkOutcome {
	var bought chunkOutcome
	// The error is discarded rather than inspected: errNotBought is the only
	// error this producer can return, and the unread it stands for is already
	// on bought, along with the call that bought it.
	entry, hit, _ := cache.Fetch(ctx, opts.Cache, keyFor(c, opts), func(ctx context.Context) (cache.Entry, error) {
		bought = extractChunk(ctx, c, sys, opts)
		if bought.unread != nil {
			return cache.Entry{}, errNotBought
		}
		return cache.Entry{Entities: bought.entities, Relations: bought.relations, Tokens: bought.tokens}, nil
	})
	if !hit {
		return bought
	}
	// calls and tokens stay zero. §7.2 obliges the job to report what it spent,
	// and a chunk that spent nothing this time is not spend: reporting the
	// original call again would invent a bill for money nobody paid. The number
	// is still in the entry, for a caller that wants to say what the graph cost
	// to produce rather than what this run cost to make.
	return adoptedBy(c, entry, opts)
}

// keyFor is the content address of one chunk's extraction (§8.2).
//
// The ontology is named by its ID rather than by its contents because §5b
// requires that ID to carry a version — "sds@3" — so a vocabulary that changed
// is an ontology that was reversioned, and an ontology that was not
// reversioned is one whose rules did not move.
func keyFor(c alchemy.Chunk, opts Options) cache.Key {
	return cache.Key{
		Chunk:    c.Text,
		Model:    opts.LLM.Name(),
		Ontology: opts.OntologyID,
		Prompt:   PromptVersion,
	}
}

// adoptedBy turns a cached entry into this run's outcome for chunk c.
//
// The model's opinion is kept — the types, the names, the attributes, the
// confidence it gave — and the provenance is rebuilt from the chunk in front of
// us. That is not bookkeeping: §5b makes "every entity and relation can name
// the source, the chunk and the producer it came from" a product guarantee, and
// the address is a hash of the text, so the same paragraph appearing in two
// documents shares one entry. Returning the stored provenance would cite
// whichever document happened to be imported first, which is a citation that
// points at a real document and the wrong one — the failure §5b exists to
// prevent, arriving through an optimisation.
func adoptedBy(c alchemy.Chunk, e cache.Entry, opts Options) chunkOutcome {
	out := chunkOutcome{chunk: c}
	for _, ent := range e.Entities {
		ent.Provenance = provenanceFor(c, opts, ent.Provenance.Confidence)
		out.entities = append(out.entities, ent)
	}
	for _, rel := range e.Relations {
		rel.Provenance = provenanceFor(c, opts, rel.Provenance.Confidence)
		out.relations = append(out.relations, rel)
	}
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
		if len(o.entities) == 0 && len(o.relations) == 0 {
			// Read, and honestly nothing in it. §5 wants this counted rather
			// than inferred from a short entity list.
			res.ChunksEmpty++
			continue
		}
		for _, e := range o.entities {
			m.add(e)
		}
	}
	// Relations are resolved only once every chunk's entities are known; see
	// merger.resolveEnds.
	for _, o := range outcomes {
		if o.unread != nil {
			continue
		}
		for _, r := range o.relations {
			m.addRelation(m.resolveEnds(r))
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
			out[i] = cachedOutcome(ctx, c, sys, opts)
		}(i, c)
	}
	wg.Wait()
	return out
}
