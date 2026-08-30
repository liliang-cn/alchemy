// Package pipeline is DESIGN.md §3's diagram made real: sources in, a graph
// out, with every stage between them run in the order the design argues for.
//
//	Source → (reader) → chunks → Extractor → Verifier → [Review hold] → Embedder → Result
//
// Everything it does is delegation. The readers, the chunker, the extractor,
// the verifier, the review queue and the embedder are all finished packages
// with their own tests; what is not in any of them, and is the whole of this
// package's own work, is four obligations that only something holding the
// entire job can keep:
//
//   - Routing. A structured source states its own entities and relations, so
//     it goes to its reader and never to a model (§2.1's first lesson). Only
//     prose is extracted.
//   - Verification over the whole job. §8.1: a conflict is two sources
//     disagreeing, and only something that sees both can notice. Every source
//     is read first and the accumulated graph is checked once.
//   - The hold. §7.3: a conflict is a question, and a job that found one does
//     not return a finished graph — whether or not the caller asked for review.
//   - The numbers. §5 makes Counts the obligation that justifies the release.
//     Each stage produces a part of it and none of them can produce the whole;
//     this is where it is finally assembled, from the slices it describes.
package pipeline

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
	"github.com/liliang-cn/alchemy/pkg/cache"
	"github.com/liliang-cn/alchemy/pkg/chunk"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/source/tabular"
)

// stageExtract and the other stage names are what an Event carries and what
// aggregate keys the cost report on. They match the names the stages
// themselves already use in alchemy.ModelCall, so a caller reading the cost
// report and a caller watching the progress stream are reading one vocabulary.
const (
	stageRead    = "read"
	stageExtract = "extract"
	stageVerify  = "verify"
	stageReview  = "review"
	stageEmbed   = "embed"
)

// Source is one file the job was given.
type Source struct {
	// Name identifies the source in provenance, in violations and in errors.
	Name string
	// Kind decides which reader runs; it is never sniffed from the bytes,
	// because a corpus routed by a guess is a corpus whose DDL can end up in
	// front of a model.
	Kind alchemy.SourceKind
	// Open is called when the source is read, rather than the bytes being a
	// field here, so that a corpus is never all in memory at once (§8.4). It is
	// called at most once per run and the pipeline closes what it returns.
	Open func() (io.ReadCloser, error)
}

// Request is one job.
type Request struct {
	Sources []Source
	// Ontology is the vocabulary extraction is constrained by and verification
	// checks against. It is required for document sources and there is no
	// unconstrained mode (§5); for a job made only of structured sources it is
	// optional, because nothing was inferred for it to constrain.
	Ontology *ontology.Ontology
	// Part is which vocabulary this corpus is. One job is one corpus under one
	// part: verify.Input is documented as one job's extraction checked against
	// one part, and a job that mixed two would have to choose which of them a
	// cross-source conflict was a conflict under.
	Part ontology.Part
	// Models are the caller's endpoints (§6). Any may be nil; a stage that
	// needs a nil model fails loudly rather than degrading.
	Models alchemy.Models
	// Chunking is the strategy this corpus is split by (§7.1). The zero value
	// is chunk.Auto, which is §7.1's default.
	Chunking chunk.Options
	// Reviewing is review mode (§5c). It is off by default and it does not
	// change what holds the job: a conflict holds it either way.
	Reviewing bool
	// Inbox is this job's review conversation as it stands, asked repeatedly
	// while the job runs rather than read once before it starts.
	//
	// It is an interface and not two slices because of §6's first reason for
	// choosing gRPC: a person's decisions take effect on work still running,
	// so an `always` rule made while the corpus is being read reaches the
	// chunks that have not been extracted yet. See standing.go, and
	// Answered for the caller that has a fixed set and no conversation.
	//
	// Nil is a job nobody is reviewing.
	Inbox Inbox
	// MinConfidence is the line below which an inferred edge is queued as
	// low-confidence in review mode. Zero queues none: pkg/review refuses to
	// invent a threshold on a caller's behalf, and so does this.
	MinConfidence float64
	// Budget bounds how many calls are in flight against one model endpoint
	// (§8.2). It is optional, and it reaches the stages the only way pkg/budget
	// allows: the models are wrapped before the first stage runs, so no stage
	// knows a budget exists. Nil is a single node with no declared endpoint
	// limit, which is what a buyer evaluating the product runs.
	Budget budget.Budget
	// Cache is §8.2's content-addressed store for extraction results, and it
	// is optional. Nil is caching off.
	//
	// It belongs to the job rather than to the process because the thing that
	// knows a run is a resumption — the same corpus, after a crash or an
	// expired lease (§8.3) — is the caller that scheduled it, and because a
	// shared store is the cluster's, not this node's. §7.2 said cost is not
	// optimised for; §8.2 draws the line that does not cross: "paying twice for
	// the identical call after a crash is a bug."
	Cache cache.Cache
	// Mapping is the caller's column mapping for tabular sources. Supplying it
	// is §2.1's determinism-first rule taken at its word: a mapping the caller
	// states is a mapping nobody guessed, and the run makes no model call and
	// records no Guess.
	Mapping *tabular.Mapping
}

