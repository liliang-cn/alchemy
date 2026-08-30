package service

import (
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// recheck re-derives the conflicts of a graph a decision has changed.
//
// A decision does not only answer a question, it edits the graph: a merge
// joins two nodes, a rejection removes records, an edit retypes them. The
// findings on a result are a function of that graph, and they were computed
// before any of it happened — so a merge that creates a contradiction creates
// it after the only pass that could have seen it.
//
// That is not a corner case, it is the ordinary path for the thing this all
// exists for. Two producers name one company `org:northgate` and
// `organization:northgate`; each carries a CHIEF_TECHNOLOGY_OFFICER_OF edge from
// a different person; the ontology says a company has one of those. No
// cardinality conflict is possible while they are two nodes, and the merge
// that makes them one is a decision. Measured on that corpus: the duplicate is
// found, the merge is available, and without this the job then finishes clean
// holding two CTOs — which is precisely the stale graph the constraint was
// added to make impossible.
//
// Only conflicts are adopted. They are what §7.3 acts on, and re-deriving
// everything would overwrite findings review.Apply deliberately shaped —
// violations it carried forward with the records they belong to, guesses a
// rule took away. A conflict is different because it is a statement about a
// pair of records rather than about one, and a merge is exactly the operation
// that makes a new pair.
//
// Answered conflicts do not come back, and nothing here has to arrange that.
// verify.Check builds each conflict out of the records that made it, so a
// conflict re-derived from records a decision has stamped carries that stamp
// in its Left and Right provenance — and alchemy.Result.Held, which is what
// holds the job, already skips a conflict either of whose sides names a
// reviewer. A reviewer who accepts both sides of a disagreement keeps both
// records and the conflict re-appears answered, which is the correct reading
// of what they did.
//
// A job with no ontology is left alone. Cardinality is the only new conflict a
// merge can produce that was not already derivable, it is a vocabulary rule,
// and re-running the checker with an empty vocabulary would report every type
// in the graph as undeclared.
func (s *Server) recheck(r *jobRun, out alchemy.Result) alchemy.Result {
	vocabulary, id, ok := vocabularyFor(r.spec.Ontology, r.spec.Part)
	if !ok {
		return out
	}
	rep := verify.Check(verify.Input{
		Entities:   out.Entities,
		Relations:  out.Relations,
		Vocabulary: vocabulary,
		OntologyID: id,
	})
	out.Conflicts = rep.Conflicts
	out.Counts.Conflicts = len(out.Conflicts)
	return out
}

// vocabularyFor reads one part out of a job's ontology document.
//
// A document that will not parse returns false rather than an error, and the
// job goes on with the findings it had. This runs while a person is answering
// a queue, and the ontology it is re-reading is the same one the job was
// created with and already parsed once — so a failure here means something
// changed underneath a running job, and refusing the decision would strand a
// reviewer in front of a question they cannot answer.
func vocabularyFor(document, part string) (ontology.Vocabulary, string, bool) {
	if strings.TrimSpace(document) == "" {
		return ontology.Vocabulary{}, "", false
	}
	ont, err := ontology.Load(strings.NewReader(document))
	if err != nil {
		return ontology.Vocabulary{}, "", false
	}
	v, err := ont.Vocabulary(partAsserted(part))
	if err != nil {
		return ontology.Vocabulary{}, "", false
	}
	return v, ont.ID, true
}

// discovered re-checks the decided graph and returns the pending result with
// any conflict that was not already in it, plus whether it grew.
//
// Identity is the conflict's kind and subject together, which is what
// review.Queue already keys an item on: two claims about one subject under one
// kind are one question however many times it is re-derived. Comparing the
// whole Conflict would re-add a question on every decision, because Left and
// Right carry provenance that a decision stamps.
func (s *Server) discovered(r *jobRun, pending, decided alchemy.Result) (alchemy.Result, bool) {
	fresh := s.recheck(r, decided).Conflicts
	if len(fresh) == 0 {
		return pending, false
	}
	known := make(map[string]bool, len(pending.Conflicts))
	for _, c := range pending.Conflicts {
		known[string(c.Kind)+"\x00"+c.Subject] = true
	}
	added := false
	for _, c := range fresh {
		if known[string(c.Kind)+"\x00"+c.Subject] {
			continue
		}
		known[string(c.Kind)+"\x00"+c.Subject] = true
		pending.Conflicts = append(pending.Conflicts, c)
		added = true
	}
	if added {
		pending.Counts.Conflicts = len(pending.Conflicts)
	}
	return pending, added
}

// unanswered counts the conflicts actually holding a job: the ones on the
// pending result that §7.3 stops for, minus the ones somebody has answered.
//
// It is one function because it was two numbers that disagreed. GetResult's
// refusal read len(pending.Held()) and ListFindings' holding count read the
// same thing, and both were computed off a result whose conflicts had no
// decision stamped on them — decisions live in the hub and reach the graph
// only when review.Apply runs. So a job held by one unanswered conflict
// reported "0 conflict(s) are unanswered" while refusing to hand over its
// result, which is the worst kind of wrong: a refusal whose stated reason
// contradicts the refusal.
//
// The queue is what says whether a conflict was answered, because the queue is
// what a reviewer was shown and a decision names an item. Held() is what says
// whether it still counts — a conflict whose provenance already carried a
// reviewer, from a decision made before this job ran, is not a question this
// job is entitled to ask again.
func unanswered(r *jobRun) int {
	res, ok := r.pending()
	if !ok {
		return 0
	}
	held := make(map[string]bool)
	for _, c := range res.Held() {
		held[c.Subject] = true
	}
	decided := make(map[string]bool)
	for _, d := range r.hub.Decisions() {
		decided[d.ItemID] = true
	}
	n := 0
	for _, it := range r.hub.queue() {
		if it.Kind == review.KindConflict && held[it.Subject] && !decided[it.ID] {
			n++
		}
	}
	return n
}

// openItems counts the queue a reviewer still has to work through, which is
// the other reason a job sits at NEEDS_REVIEW.
//
// It is not unanswered's number and must not be confused with it. Conflicts
// hold a job whether or not review was asked for (§7.3); everything else in
// the queue holds it only because the caller turned review on. Reporting one
// as the other is how a person ends up looking for a contradiction in a graph
// that has none.
func openItems(r *jobRun) int {
	decided := make(map[string]bool)
	for _, d := range r.hub.Decisions() {
		decided[d.ItemID] = true
	}
	n := 0
	for _, it := range r.hub.queue() {
		if !decided[it.ID] {
			n++
		}
	}
	return n
}
