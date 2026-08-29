package pipeline

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// validate refuses a request before anything is opened or bought.
//
// Every check here could be discovered later, in the stage that needs the
// thing that is missing. Doing it first is the point: §5 says an ontology is
// required for document sources and that there is no unconstrained mode, and a
// run that reads a corpus, spends an hour of a caller's model quota and then
// says the vocabulary was missing has already done the expensive half of the
// unconstrained run it was supposed to refuse.
func (r *run) validate() error {
	if len(r.req.Sources) == 0 {
		return fmt.Errorf("pipeline: the job has no sources")
	}
	documents := false
	for _, src := range r.req.Sources {
		switch src.Kind {
		case alchemy.SourceDDL, alchemy.SourceGraph, alchemy.SourceTabular:
		case alchemy.SourceDocument:
			documents = true
		default:
			return fmt.Errorf("pipeline: source %q: unknown source kind %q", src.Name, src.Kind)
		}
		if src.Open == nil {
			return fmt.Errorf("pipeline: source %q: has no Open function, so there is nothing to read", src.Name)
		}
	}
	if documents {
		if r.req.Ontology == nil {
			return fmt.Errorf("pipeline: this job has document sources and no ontology; extraction is constrained by a declared vocabulary and there is no unconstrained mode (DESIGN.md §5)")
		}
		if r.req.Models.LLM == nil {
			return fmt.Errorf("pipeline: this job has document sources and no LLM; prose is the one kind that needs a model, and a stage given a nil model fails rather than degrading")
		}
	}
	// The vocabulary is resolved here rather than at each use, so that a part
	// the ontology does not declare is one error at the start instead of a
	// different error in the extractor and another in the verifier.
	if r.req.Ontology != nil {
		v, err := r.req.Ontology.Vocabulary(r.req.Part)
		if err != nil {
			return fmt.Errorf("pipeline: %w", err)
		}
		r.vocabulary = v
		r.ontologyID = r.req.Ontology.ID
	}
	return nil
}
