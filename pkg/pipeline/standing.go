package pipeline

import (
	"sort"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/extract"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// Inbox is the live state of this job's review conversation.
//
// It is asked rather than handed over, and that is the whole of DESIGN.md §6's
// first reason for choosing gRPC: "a person working a queue wants items as they
// are found and wants their decisions to take effect on work still running."
// A slice on the Request is the conversation as it stood when the job started,
// and a job that reads one has a person talking to a recording.
//
// A snapshot, not a channel, for the reason pkg/service gives for the same
// shape: a decision is not an event to be consumed once. A stream that
// reconnects redelivers, and a redelivered answer is the same answer.
//
// Nil is a run with nobody reviewing it, which is every unattended import.
type Inbox interface {
	// Decisions is every answer recorded for this job so far.
	Decisions() []review.Decision
	// Rules is every `always` rule in force (§5c). These are the ones that can
	// reach a chunk that has not been extracted yet.
	Rules() []review.Rule
}

// Answered is an Inbox for a caller that has a fixed set of answers and no
// conversation in progress: a job resumed with what was decided last time, and
// every test that is not about liveness.
func Answered(decisions []review.Decision, rules []review.Rule) Inbox {
	return fixed{decisions: decisions, rules: rules}
}

type fixed struct {
	decisions []review.Decision
	rules     []review.Rule
}

func (f fixed) Decisions() []review.Decision { return f.decisions }
func (f fixed) Rules() []review.Rule         { return f.rules }

// decisions and rules are the run's only readers of the inbox. They exist so
// that a nil one is answered in one place rather than guarded at each use.
func (r *run) decisions() []review.Decision {
	if r.req.Inbox == nil {
		return nil
	}
	return r.req.Inbox.Decisions()
}

func (r *run) rules() []review.Rule {
	if r.req.Inbox == nil {
		return nil
	}
	return r.req.Inbox.Rules()
}

// standing is what the extractor is given so that a rule made in minute three
// reaches the chunk read in minute four.
//
// A nil Inbox produces a nil Standing rather than a function that always
// returns nothing. The difference is not efficiency: pkg/extract's determinism
// test is a claim about a run with no Standing at all, and a run that has one
// which happens to be empty is a different run to reason about.
func (r *run) standing() extract.Standing {
	if r.req.Inbox == nil {
		return nil
	}
	return func() extract.Settled {
		rules := r.rules()
		// One rendering, asked for once, and everything about this chunk comes
		// out of it: the lines the model is shown, the name its records will
		// carry, and the set the result will resolve that name through. That
		// is extract.Settled's own argument taken one step further — a chunk
		// whose provenance named a policy its prompt never carried is the
		// failure, and it is impossible when the prompt and the name are two
		// halves of one object.
		set := review.InForce(rules)
		if set.Name == "" {
			// No rule with a shape is in force, which is a run nobody has
			// decided anything for. It gets the zero snapshot rather than a
			// filter that would match nothing: see the comment on standing for
			// why an empty run and a run with an empty policy are different
			// runs to reason about.
			return extract.Settled{}
		}
		r.remember(set)
		return extract.Settled{
			Told:  toldOf(set),
			Named: set.Name,
			Filter: func(_ alchemy.Chunk, ents []alchemy.Entity, rels []alchemy.Relation) ([]alchemy.Entity, []alchemy.Relation) {
				return r.settle(rules, ents, rels)
			},
		}
	}
}

// remember files a policy this job actually asked a chunk under.
//
// Recorded here rather than assembled at the end from the inbox, because the
// inbox at the end is not the policy any particular chunk ran under: §6's
// whole point is that the set grows while the job runs, so the last reading of
// it would describe the last chunk and misdescribe every earlier one. What a
// record names has to be a set that was really in force at the moment it was
// asked, and this is the only moment that is known.
//
// Locked because chunks are extracted concurrently, and by name because the
// same policy is asked for once per chunk and is one set however many chunks
// saw it — which is the entire saving.
func (r *run) remember(set alchemy.RuleSet) {
	r.setsMu.Lock()
	defer r.setsMu.Unlock()
	if r.sets == nil {
		r.sets = map[string]alchemy.RuleSet{}
	}
	r.sets[set.Name] = set
}

// ruleSets is every policy this job's records were extracted under.
//
// Ordered by name, which is not the order they came into force. Chunks are
// extracted concurrently, so the order two policies were first *observed* in
// depends on a scheduler, and a result field that came back in a different
// order on every run would make two runs of one corpus look different for no
// reason anybody could act on. The order they came into force is recoverable
// anyway, by a reader who cares: a set is a subset of the ones that follow it.
func (r *run) ruleSets() []alchemy.RuleSet {
	r.setsMu.Lock()
	defer r.setsMu.Unlock()
	out := make([]alchemy.RuleSet, 0, len(r.sets))
	for _, set := range r.sets {
		out = append(out, set)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// settle is the guarantee half of §6's sentence: a proposal a standing rule
// already answers does not enter the graph as an open question.
//
// Nothing here decides what a rule means. The chunk's proposal is checked
// against the same vocabulary the job is checked against, ranked into the same
// queue a person would have been shown, and the items a rule already covers
// are handed to review.Apply — pkg/review's own machinery, doing pkg/review's
// own job, on one chunk instead of on a whole result. A private
// re-implementation of "what an `always` does" would be a second mechanism,
// and the two would answer the same question differently on the day it
// mattered.
//
// Reviewing is true regardless of the job's own review mode, and that is not
// the same as turning review on. It asks for the full set of questions so that
// the ones already answered can be recognised; a rule a person wrote down
// stops applying to nothing because tonight's import ran unattended.
//
// Chunks already extracted are not reachable from here, by construction: this
// is handed one chunk's proposal, before it is appended to the job. A rule
// learned at chunk forty leaves chunk three exactly as chunk three was, which
// is the honest record of a run in which chunk three genuinely came first.
func (r *run) settle(rules []review.Rule, ents []alchemy.Entity, rels []alchemy.Relation) ([]alchemy.Entity, []alchemy.Relation) {
	rep := verify.Check(verify.Input{
		Entities:   ents,
		Relations:  rels,
		Vocabulary: r.vocabulary,
		OntologyID: r.ontologyID,
	})
	res := alchemy.Result{
		Entities:   rep.Entities,
		Relations:  rep.Relations,
		Violations: rep.Violations,
		Conflicts:  rep.Conflicts,
	}
	var answered []review.Item
	for _, it := range review.Queue(rep, res, review.Options{
		Reviewing:     true,
		MinConfidence: r.req.MinConfidence,
		Rules:         rules,
	}) {
		if it.SuppressedBy != nil {
			answered = append(answered, it)
		}
	}
	if len(answered) == 0 {
		return ents, rels
	}
	out, _, err := review.Apply(res, answered, nil)
	if err == nil {
		// §5: the numbers needed to distrust the graph. A rule that removes a
		// chunk's proposal removes it here, before the record ever reaches the
		// job, so this is the only place that can count it — by the end of the
		// run there is nothing left to subtract. Counted with an atomic
		// because chunks are extracted concurrently and this is the one piece
		// of run state a chunk writes.
		r.dropped.Add(int64(out.Counts.Dropped))
	}
	if err != nil {
		// The proposal goes through untouched. A rule that cannot be applied
		// to this chunk is a rule that will fail the same way over the whole
		// result at the end of the job, where the failure reaches the caller
		// as an error they can act on — and where dropping the chunk's records
		// here would already have made that error a lie about what was found.
		return ents, rels
	}
	return out.Entities, out.Relations
}

// toldOf is the standing answers as the lines the model is shown.
//
// They are read off the set rather than rendered again from the rules, so the
// sentences in the prompt are the same objects the result records as having
// been told. The system prompt is part of the cache address (§8.2), and taking
// the lines from the set means one policy produces one prompt whatever order
// the rules arrived in — so a resumed job (§8.3) that reads its policy back in
// a different order is served the answers it already bought rather than made
// to buy them again.
func toldOf(set alchemy.RuleSet) []string {
	out := make([]string, 0, len(set.Rules))
	for _, rule := range set.Rules {
		out = append(out, rule.Told)
	}
	return out
}
