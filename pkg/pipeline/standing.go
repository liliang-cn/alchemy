package pipeline

import (
	"fmt"
	"strings"

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
		if len(rules) == 0 {
			return extract.Settled{}
		}
		return extract.Settled{
			Told:  toldOf(rules),
			Named: namedOf(rules),
			Filter: func(_ alchemy.Chunk, ents []alchemy.Entity, rels []alchemy.Relation) ([]alchemy.Entity, []alchemy.Relation) {
				return r.settle(rules, ents, rels)
			},
		}
	}
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

// toldOf renders the standing answers as the lines the model is shown.
//
// Rule.Because is the sentence the reviewer was looking at when they decided,
// which §5c put on the rule precisely so it could be read after the item it
// came from is gone. It is what a model needs too: "entity W1 has type Widget,
// which sds@3 does not declare" says more about what to stop doing than any
// paraphrase of the shape string would.
func toldOf(rules []review.Rule) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, told(rule))
	}
	return out
}

func told(rule review.Rule) string {
	parts := []string{rule.Because}
	// A rejection is the one standing answer whose sentence does not say what
	// to do. "--flag is a switch, not an entity" is a reason; the instruction
	// that follows from it — stop proposing these — is what the model needs,
	// and leaving it to be inferred from a reason wastes the cheap half of
	// §6's promise. The filter still runs either way; telling is the nudge and
	// not believing it is the guarantee.
	if rule.From.Verb == review.VerbReject {
		parts = append(parts, "do not propose these at all")
	}
	if ed := rule.From.Edit; ed != nil {
		if ed.Type != "" {
			parts = append(parts, fmt.Sprintf("use the type %q for these instead", ed.Type))
		}
		if ed.Name != "" {
			parts = append(parts, fmt.Sprintf("write the name %q for these instead", ed.Name))
		}
	}
	if rule.From.Note != "" {
		parts = append(parts, rule.From.Note)
	}
	if rule.From.By != "" {
		// "declared by" rather than "decided by" for an authored rule, because
		// the model is being told the truth about the sentence's standing: one
		// came from somebody reading this corpus, the other from somebody
		// stating policy about corpora like it.
		parts = append(parts, verbOfOrigin(rule)+" by "+rule.From.By)
	}
	return strings.Join(parts, "; ")
}

func verbOfOrigin(rule review.Rule) string {
	if rule.Authored() {
		return "declared"
	}
	return "decided"
}

// namedOf is what alchemy.Provenance.Rules records.
//
// Shapes, not the rendered sentences: the shape is the rule's identity — the
// thing Rule.Covers matches on — so a reader comparing two runs can say which
// rule, not merely that there was one. It is what a person auditing a graph
// would have to be given anyway to check the claim.
//
// Each shape is prefixed with the rule's origin, because the two are different
// claims about the same suppression and this string is where a reader meets
// them. "reviewed" says a person looked at this exact finding and generalised
// from it; "authored" says a person declared it in advance, having seen no
// instance at all. The second is the weaker warrant, and a provenance that
// rendered them identically would let it be read as the stronger.
//
// Both are marked rather than only the new one. A scheme where silence meant
// "reviewed" would make an authored rule that lost its marker indistinguishable
// from a reviewer's — the failure this whole field exists to prevent — and it
// is the same argument pkg/review makes for not treating a confidence of zero
// as a model expressing doubt. An unprefixed entry in an old graph is then
// visibly an older run rather than a claim about who decided.
func namedOf(rules []review.Rule) string {
	shapes := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule.Shape != "" {
			shapes = append(shapes, string(originOf(rule))+":"+rule.Shape)
		}
	}
	return strings.Join(shapes, "; ")
}

// originOf spells out the origin a rule carries, including the zero value.
// review.Origin's zero is a reviewer's rule — every rule that could exist
// before the field did was minted from a decision — and writing that out here
// is what keeps the rendered string from having a meaning nobody stated.
func originOf(rule review.Rule) review.Origin {
	if rule.Authored() {
		return review.OriginAuthored
	}
	return review.OriginReviewed
}
