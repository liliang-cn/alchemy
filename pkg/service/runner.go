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
