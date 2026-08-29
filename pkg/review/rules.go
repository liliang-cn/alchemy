package review

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// A Rule is what `always` produced: a class of question the reviewer has
// answered once and does not want asked again.
//
// §5c is explicit that it is recorded with the decision that produced it. A
// rule without an origin is an unexplainable policy — six months on, the only
// available reading of "Widget entities are accepted" is that somebody must
// have had a reason, and the queue quietly shrinks for reasons nobody can
// audit. With the decision attached, the rule answers who, when, about what,
// and with what note.
type Rule struct {
	// Shape is the class this rule covers. An item is suppressed when its
	// Shape is equal to this one — see shapeOf for what that equality means.
	Shape string `json:"shape"`
	// Kind is the kind of item the rule was made from, carried separately so a
	// rule can be read without decomposing its shape.
	Kind Kind `json:"kind"`
	// From is the decision that produced the rule, whole.
	From Decision `json:"from"`
	// Because is the item the decision was made on, rendered. The item itself
	// is gone by the time anybody reads the rule — the job it belonged to
	// expired — so the sentence the reviewer was looking at travels with it.
	Because string `json:"because"`
}

// Covers reports whether this rule already answered this item — whether the
// item is one "like" the item the rule was made from.
//
// It is exported because a suppression nobody can attribute is the same
// failure as a policy nobody can explain, one step earlier: a queue that is
// three items shorter than the findings should be able to say which rule took
// each of the three away, and who made it.
//
// Equality on the shape string, and nothing else. A rule that matched by
// prefix or by subset would grow silently as new shapes appeared underneath
// it, and "the queue got shorter and nobody changed anything" is exactly what
// this package exists to prevent.
func (r Rule) Covers(it Item) bool {
	return r.Shape != "" && r.Shape == it.Shape
}

// ruleFor returns the first rule covering an item, or nil. First rather than
// most-specific: shapes are matched whole, so two rules covering one item are
// two people who wrote down the same policy, and either explains it.
func ruleFor(it Item, rules []Rule) *Rule {
	for i, r := range rules {
		if r.Covers(it) {
			return &rules[i]
		}
	}
	return nil
}

// The shape functions below decide what "an item like this one" means, which
// is the central design decision of this package. The boundary is the same in
// all four:
//
//	A shape carries everything the reviewer relied on to answer the class of
//	question, and drops what identifies the individual instance.
//
// Dropped, in every kind: the entity ID, the edge's endpoints, the chunk index
// and the row. Those are what make a queue a thousand items instead of twelve,
// and they are exactly what §5c wants generalised over — "reviewing the twelve
// kinds of mistake" is only possible if the identity of the thousandth
// instance is not part of the rule.
//
// Kept, in every kind: the producer, and the model when there is one. The
// producer is why the reviewer believed the item. A rule made about a schema
// import must not go on to accept a model's proposal, and one made about last
// quarter's model must not cover the model swapped in next week — that is a
// different extractor with a different failure mode wearing the same rule.
//
// Kept, per kind: the type name, the ontology rule, the mapped field. Those
// are the class itself. "Widget is not in this ontology" is a decision about
// Widget; letting it cover Gadget would accept a type nobody ever saw.
func conflictShape(c alchemy.Conflict, attribute string) string {
	// Both producers, in the order verify wrote them — left is the incumbent
	// and, for a contradiction, the deterministic side. The pair is the class:
	// "when the schema and the model disagree about an entity's type, the
	// schema wins" is a policy an operator can state and defend, where "when
	// anything disagrees with anything" is not.
	//
	// The attribute name is in when there is one, because disagreeing about a
	// name and disagreeing about a capacity are different questions with
	// different right answers.
	return join("conflict", string(c.Kind), attribute,
		"between="+string(c.Left.Provenance.Producer)+"|"+string(c.Right.Provenance.Producer),
		modelOf(c.Right.Provenance))
}

func violationShape(v alchemy.Violation, targets []Ref) string {
	// The offending type, taken from the record rather than from the message,
	// so the shape does not depend on how a sentence is worded.
	//
	// Endpoints stay out even for relation_not_allowed, where they are part of
	// the rule that was broken. That is the deliberate widening in this kind:
	// "DEPLOYED_ON is not allowed between the types this model keeps giving it"
	// is the mistake, and a rule pinned to one pair of endpoints would have to
	// be made again for every pair — which is the thousand-item queue §5c says
	// nobody sustains.
	return join("violation", string(v.Kind), typeOf(targets),
		"producer="+string(v.Provenance.Producer), modelOf(v.Provenance))
}

func guessShape(g alchemy.Guess) string {
	// Field and chosen mapping, not the source file. A guess is a decision
	// about a column, and the nightly re-import §5c describes — "a nightly
	// re-import of a table whose mapping was approved last month should not
	// ask again" — is the same column arriving in a file that may well have
	// been renamed by the date in it.
	//
	// The chosen mapping is in, so a rule stops covering the guess the moment
	// the mapper starts choosing differently. That is the case where the
	// approval no longer means what it meant.
	return join("guess", "field="+g.Field, "chosen="+g.ChosenAs,
		"producer="+string(g.Provenance.Producer), modelOf(g.Provenance))
}

func lowConfidenceShape(refKind, typ string, p alchemy.Provenance) string {
	// The narrowest of the four, and on purpose: this is the kind where a
	// rule accepts records nobody looked at one by one, so it is pinned to one
	// type from one model. "This model's low-confidence PART_OF edges are
	// fine" is a claim an operator can hold; "this model's low-confidence
	// output is fine" is a claim that turns review off while leaving it
	// switched on.
	//
	// The confidence value itself is not in the shape. It is a continuous
	// number, so including it would make every item its own class and no rule
	// would ever match a second item.
	return join("low_confidence", refKind, "type="+typ,
		"producer="+string(p.Producer), modelOf(p))
}

func modelOf(p alchemy.Provenance) string {
	if p.Model == "" {
		return ""
	}
	return "model=" + p.Model
}

func typeOf(targets []Ref) string {
	if len(targets) == 0 {
		return "type="
	}
	return "type=" + targets[0].Type
}

func join(parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += "/"
		}
		out += p
	}
	return out
}
