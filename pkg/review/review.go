// Package review is the only mechanism in DESIGN.md that produces
// correctness. Everything before it produces attributability: §5b is careful
// to promise that a wrong edge is traceable, not that no edge is wrong. This
// is where a person closes that gap — "the model proposed, and what you have
// was checked."
//
// Two rules shape the whole package. The first is that review is not a new
// analysis: §5c says what is worth reviewing is already computed, so this
// package ranks findings the verifier and the mappers produced and adds no
// judgement of its own. The second is that a queue containing the obvious is a
// queue people stop reading, which would make review worse than none — it
// would launder unchecked output as reviewed. So nothing enters the queue for
// being a record; it enters for being a question.
//
// The flow is Queue, then Apply. Queue ranks the questions; Open is the part
// of it a person still has to answer, the rest having been answered by an
// `always` rule somebody wrote down. Apply carries the answers onto the
// result, and Held reports whether §7.3's job is still blocked — a conflict
// nobody has put their name to.
package review

import (
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Kind is why an item is in the queue. The set is closed and ordered: §5c's
// table is the ranking, and a kind invented outside it would have no place in
// that order.
type Kind string

const (
	// KindConflict — two sources both claiming to be right. §7.3: this is the
	// one that holds a job whether or not review mode is on.
	KindConflict Kind = "conflict"
	// KindViolation — an edge or entity whose type the ontology does not allow.
	KindViolation Kind = "violation"
	// KindGuess — an inferred mapping. §2.1: one wrong guess misaligns a whole
	// table, which is why it outranks a single unsure edge.
	KindGuess Kind = "guess"
	// KindLowConfidence — the model was unsure and said so.
	KindLowConfidence Kind = "low_confidence"
)

// Item is one question for a person, carrying enough to answer it without
// opening the source.
type Item struct {
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// Rank is this item's position in the queue; lower is more urgent.
	Rank int `json:"rank"`
	// Index is where the finding this item was built from sits in the
	// matching Result slice — Conflicts, Violations or Guesses. It is how
	// Apply writes the reviewer's name onto the finding as well as onto the
	// graph. It is unused for KindLowConfidence, which is not a finding: the
	// record is its own report.
	Index int `json:"index"`
	// Subject names what the question is about.
	Subject string `json:"subject"`
	// Summary is the sentence the reviewer reads.
	Summary string `json:"summary"`
	// Shape is the class of mistake this item belongs to. It is what an
	// "always" rule is made of and matched on; see rules.go for what it
	// deliberately does and does not generalise over.
	Shape string `json:"shape"`
	// Targets are the graph records a decision on this item acts on. It is
	// empty for a guess, which is a mapping rather than a record.
	Targets []Ref `json:"targets,omitempty"`
	// SuppressedBy is the `always` rule that already answered this item's
	// class. An item carrying one is not a question — it is an answer already
	// given, kept in the queue rather than dropped from it.
	//
	// Dropping it was the obvious design and is wrong twice over. §7.3 needs a
	// rule to *resolve* tonight's conflict, not conceal it: an item that
	// vanished would leave the job held on a question nobody is being shown.
	// And a queue three items shorter than the findings, with no way to say
	// which rule took each away, is the unexplainable policy §5c warns about
	// wearing a different hat. Open() is what a person is asked.
	SuppressedBy *Rule `json:"suppressed_by,omitempty"`
	// Provenance is the side that proposed the thing being questioned, so the
	// reviewer sees a schema on one side and a PDF on the other without
	// looking anything up.
	Provenance alchemy.Provenance `json:"provenance"`
}

// Open is the part of a queue a person still has to answer. Everything else
// in it was answered by a rule somebody wrote down.
func Open(items []Item) []Item {
	var out []Item
	for _, it := range items {
		if it.SuppressedBy == nil {
			out = append(out, it)
		}
	}
	return out
}

// RefKind says which half of the graph a Ref points into.
type RefKind string

const (
	RefEntity   RefKind = "entity"
	RefRelation RefKind = "relation"
)

// Ref names graph records by what they say rather than by index. An index into
// a slice stops meaning anything the moment a rejection removes an element,
// and decisions are explicitly a set applied in no particular order, so an
// index would make the outcome depend on the order after all.
//
// One Ref can name several records: duplicates are how a conflict arises in
// the first place, and a decision about "n1" is a decision about every record
// claiming to be n1 from that source.
type Ref struct {
	Kind RefKind `json:"kind"`
	// ID is the entity ID when Kind is RefEntity.
	ID string `json:"id,omitempty"`
	// From, To and Type identify a relation when Kind is RefRelation.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Type string `json:"type,omitempty"`
	// Provenance narrows the Ref to the records one source produced. A
	// conflict names two claims about one subject and a decision is about one
	// of them, so without this a rejection would delete the side the reviewer
	// kept along with the side they threw away.
	Provenance alchemy.Provenance `json:"provenance"`
}

// Options is what the caller asked for. The zero value is a caller that never
// asked for review, which §7.3 says still gets its conflicts.
type Options struct {
	// Reviewing is review mode. §5c: the default is off, and a caller that
	// never turns it on gets exactly the service §5b describes.
	Reviewing bool
	// MinConfidence is the line below which an inferred item is queued as
	// low-confidence. Zero queues none of them: a caller who has not said what
	// "unsure" means for their model has not asked a question, and inventing a
	// threshold on their behalf would fill the queue with items whose presence
	// nobody can explain.
	MinConfidence float64
	// Rules are the `always` rules already recorded. An item whose class one
	// of them covers comes back marked rather than missing — see
	// Item.SuppressedBy — so that Apply can carry the rule's answer onto the
	// result instead of leaving the question unasked and unanswered.
	Rules []Rule
}

// Verb is what a reviewer did. §5c lists exactly four, and the set is closed:
// a fifth would be a way of disposing of a proposal that the provenance of the
// result has no way to describe.
type Verb string

const (
	// VerbAccept keeps the record and records who looked at it.
	VerbAccept Verb = "accept"
	// VerbReject removes it from the graph.
	VerbReject Verb = "reject"
	// VerbEdit corrects it: retype an entity, redirect an edge, rename.
	VerbEdit Verb = "edit"
	// VerbAlways accepts this one and stops the queue asking about ones like
	// it. §5c: "Reviewing a thousand extractions one at a time is not a
	// workflow anybody sustains; reviewing the twelve kinds of mistake in them
	// is."
	VerbAlways Verb = "always"
)

// Edit is a correction. An empty field is one the reviewer did not touch,
// which is why this is not simply a replacement record: a reviewer who
// retyped an entity said nothing about its name, and a struct that carried
// both would silently blank the half they left alone.
type Edit struct {
	// Type retypes an entity or an edge.
	Type string `json:"type,omitempty"`
	// Name renames an entity.
	Name string `json:"name,omitempty"`
	// From and To redirect an edge.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// empty reports whether an edit would change nothing. Applying one is an
// error rather than a no-op: a reviewer who pressed Edit believes they changed
// something, and a result that says "reviewed by X" over an unchanged record
// is exactly the laundering §5c warns about.
func (e Edit) empty() bool { return e == Edit{} }

// Decision is one answer. §5c: decisions are part of the result, so an
// accepted graph carries who accepted what.
type Decision struct {
	ItemID string `json:"item_id"`
	Verb   Verb   `json:"verb"`
	// By is who decided. It is required: a decision nobody signed cannot be
	// written into provenance, and "reviewed by" with nobody in it is worse
	// than not claiming review at all.
	By string `json:"by"`
	// Edit is set when Verb is VerbEdit, and may be set with VerbAlways when
	// the rule is "correct ones like this the same way".
	Edit *Edit `json:"edit,omitempty"`
	// Note is why. It is what a later reader of a Rule has to go on.
	Note string    `json:"note,omitempty"`
	At   time.Time `json:"at,omitzero"`
}

// sameAs reports whether two decisions say the same thing. Apply uses it to
// tell a retransmitted decision from a contradictory one — Review is a
// bidirectional stream (§6), and a stream that reconnects redelivers.
func (d Decision) sameAs(other Decision) bool {
	if d.ItemID != other.ItemID || d.Verb != other.Verb || d.By != other.By || d.Note != other.Note {
		return false
	}
	switch {
	case d.Edit == nil && other.Edit == nil:
		return true
	case d.Edit == nil || other.Edit == nil:
		return false
	default:
		return *d.Edit == *other.Edit
	}
}
