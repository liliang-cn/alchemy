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
func buildRequest(spec service.JobSpec, in service.Inbox) (pipeline.Request, error) {
	onto, err := ontologyOf(spec.Ontology)
	if err != nil {
		return pipeline.Request{}, err
	}
	return pipeline.Request{
		Sources:  sourcesOf(spec.Sources),
		Ontology: onto,
		Part:     ontology.PartProse,
		Chunking: chunkingOf(spec.Chunking),

		Reviewing:     spec.Review.Reviewing,
		MinConfidence: spec.Review.MinConfidence,
		Rules:         spec.Review.Rules,
		// Read once, here, at the start of the run. See Run for what that
		// costs and what it does not.
		Decisions: decisionsOf(in),
	}, nil
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

// decisionsOf takes the snapshot of what a person has already answered.
//
// A nil Inbox is a caller with nowhere for decisions to come from — the first
// run of a job that has never been held — and is not a failure.
func decisionsOf(in service.Inbox) []review.Decision {
	if in == nil {
		return nil
	}
	return in.Decisions()
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
