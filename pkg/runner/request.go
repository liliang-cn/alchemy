package runner

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/chunk"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/pipeline"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/service"
)

// buildRequest turns one JobSpec into one pipeline.Request.
//
// It is the whole translation this package exists for, and it deliberately
// refuses nothing that the pipeline already refuses: an absent ontology, a job
// with no sources and an unknown source kind are all rules pipeline.validate
// owns, and a second copy here would be a second place for them to drift.
func buildRequest(jobID string, spec service.JobSpec, in service.Inbox, configured []review.Rule) (pipeline.Request, error) {
	onto, err := ontologyOf(spec.Ontology)
	if err != nil {
		return pipeline.Request{}, err
	}
	return pipeline.Request{
		// The service's job ID, carried so that the graph a store is handed can
		// say which run produced it. It reached this function as a parameter of
		// Run and went no further, which is why four stores each had to invent
		// a result identity of their own; see alchemy.Result.Job.
		Job:      jobID,
		Sources:  sourcesOf(spec.Sources),
		Ontology: onto,
		Part:     partOf(spec.Part),
		Chunking: chunkingOf(spec.Chunking),

		Reviewing:     spec.Review.Reviewing,
		MinConfidence: spec.Review.MinConfidence,
		// Not a snapshot. The pipeline asks this while it runs, which is how a
		// decision reaches an extraction that has not started (§6). See inbox.
		Inbox: inboxOf(spec.Review.Rules, configured, in),
	}, nil
}

// partOf is which vocabulary of the ontology this corpus is read under.
//
// Empty means prose, and that default is load-bearing rather than tidy. §5's
// first release is documents and entity extraction; a document is prose; and
// every job ever created against this service was created before this field
// existed, so the meaning of "unstated" has to be the meaning those jobs
// already had. A default of "refuse" would be correct in the abstract and would
// break every running client, and a default of "whatever the ontology declares
// first" would make the vocabulary a job is checked against depend on the order
// somebody wrote a JSON file in.
//
// A name that is not a part is passed through rather than corrected here.
// ontology.Vocabulary refuses it, and it refuses it holding the list of parts
// the document does declare — which is the error message the operator needs,
// and one this function could not write.
func partOf(name string) ontology.Part {
	if name == "" {
		return ontology.PartProse
	}
	return ontology.Part(name)
}

// chunkingOf carries §7.1's choice across unchanged, zeroes included.
//
// A zero field is deliberately not filled in here. chunk.Options owns the
// defaults and one of them is load-bearing — overlap is non-zero unless the
// caller says chunk.NoOverlap — so a translator that helpfully substituted its
// own numbers would be a second opinion about the one setting §7.1 calls the
// cheap insurance against the split-relation problem.
func chunkingOf(c service.Chunking) chunk.Options {
	return chunk.Options{
		Strategy:  chunk.Strategy(c.Strategy),
		MaxTokens: c.Size,
		Overlap:   c.Overlap,
	}
}

// inboxOf joins the places a standing answer can come from.
//
// The job's own rules were supplied when it was created: §4 says the service
// holds no policy between jobs, so a caller that learned something last week
// hands it back with this week's request, and those rules are in force before
// the first chunk is read. Behind them come the rules this process was started
// with — the operator's own policy, which a job that says nothing about rules
// still runs under, and which is the only place an unattended nightly import
// can carry policy at all. The service's inbox is this job's conversation as
// it happens.
//
// The job's own come first because review takes the first rule that covers an
// item, and that is what decides which rule the provenance credits. A policy
// stated about this job is closer to the work than one stated about the
// deployment, and is the one a reader chasing a suppression should be sent to. Both are `always` rules and both are answered the same way; what
// differs is only when they were made, and by the time a chunk is measured
// against one that no longer matters.
//
// A nil service inbox is a caller with nowhere for a decision to come from —
// the first run of a job that has never been held, or an unattended import —
// and is not a failure. It is still an inbox, because the job's own rules
// still apply.
func inboxOf(given, configured []review.Rule, in service.Inbox) pipeline.Inbox {
	stated := append(append(make([]review.Rule, 0, len(given)+len(configured)), given...), configured...)
	if in == nil {
		return pipeline.Answered(nil, stated)
	}
	return inbox{given: stated, live: in}
}

// inbox is the service's live conversation with the job's own rules in front
// of it.
type inbox struct {
	given []review.Rule
	live  service.Inbox
}

func (i inbox) Decisions() []review.Decision { return i.live.Decisions() }

// Rules puts the supplied rules first, and the order is not arbitrary:
// review.ruleFor takes the first rule that covers an item, so a policy the
// caller stated when they created the job is the one that gets named as
// having answered it. Two rules covering one item are two people who wrote
// down the same policy — pkg/review's words — and this decides which of them
// the provenance credits.
func (i inbox) Rules() []review.Rule {
	live := i.live.Rules()
	if len(i.given) == 0 {
		return live
	}
	out := make([]review.Rule, 0, len(i.given)+len(live))
	return append(append(out, i.given...), live...)
}

// ontologyOf parses the JSON document the caller supplied.
//
// An empty document is nil rather than an error. §5 makes an ontology required
// for document sources and optional for a job made only of structured ones,
// and pipeline.validate already states that rule in the one place that can see
// which kinds the job actually has. Restating it here would give the design one
// sentence and two enforcers, and the two would eventually disagree.
//
// A malformed document, on the other hand, is a mistake nobody else will catch
// in time: the pipeline would take the nil for "no ontology supplied" and go on
// to refuse a document job for the wrong reason, telling the caller they forgot
// a vocabulary they did in fact send.
func ontologyOf(doc string) (*ontology.Ontology, error) {
	if strings.TrimSpace(doc) == "" {
		return nil, nil
	}
	o, err := ontology.Load(strings.NewReader(doc))
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}
	return o, nil
}

// sourcesOf turns spooled paths into readers that have not read anything yet.
//
// §8.4: "a big source is not held in memory". The Open closure is what keeps
// that promise across the seam — the pipeline calls it when it reaches the
// source, one at a time, and closes what it gets, so a corpus of a hundred
// files has one file open at a time rather than a hundred buffers.
func sourcesOf(spooled []service.Source) []pipeline.Source {
	if len(spooled) == 0 {
		return nil
	}
	out := make([]pipeline.Source, 0, len(spooled))
	for _, s := range spooled {
		path := s.Path
		out = append(out, pipeline.Source{
			Name: nameOf(s),
			Kind: s.Kind,
			Open: func() (io.ReadCloser, error) { return os.Open(path) },
		})
	}
	return out
}

// nameOf is what this source will be called in every violation, conflict and
// provenance record the job produces. The upload's name is the one a person
// recognises; the ID is the fallback because §5b's promise is that every fact
// names its source, and "" names nothing.
func nameOf(s service.Source) string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}