// Run executes the whole job and returns the graph.
//
// It returns a graph only with a nil error. Two failures are distinguished and
// both are typed rather than described in a string: a job that found a
// conflict returns a *HeldError carrying what it had, because §7.3 says a
// question has to be asked of someone; anything else returns a Result carrying
// only what the job spent, because §7.2's promise that cost is never hidden is
// least dispensable on the run that failed.
//
// events may be nil, which is a caller that does not want progress. When it is
// not nil, Run closes it before returning, so a caller can range over it —
// which means Run owns that channel for the duration of the call, and each Run
// should be given its own. The caller must consume it until it closes, or
// cancel ctx: every stage change and every conflict is delivered, including
// the ones still queued when the job ends. See emitter.go for why that is
// worth a contract.
func Run(ctx context.Context, req Request, events chan<- Event) (alchemy.Result, error) {
	run := &run{req: req, events: newEmitter(ctx, events)}
	// The stream is closed on every path out, including the ones that refuse
	// the request before anything is read: a caller ranging over the channel
	// should finish when the job does, whatever ended it.
	defer run.events.close()

	if err := run.validate(); err != nil {
		return alchemy.Result{}, err
	}
	run.budget()
	if err := run.readSources(ctx); err != nil {
		return run.spent(), err
	}
	if err := run.extract(ctx); err != nil {
		return run.spent(), err
	}
	// A job whose caller has gone does not go on to verify, review and embed
	// what it managed to read: those stages are where the remaining money is.
	if err := run.stopped(ctx); err != nil {
		return run.spent(), err
	}
	rep := run.verify()
	res, queue, err := run.reviewJob(rep, run.result())
	if err != nil {
		return run.spent(), err
	}
	// §7.3's refusal, and the reason Run's second return value is not just an
	// error: a conflict nobody has answered means this job does not finish,
	// whether or not the caller asked for review. review.Held is the test
	// rather than "are there conflicts" because a conflict a person has
	// decided stays in the result, carrying their name, and a job that stayed
	// stuck on an answered question would be a queue nobody could empty.
	if open, unanswered := review.Held(res), openQuestions(queue, run.decided); len(open) > 0 || (req.Reviewing && len(unanswered) > 0) {
		return alchemy.Result{}, &HeldError{Conflicts: open, Queue: unanswered, Pending: run.finish(res)}
	}
	res, err = run.embedSurvivors(ctx, res)
	if err != nil {
		return run.spent(), err
	}
	return run.finish(res), nil
}

// run is the state one job accumulates. It exists because §8.1 says the
// coordinator holds the accumulating index and is the only place conflicts are
// decided: every stage below writes into one of these, and the assembly at the
// end reads nothing else.
type run struct {
	req    Request
	events *emitter
	// stage is what the job is doing, so that an event raised deep in a
	// reader can say where it came from without every function passing it.
	stage string

	vocabulary ontology.Vocabulary
	ontologyID string

	entities  []alchemy.Entity
	relations []alchemy.Relation
	// chunks are job-wide and renumbered as they are adopted; see adopt.
	chunks []alchemy.Chunk
	// docs is the chunks of each document source, kept per source because
	// extraction runs per source: merging two sources' proposals inside one
	// extractor would settle their disagreement before the verifier — the one
	// thing §8.1 says only the coordinator can notice — ever saw two claims.
	docs []docSource

	// decided is the decisions the review stage was run with. It is kept so
	// that the hold below and the apply above are answering from one reading
	// of the conversation: a decision that arrived between the two would
	// otherwise resolve an item in the graph and leave the job held on it.
	decided []review.Decision

	unread     []alchemy.Unread
	violations []alchemy.Violation
	conflicts  []alchemy.Conflict
	guesses    []alchemy.Guess
	modelCalls []alchemy.ModelCall
	// chunksEmpty is the running total of chunks that produced nothing, summed
	// over the stages that can tell. §5 makes it one of the numbers a caller
	// needs in order to distrust the graph.
	chunksEmpty int
	// dropped is how many records a standing rule removed without anybody
	// being asked about them, over the whole job. It is atomic because the
	// extract stage settles each chunk's proposal on its own goroutine, and it
	// is accumulated rather than recomputed because a record a rule removed
	// leaves nothing behind to count (see alchemy.Counts.Dropped).
	dropped atomic.Int64
}

