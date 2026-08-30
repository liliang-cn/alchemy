package review

import (
	"testing"
	"time"
)

// declared is a rule as a person writes one down: the shape it covers, the
// reason, and who said so.
func declared(shape, because string) Rule {
	return Rule{
		Shape:   shape,
		Kind:    KindViolation,
		Origin:  OriginAuthored,
		Because: because,
		From: Decision{
			Verb: VerbReject, By: "ana@example.com",
			At: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		},
	}
}

// The name has to be the same on every node that runs a piece of one job
// (§8.3), which means it is a specification and not an implementation detail:
// a change to the framing, the digest or the length is a change every reader
// of every graph can see, and it has to be made deliberately by bumping the
// domain rather than discovered by two nodes disagreeing.
//
// So the value is written down. A test that only checked "the same input gives
// the same output" would pass just as happily after an accidental rewrite that
// renamed every policy in every graph ever produced.
func TestARuleSetIsNamedByAValueThatCannotChangeQuietly(t *testing.T) {
	set := InForce([]Rule{
		declared("violation/unknown_entity_type/type=Flag/producer=llm-extract", "--verbose is a switch, not an entity"),
		declared("violation/unknown_entity_type/type=Widget/producer=llm-extract", "widgets are clusters here"),
	})
	const want = "2068d8451d7be2df"
	if set.Name != want {
		t.Errorf("the set is named %q, want %q; if this change was deliberate, bump setDomain — every graph ever produced names its policy the old way", set.Name, want)
	}
	if len(set.Name) != setNameLength {
		t.Errorf("the name is %d characters, want %d; the length is on every record of a four-hundred-thousand-record import", len(set.Name), setNameLength)
	}
}

// A set is a set. Two nodes reading one policy out of one store cannot promise
// each other the order they assembled it in, and a name that depended on it
// would make two halves of one job look like two different policies.
func TestARuleSetIsNamedTheSameWhateverOrderItsRulesArrivedIn(t *testing.T) {
	a := declared("violation/unknown_entity_type/type=Flag/producer=llm-extract", "--verbose is a switch")
	b := declared("violation/unknown_relation_type/type=USES/producer=llm-extract", "USES is not in this ontology")

	first, second := InForce([]Rule{a, b}), InForce([]Rule{b, a})
	if first.Name != second.Name {
		t.Errorf("one policy is named %q read one way and %q read the other", first.Name, second.Name)
	}
	if len(first.Rules) != len(second.Rules) {
		t.Fatalf("one policy has %d members read one way and %d the other", len(first.Rules), len(second.Rules))
	}
	for i := range first.Rules {
		if first.Rules[i] != second.Rules[i] {
			t.Errorf("member %d is %+v read one way and %+v the other; the members are what the name is computed over", i, first.Rules[i], second.Rules[i])
		}
	}
	// The same rule stated twice is one policy, not two. ruleFor takes the
	// first rule that covers an item, so the second adds no coverage.
	if twice := InForce([]Rule{a, b, a}); twice.Name != first.Name {
		t.Errorf("a policy with a rule repeated is named %q and the same policy without the repeat is named %q", twice.Name, first.Name)
	}
}

// The length prefix, tested where it bites. Without it the digest sees
// name+told+name+told as one run of bytes, and two genuinely different
// policies whose text happens to concatenate the same way would share a name —
// which would make two records asked under different rules indistinguishable,
// the one property naming a set instead of listing it is not allowed to lose.
func TestTwoPoliciesWhoseTextConcatenatesTheSameAreNamedDifferently(t *testing.T) {
	// "reviewed:a" + "bc" and "reviewed:ab" + "c" are the same bytes in a row.
	left := InForce([]Rule{{Shape: "a", Because: "bc"}})
	right := InForce([]Rule{{Shape: "ab", Because: "c"}})
	if left.Name == right.Name {
		t.Errorf("two different policies are both named %q; the framing is not stating its boundaries", left.Name)
	}
}

// A shape says which class is suppressed and says nothing about what happens
// to it. Two policies that cover one class and correct it differently are two
// policies, and a record has to be able to say which one it was extracted
// under — so the name is computed over what the model was told and not only
// over the shapes.
func TestTwoPoliciesThatCorrectTheSameClassDifferentlyAreNamedDifferently(t *testing.T) {
	const shape = "violation/unknown_entity_type/type=Widget/producer=llm-extract"
	cluster := declared(shape, "widgets are clusters here")
	cluster.From.Verb, cluster.From.Edit = VerbAlways, &Edit{Type: "Cluster"}
	node := declared(shape, "widgets are clusters here")
	node.From.Verb, node.From.Edit = VerbAlways, &Edit{Type: "Node"}

	if a, b := InForce([]Rule{cluster}), InForce([]Rule{node}); a.Name == b.Name {
		t.Errorf("retyping a class to Cluster and retyping it to Node are both named %q; a record could not say which was in force", a.Name)
	}
}

// The two warrants stay apart. "A person looked at this exact finding and
// generalised" and "a person declared this in advance" are different claims
// about the same suppression, and a naming that rendered them identically
// would let the weaker be read as the stronger.
func TestAnAuthoredRuleAndAReviewersRuleAreNamedDifferently(t *testing.T) {
	const shape = "violation/unknown_entity_type/type=Widget/producer=llm-extract"
	authored := declared(shape, "widgets are clusters here")
	reviewed := authored
	reviewed.Origin = OriginReviewed

	if authored.Name() == reviewed.Name() {
		t.Fatalf("both rules are named %q", authored.Name())
	}
	if got := authored.Name(); got != string(OriginAuthored)+":"+shape {
		t.Errorf("the authored rule is named %q, want its origin in front of its shape", got)
	}
	// The zero value is a reviewer's rule — every rule that could exist before
	// the field did was minted from a decision — and it has to say so rather
	// than say nothing, or an authored rule that lost its marker would be
	// indistinguishable from one somebody decided.
	unset := Rule{Shape: shape}
	if got := unset.Name(); got != string(OriginReviewed)+":"+shape {
		t.Errorf("a rule with no origin set is named %q, want it spelled out as %q", got, OriginReviewed)
	}
}

// A run nobody has decided anything for has no policy, and the way to say so
// is to say nothing. A digest over the empty set would be a name, and a name
// on every record of every unattended import is the repetition this whole
// scheme removes, in miniature.
func TestAnEmptyPolicyHasNoName(t *testing.T) {
	for _, rules := range [][]Rule{nil, {}, {{Because: "a rule with no shape covers nothing"}}} {
		if got := InForce(rules); got.Name != "" || len(got.Rules) != 0 {
			t.Errorf("InForce(%+v) = %+v, want the zero set", rules, got)
		}
	}
}
