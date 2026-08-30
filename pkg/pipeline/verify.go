package pipeline

import "github.com/liliang-cn/alchemy/pkg/verify"

// verifyJob checks the whole job at once and returns the report the review
// queue is built from.
//
// At once is the entire point. §8.1: "a conflict is two sources disagreeing —
// and only something that sees both can notice", so every source is read
// before anything is checked, and the check is over the accumulated graph
// rather than over each source as it arrives. Per-source verification would be
// cheaper, would parallelise, and would find every violation — and would miss
// exactly the class of failure this design exists to prevent.
//
// What comes back is not quite what verify.Check returned. The report's
// finding slices are replaced by the job's, which are the verifier's findings
// appended to the ones the deterministic readers already made: a foreign key
// pointing at a table the file never defines is a violation nobody re-derives
// later, and a table declared twice is a conflict that has to hold the job the
// same as any other. review.Queue indexes into these slices and review.Apply
// indexes into the result's, so they have to be one list — which they are,
// because the result is assembled from the same fields.
func (r *run) verify() verify.Report {
	r.stage = stageVerify
	r.emit(Event{Kind: EventStage, Stage: stageVerify})
	rep := verify.Check(verify.Input{
		Entities:   r.entities,
		Relations:  r.relations,
		Vocabulary: r.vocabulary,
		OntologyID: r.ontologyID,
	})
	// Check returns the graph with types canonicalised, which is what the rest
	// of the job carries: a graph holding Cluster, cluster and CLUSTER is one
	// a traversal keyed on the type name only finds a third of.
	r.entities = rep.Entities
	r.relations = rep.Relations

	// A job with no ontology declared no rules, so nothing in it can have
	// broken one. Check still computes violations, because it was handed an
	// empty vocabulary and an empty vocabulary permits nothing — but every one
	// of them says "the vocabulary you did not supply does not declare this",
	// which is a fact about the request rather than about the data. The
	// conflicts are kept, because two sources disagreeing needs no vocabulary
	// to be a question.
	if r.req.Ontology != nil {
		r.violations = append(r.violations, rep.Violations...)
	}
	r.found(rep.Conflicts...)
	// Kept whatever the ontology said, for the same reason the conflicts are:
	// two spellings of one thing is not a rule anybody declared and not a rule
	// anybody broke, so a job with no vocabulary is exactly as entitled to the
	// finding as one with a strict vocabulary.
	r.duplicates = append(r.duplicates, rep.Duplicates...)

	rep.Violations = r.violations
	rep.Conflicts = r.conflicts
	rep.Duplicates = r.duplicates
	r.progress(stageVerify, "")
	return rep
}
