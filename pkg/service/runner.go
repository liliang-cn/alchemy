package service

import (
	"context"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// Runner is what the service needs from whatever actually does the work.
//
// It is declared here, by the consumer, rather than imported from the package
// that satisfies it. That is not only idiomatic Go: it is what keeps the wire
// layer testable without a model, a corpus or a network, and it is what lets
// the service state §7.3's rule — a conflict holds the job — as something a
// pipeline cannot opt out of, because the pipeline never gets to say what
// state a job ended in. It returns a result; the service decides what that
// result means.
//
// The two extra parameters are the two streams §6 says the interface exists
// for. events carries progress out, including a review item the moment it is
// found, so WatchJob can report a conflict in minute three of a two-hour
// import rather than at minute one hundred and twenty. in carries decisions
// back, so a decision reaches an extraction that has not run yet.
//
// A Runner must return when ctx is done. The service cancels ctx when the
// caller cancels the job or the process is shutting down, and a runner that
// ignores it is a job that cannot be stopped.
type Runner interface {
	Run(ctx context.Context, jobID string, spec JobSpec, events chan<- Event, in Inbox) (alchemy.Result, error)
}

// Finisher is the half of the work that can only happen after a person has
// answered, and it is optional.
//
// It exists because of the ordering §5c argues for. Vectors "are recomputed
// for whatever text survives review", so a job that stops for a person stops
// one stage short: pipeline.HeldError.Pending is documented as "complete
// except for the vectors, which §5c will not spend until the text they
// describe has survived". Somebody has to spend them once the decisions are
// in, and it cannot be this package — §6 gives the service endpoint
// descriptions, not models, and building one here would put a provider behind
// the wire layer. The Runner holds the factory, so the Runner is asked.
//
// It is a second interface rather than a method on Runner because Runner is
// what every caller's worker implements — Athanor's is a fake and this
// package's tests are functions — and widening it would break each of them for
// a capability most of them do not have. So the service type-asserts, and a
// Runner that does not satisfy this keeps the behaviour it had before this
// existed.
//
// What that Runner gets, stated so nobody has to discover it: the decided
// graph is delivered, the job succeeds, and it has no vectors. That is not
// silent. Every chunk with no vector is named in Result.Unread with the reason
// — the same shape pkg/embed uses for a batch its endpoint lost — so
// Counts.Vectors is 0, Counts.ChunksUnread is the number of chunks, and a
// caller reading either learns that the graph cannot cite the text its facts
// came from. §5's rule is that source material nothing could read is named
// rather than quietly omitted; a vector nothing could compute is the same rule
// one stage later.
//
// Embed returns the graph it was given, changed only by what it bought. A
// failure returns that graph with an error rather than nothing: the decisions
// on it were made by a person, and throwing their work away because an
// endpoint was unreachable would cost more than the vectors did.
type Finisher interface {
	Embed(ctx context.Context, spec JobSpec, res alchemy.Result) (alchemy.Result, error)
}

// Inbox is the runner's end of the bidirectional review stream.
//
// It is a snapshot rather than a channel because a decision is not an event to
// be consumed once: the runner asks "what do I know now" before each chunk,
// and a reviewer who reconnects and resends is answering the same question
// again, not asking a new one. A channel would make a redelivered decision a
// second decision, and would drop every decision made before the runner
// happened to be listening.
type Inbox interface {
	// Decisions returns every decision recorded for this job so far, in
	// arrival order, deduplicated by the service.
	Decisions() []review.Decision
	// Rules returns the `always` rules those decisions have produced so far
	// (§5c), in the order they were made.
	//
	// It is a second method rather than something the runner derives from
	// Decisions because a rule is a decision *and* the item it was made on:
	// §5c records a rule with the decision that produced it, and the item —
	// its shape, its kind, the sentence the reviewer was reading — is in the
	// queue the service holds and not in the answer the reviewer sent. A
	// runner deriving rules would need the queue too, which is the service's.
	//
	// These are the answers that can reach work that has not run yet. A
	// decision about one record settles that record; a rule is the class, and
	// a class is the only thing a chunk nobody has extracted can be measured
	// against (§6).
	Rules() []review.Rule
}

// Event is one thing worth telling a watcher about. §7.2 and §7.3 between them
// decide the fields: a running cost, and a conflict at the moment it is found.
type Event struct {
	At    time.Time
	Stage string
	// Counts so far. §5: the numbers needed to distrust a graph are not a
	// closing report, they are what an operator watches accumulate.
	Counts alchemy.Counts
	// Conflict is set when this event announces one. §7.3: an operator
	// watching a long import should learn early that it will need them.
	Conflict *alchemy.Conflict
	// ModelCalls is the running total. §7.2: cost is not optimised for, but a
	// job whose bill is growing faster than expected must be cancellable while
	// it runs rather than after it finishes.
	ModelCalls int64
	// ByStage is that total broken down, for the operator who wants to know
	// which stage is spending it.
	ByStage []alchemy.ModelCall
	Message string
	// Item is a question for a person, published the moment the runner finds
	// it rather than collected for the end. A watcher ignores it; the review
	// stream is what it is for.
	Item *review.Item
}

// Source is one spooled upload. §8.4: what the service passes on is a path,
// never bytes — a 10GB dump that is parsed by reading it into a string is a
// service that dies on the first real customer.
type Source struct {
	ID   string
	Kind alchemy.SourceKind
	Name string
	// Path is where the bytes were spooled. The file outlives the upload call
	// and is removed when the job is deleted or expires.
	Path      string
	Size      int64
	MediaType string
}

// Chunking is §7.1's decision, carried per job because the person who knows
// the corpus is the caller.
type Chunking struct {
	Strategy string
	Size     int
	Overlap  int
}

// Model is one endpoint the caller supplied. §6: models are supplied per job,
// not configured globally.
type Model struct {
	Name     string
	Endpoint string
	APIKey   string
	Options  map[string]string
}

// Models is the three a job may supply. An absent OCR is a configuration, not
// an error: §5 says a scanned page is then reported unread.
type Models struct {
	LLM      Model
	Embedder Model
	OCR      Model
}

// JobSpec is everything the runner was asked to do. It carries no transport
// types: a runner built against this never learns that gRPC exists.
type JobSpec struct {
	Sources  []Source
	Ontology string
	// Part names which vocabulary of the ontology this corpus is extracted
	// under and verified against. It is a string rather than an ontology.Part
	// so that the wire layer keeps its one property: a Runner built against
	// this never learns that gRPC exists, and this package never learns what an
	// ontology is. The closed set of names lives in pkg/ontology, and a name
	// outside it is refused there, where the list of what the document actually
	// declares is in hand.
	//
	// Empty means prose. That is a decision and not a fallback: §5 puts
	// documents and entity extraction in the first release, prose is what a
	// document is, and every caller that has ever created a job was creating a
	// prose job. Making the new field required would have made the old requests
	// invalid, and making the zero value mean something other than what every
	// existing caller meant is how a compatible-looking change breaks a running
	// binary.
	Part     string
	Models   Models
	Chunking Chunking
	Review   review.Options
}