// docSource is one document's chunks, waiting for the extract stage.
type docSource struct {
	name   string
	chunks []alchemy.Chunk
}

// adopt gives chunks their job-wide index and files them.
//
// The index a chunk arrives with is per source, and a job has several sources:
// left alone, two chunks numbered 0 would land in one Result, an
// alchemy.Vector naming chunk 0 would name both, and the provenance of an
// entity "from chunk 0" would be a citation to two different pages. Renumbering
// before extraction is what makes Provenance.Chunk and Vector.Chunk name one
// span of one file. Start and End are untouched: they are offsets into the
// source, and the source did not move.
func (r *run) adopt(chunks []alchemy.Chunk) []alchemy.Chunk {
	out := make([]alchemy.Chunk, len(chunks))
	for i, c := range chunks {
		c.Index = len(r.chunks) + i
		out[i] = c
	}
	r.chunks = append(r.chunks, out...)
	return out
}

// spend records what a stage bought. §7.2: nothing a job spent is lost, least
// of all by the stage that failed.
func (r *run) spend(calls ...alchemy.ModelCall) {
	r.modelCalls = append(r.modelCalls, calls...)
}

// readSources runs each source through the reader its kind names.
func (r *run) readSources(ctx context.Context) error {
	r.stage = stageRead
	for _, src := range r.req.Sources {
		// Checked per source rather than only inside the readers: a
		// deterministic reader has no model call to be cancelled at, so a
		// cancelled job would otherwise read the whole corpus before anyone
		// asked whether it should.
		if err := r.stopped(ctx); err != nil {
			return err
		}
		r.emit(Event{Kind: EventStage, Stage: stageRead, Source: src.Name})
		if err := r.read(ctx, src); err != nil {
			return fmt.Errorf("pipeline: source %q: %w", src.Name, err)
		}
		r.progress(stageRead, src.Name)
	}
	return nil
}

func (r *run) result() alchemy.Result {
	return alchemy.Result{
		Entities:   r.entities,
		Relations:  r.relations,
		Chunks:     r.chunks,
		Conflicts:  r.conflicts,
		Violations: r.violations,
		Guesses:    r.guesses,
		Unread:     r.unread,
		ModelCalls: aggregate(r.modelCalls),
	}
}

// budget wraps the caller's models so that every call this job makes leases a
// slot first.
//
// Wrapping, not knowing. §8.2 is explicit that a stage is budgeted by being
// handed a wrapped model, and the alternative — a stage that consults a budget
// itself — would put a cluster-wide concern into pkg/extract, which would then
// need a reason to care about how many other nodes exist.
//
// A nil model stays nil. A wrapper around nothing is not nothing: it satisfies
// the interface, so the check that a document job has an LLM would pass and
// the failure would move from validate to the first call.
func (r *run) budget() {
	b := r.req.Budget
	if b == nil {
		return
	}
	if r.req.Models.LLM != nil {
		r.req.Models.LLM = budget.WrapLLM(r.req.Models.LLM, b)
	}
	if r.req.Models.Embedder != nil {
		r.req.Models.Embedder = budget.WrapEmbedder(r.req.Models.Embedder, b)
	}
	if r.req.Models.OCR != nil {
		r.req.Models.OCR = budget.WrapOCR(r.req.Models.OCR, b)
	}
}

// stopped reports that the caller has given up on this job.
//
// §7.2's running cost exists so that a job can be cancelled while it runs, and
// a cancel that is noticed only at the end of the stage that was running when
// it arrived is a cancel that arrived too late to be worth offering. So it is
// asked between stages and between sources as well as inside the stages that
// pass ctx to a model.
func (r *run) stopped(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("pipeline: stopped during %s: %w", r.stage, err)
	}
	return nil
}

// finish fills in the fields no single stage owns: what could not be read,
// what the job spent, and the numbers §5 makes the obligation that justifies
// the release. They are written here, once, from the slices they describe —
// a count maintained by increments drifts away from its subject exactly once
// and then lies forever.
func (r *run) finish(res alchemy.Result) alchemy.Result {
	res.Unread = r.unread
	res.ModelCalls = aggregate(r.modelCalls)
	res.Counts = r.counts(res)
	return res
}

// spent is what a failed job returns: what it cost and nothing else.
//
// The graph is deliberately not in it. §7.2 says a job reports what it made in
// model calls even when it fails, and §7.3 says a job that did not finish must
// not look like one that did; returning the half-built graph alongside the
// error would keep the first promise by breaking the second, since the only
// thing standing between a caller and a partial graph would be their habit of
// checking err. A graph leaves this package two ways: a nil error, or
// HeldError.Pending, which has to be named to be reached.
func (r *run) spent() alchemy.Result {
	return alchemy.Result{ModelCalls: aggregate(r.modelCalls)}
}
