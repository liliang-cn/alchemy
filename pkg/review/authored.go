package review

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// This file is the other way a Rule can come to exist: a person writes one.
//
// Everything in rules.go assumes a rule was minted from a decision on an item
// that was actually offered, which means a rule can only exist after the
// mistake it prevents has already happened once. An operator who knows their
// corpus cannot state policy before the first job, and a nightly pipeline
// cannot carry a rule set as configuration. Both are things the design implies
// — §6's "--flag is never an entity" is policy, not a finding — and neither is
// reachable through a queue.
//
// What is deliberately *not* here is a second rule system. An authored rule is
// the same Rule, matched by the same Covers, applied by the same Apply, and
// shown to the model by the same standing-answers section. The only new things
// are where its origin comes from and what it is not allowed to say.

// Origin says which of the two warrants a rule has.
//
// They are different claims about the same suppression. A reviewer's rule says
// "a person looked at this exact finding and generalised from it"; an authored
// rule says "a person declared this in advance". The second is weaker — nobody
// has seen an instance — and a reader who cannot tell them apart will read the
// weaker one as the stronger.
type Origin string

const (
	// OriginReviewed is a rule minted from a decision on an item. It is the
	// zero value on purpose: every rule that could exist before this field did
	// was minted that way, so silence has to mean what those rules already
	// meant. An authored rule has to say so, which is the direction that fails
	// safe — a lost marker under-claims rather than over-claims.
	OriginReviewed Origin = "reviewed"
	// OriginAuthored is a rule a person wrote directly.
	OriginAuthored Origin = "authored"
)

// Authored reports whether a person declared this rule in advance rather than
// deciding an item they were shown.
func (r Rule) Authored() bool { return r.Origin == OriginAuthored }

// Authorship is a rule as a person writes it, and is the JSON a rule file
// holds.
//
// It is a separate type from Rule rather than a constructor over one because
// the two are written by different parties. Rule is what the service records;
// this is what a person states, and the fields are the four things §5c's
// origin requirement needs when there is no decision to point at: who, why,
// when, and what to do. Rule() is the only way to turn one into the other, so
// there is no path that produces an authored rule without checking it.
type Authorship struct {
	// Shape is the class this rule covers, in the same spelling the queue
	// builds (see rules.go). It is written out rather than described because
	// the shape is the rule's identity: Covers is equality on it, and a
	// description would be a second language for the same thing.
	Shape string `json:"shape"`
	// Verb is what the rule does with the records it covers: `always` accepts
	// them, `reject` drops them.
	Verb Verb `json:"verb"`
	// By is who declared it. Required for the reason §5c gives for a decision:
	// a policy nobody signed cannot be written into provenance, and "the
	// service decided" is not an answer anybody can follow up.
	By string `json:"by"`
	// Because is why. Required, and it is the whole of §5c's origin
	// requirement for an authored rule — the sentence a later reader has to go
	// on when the author has left. A rule with an empty reason is exactly the
	// unexplainable policy §5c names.
	Because string `json:"because"`
	// At is when it was declared. Required, because a policy with no date
	// cannot be judged stale: "six months on" is §5c's own framing, and it is
	// not a question anybody can ask of an undated rule. It also says which
	// model generation, which corpus and which ontology the author had in
	// front of them.
	At time.Time `json:"at"`
	// Edit is the correction to apply, for a rule that means "and correct ones
	// like this the same way".
	Edit *Edit `json:"edit,omitempty"`
	// Note is anything else worth saying — when to revisit it, what would make
	// it wrong. Optional: Because already carries the obligation.
	Note string `json:"note,omitempty"`
}

// Rule turns a declaration into the rule the rest of the package works with,
// or refuses it.
//
// The Decision it builds carries no ItemID, and that is the honest record: no
// item was answered. Everything else a Decision holds — the verb, the person,
// the note, the moment — is what the author actually stated, which is why this
// reuses Decision rather than inventing a parallel origin type. Two ways to
// become a decision would be two ways for Apply to behave.
func (a Authorship) Rule() (Rule, error) {
	kind, err := a.check()
	if err != nil {
		return Rule{}, err
	}
	return Rule{
		Shape:  a.Shape,
		Kind:   kind,
		Origin: OriginAuthored,
		From: Decision{
			Verb: a.Verb,
			By:   a.By,
			Edit: a.Edit,
			Note: a.Note,
			At:   a.At,
		},
		Because: a.Because,
	}, nil
}

// check is every refusal an authored rule can meet, in the order a person
// writing one would hit them.
func (a Authorship) check() (Kind, error) {
	if strings.TrimSpace(a.By) == "" {
		return "", fmt.Errorf("review: this authored rule names nobody; a policy nobody signed is one a later reader cannot follow up (DESIGN.md §5c)")
	}
	if strings.TrimSpace(a.Because) == "" {
		return "", fmt.Errorf("review: the authored rule %q states no reason; a rule with an empty reason is the unexplainable policy §5c refuses — six months on, the only available reading is that somebody must have had one", a.Shape)
	}
	if a.At.IsZero() {
		return "", fmt.Errorf("review: the authored rule %q does not say when it was declared; a policy with no date cannot be judged stale, and staleness is the failure mode of a rule nobody has revisited", a.Shape)
	}
	switch a.Verb {
	case VerbAlways, VerbReject:
	case VerbAccept, VerbEdit:
		// Both are decisions about the record in front of the reviewer. A rule
		// is by definition about a class, so an `accept` rule would be a rule
		// that accepts one thing it cannot identify, and an `edit` rule is
		// spelled `always` with an Edit — which is what §5c already calls
		// "correct ones like this the same way".
		return "", fmt.Errorf("review: an authored rule cannot carry the verb %q, which decides one record; a rule is about a class, so use \"always\" (with an edit, if it corrects them) or \"reject\"", a.Verb)
	default:
		return "", fmt.Errorf("review: the authored rule %q has the unknown verb %q; a rule may only accept a class (\"always\") or drop it (\"reject\")", a.Shape, a.Verb)
	}
	if a.Verb == VerbAlways && a.Edit != nil && a.Edit.empty() {
		return "", fmt.Errorf("review: the authored rule %q carries an edit that changes nothing; a record marked settled and left alone is an accept wearing the wrong label", a.Shape)
	}
	kind, err := authoredShape(a.Shape)
	if err != nil {
		return "", err
	}
	if kind == KindDuplicate {
		if err := authoredMerge(a); err != nil {
			return "", err
		}
	}
	return kind, nil
}

// authoredShape is the floor on how wide a hand-written rule may be.
//
// A reviewer's rule cannot widen past a finding that existed: shapeOf builds
// it from a real item, so every part of it was true of something. An authored
// rule has no such floor — it is a string a person typed — and the shape it is
// easiest to type is the widest one. "This model's low-confidence output is
// fine" is the rule lowConfidenceShape's own comment calls "a claim that turns
// review off while leaving it switched on", and it is one empty segment away
// from a legal rule.
//
// So two conditions, and they are different in kind:
//
//   - The shape must be one the queue itself could have built. That is checked
//     by parsing it and building it again with the very functions the queue
//     uses, and requiring the two strings to be equal — so a typo, an invented
//     segment, a producer nobody has, or a violation kind nothing raises is a
//     refusal at the moment it is written rather than a rule that silently
//     matches nothing forever.
//   - Every part that names the class must actually name something. The
//     round-trip alone does not give this: `type=` is a shape the queue can
//     build, from an item whose finding named no record, and a reviewer
//     reaching it has at least seen that finding. A person writing one has
//     seen nothing, and the rule they have written is "everything from this
//     producer".
//
// Conflicts are refused outright; see the comment on conflictAuthored.
func authoredShape(shape string) (Kind, error) {
	if strings.TrimSpace(shape) == "" {
		return "", fmt.Errorf("review: this authored rule has no shape, so there is no class for it to cover; write the shape the queue would build for the finding you mean")
	}
	parts := strings.Split(shape, "/")
	kind, err := kindOf(parts[0])
	if err != nil {
		return "", err
	}
	if kind == KindConflict {
		return "", conflictAuthored(shape)
	}
	fields, positional, err := segments(parts[1:])
	if err != nil {
		return "", fmt.Errorf("review: the authored rule %q: %w", shape, err)
	}
	// A duplicate names two sides and so names two producers, the way a
	// conflict does; the single-producer segment below is not its shape.
	var prov alchemy.Provenance
	if kind != KindDuplicate {
		producer, err := producerOf(fields["producer"])
		if err != nil {
			return "", fmt.Errorf("review: the authored rule %q: %w", shape, err)
		}
		prov = alchemy.Provenance{Producer: producer, Model: fields["model"]}
	}

	var rebuilt string
	switch kind {
	case KindDuplicate:
		rebuilt, err = duplicateAuthored(shape, fields, positional)
		if err != nil {
			return "", err
		}
	case KindViolation:
		if len(positional) != 1 {
			return "", fmt.Errorf("review: the authored rule %q does not say which violation it is about; write one of the kinds the verifier raises, as in %q", shape, "violation/unknown_entity_type/type=Widget/producer=llm-extract")
		}
		vk, err := violationKindOf(positional[0])
		if err != nil {
			return "", fmt.Errorf("review: the authored rule %q: %w", shape, err)
		}
		if fields["type"] == "" {
			return "", tooWide(shape, "the type it is about", `every violation of this kind from this producer, whatever it is about`)
		}
		rebuilt = violationShape(alchemy.Violation{Kind: vk, Provenance: prov}, []Ref{{Ref: alchemy.Ref{Type: fields["type"]}}})
	case KindGuess:
		if fields["field"] == "" || fields["chosen"] == "" {
			return "", tooWide(shape, "the column and the field it was read as", `every inferred mapping this producer makes`)
		}
		rebuilt = guessShape(alchemy.Guess{Field: fields["field"], ChosenAs: fields["chosen"], Provenance: prov})
	case KindLowConfidence:
		if len(positional) != 1 || (positional[0] != string(RefEntity) && positional[0] != string(RefRelation)) {
			return "", fmt.Errorf("review: the authored rule %q does not say whether it is about an entity or a relation, as in %q", shape, "low_confidence/relation/type=DEPLOYED_ON/producer=llm-extract")
		}
		if fields["type"] == "" {
			// The narrowest of the four kinds on purpose (see
			// lowConfidenceShape): this is where a rule accepts records nobody
			// ever looked at one by one.
			return "", tooWide(shape, "the type it is about", `every unsure record this producer proposes`)
		}
		rebuilt = lowConfidenceShape(positional[0], fields["type"], prov)
	}
	if rebuilt != shape {
		return "", fmt.Errorf("review: the authored rule %q is not a shape this service builds; the queue would have written %q, and a shape that differs by one character matches nothing forever without ever saying so", shape, rebuilt)
	}
	return kind, nil
}

// conflictAuthored is the refusal §7.3 asks for, and the reason is worth
// stating where somebody meets it.
//
// §7.3 does permit a rule to resolve a conflict unattended: "resolve it, or
// tell the service how to resolve conflicts of that shape next time (§5c's
// `always`), which is how a pipeline that started attended becomes one that
// runs itself without ever having guessed." The last four words are the whole
// argument. That rule came from a person who had seen a conflict of that
// shape: they read the two claims, the two producers and the attribute in
// dispute, and generalised from something that had actually happened. The
// shape was bounded by a finding.
//
// An authored conflict rule has no such witness. It is a person answering a
// question nobody has asked yet, about two sources whose disagreement they
// have imagined — a guess, in the one place §5c calls "the one place this
// design refuses to let a caller opt out of a person". And the failure is
// silent and total: `conflict/entity_type/between=ddl|llm-extract` is one line
// that means "whenever the schema and the model disagree about any entity's
// type, in any corpus, forever, the schema wins", written while review mode is
// off, on a job that then returns success. §5b's "a failure that looks like a
// success" is exactly that.
//
// The operator who genuinely knows their sources is not left with nothing. The
// first job that meets such a conflict holds, once, and they answer it with
// `always` on the finding in front of them — which mints a rule bounded by
// what they saw, and every night after that runs unattended. That is §7.3's
// sentence unchanged, and it costs one hold per shape rather than one per
// record.
func conflictAuthored(shape string) error {
	return fmt.Errorf("review: %q is a conflict rule, and a conflict cannot be answered in advance: §7.3 makes a conflict the one thing that always requires a person, and its own escape hatch is a rule made by somebody who had seen one — %q. Let the first job hold, answer it with `always`, and every run after that is unattended",
		shape, "how a pipeline that started attended becomes one that runs itself without ever having guessed")
}

func tooWide(shape, missing, covers string) error {
	return fmt.Errorf("review: the authored rule %q does not name %s, so it covers %s; a reviewer's rule is bounded by a finding that existed and a written one has to say what it is about, or it is review switched off while the flag says it is on",
		shape, missing, covers)
}

// segments splits a shape's tail into its key=value parts and the bare ones.
// A repeated or unknown key is refused rather than ignored: both are typos,
// and a typo that survives becomes a rule that matches nothing and says so
// nowhere.
func segments(parts []string) (map[string]string, []string, error) {
	fields := map[string]string{}
	var positional []string
	for _, p := range parts {
		key, value, ok := strings.Cut(p, "=")
		if !ok {
			positional = append(positional, p)
			continue
		}
		switch key {
		case "type", "producer", "model", "field", "chosen", "left", "right", "between":
		default:
			return nil, nil, fmt.Errorf("names %q, which is not part of any shape this service builds", key)
		}
		if _, dup := fields[key]; dup {
			return nil, nil, fmt.Errorf("names %q twice", key)
		}
		fields[key] = value
	}
	return fields, positional, nil
}

func kindOf(word string) (Kind, error) {
	switch k := Kind(word); k {
	case KindConflict, KindViolation, KindGuess, KindDuplicate, KindLowConfidence:
		return k, nil
	default:
		return "", fmt.Errorf("review: %q is not a kind of question this service asks; a shape begins with one of %q, %q, %q, %q or %q", word, KindConflict, KindViolation, KindGuess, KindDuplicate, KindLowConfidence)
	}
}

// duplicateAuthored is the one two-sided finding a person may write a rule
// about in advance, and the difference from a conflict is worth stating beside
// the refusal it is not.
//
// conflictAuthored's objection is that the shape is unbounded: "whenever the
// schema and the model disagree about any entity's type, in any corpus,
// forever, the schema wins" is one line, written by somebody who has seen no
// instance, and it decides questions nobody has read. A merge rule cannot be
// written that way. It names both spellings in full, so the widest thing an
// author can say is "these two names are one thing" — a claim about two strings
// they typed, which either matches a pair in tonight's corpus or matches
// nothing. §5c's operator who knows their corpus is exactly the person this
// serves: they already know their model writes the type word into the name, and
// making them wait for a hold per pair to say so is the ceremony §5c's opening
// argument refuses.
//
// The floor is that both names and the type must be there. A rule naming one
// side would be "merge anything affix-matching this name into whatever I said",
// which is the unbounded rule again in a smaller font.
func duplicateAuthored(shape string, fields map[string]string, positional []string) (string, error) {
	if len(positional) != 1 {
		return "", fmt.Errorf("review: the authored rule %q does not say which signal it is about; write the one the verifier raises, as in %q",
			shape, "duplicate/name_affix/type=Package/left=document/right=document package/between=llm-extract|llm-extract")
	}
	signal, err := duplicateSignalOf(positional[0])
	if err != nil {
		return "", fmt.Errorf("review: the authored rule %q: %w", shape, err)
	}
	if fields["type"] == "" || fields["left"] == "" || fields["right"] == "" {
		return "", tooWide(shape, "the type and both names",
			"every pair this signal ever matches, including pairs nobody has seen")
	}
	left, right, err := producerPair(fields["between"])
	if err != nil {
		return "", fmt.Errorf("review: the authored rule %q: %w", shape, err)
	}
	return duplicateShape(alchemy.Duplicate{
		Signal: signal,
		Left: alchemy.DuplicateSide{
			Type: fields["type"], Name: fields["left"],
			Provenance: alchemy.Provenance{Producer: left},
		},
		Right: alchemy.DuplicateSide{
			Type: fields["type"], Name: fields["right"],
			Provenance: alchemy.Provenance{Producer: right, Model: fields["model"]},
		},
	}), nil
}

// producerPair reads the "between=a|b" segment both two-sided findings write.
func producerPair(between string) (alchemy.Producer, alchemy.Producer, error) {
	l, r, ok := strings.Cut(between, "|")
	if !ok {
		return "", "", fmt.Errorf("does not name the two producers it is between, as in %q", "between=ddl|llm-extract")
	}
	left, err := producerOf(l)
	if err != nil {
		return "", "", err
	}
	right, err := producerOf(r)
	if err != nil {
		return "", "", err
	}
	return left, right, nil
}

func duplicateSignalOf(name string) (alchemy.DuplicateSignal, error) {
	switch s := alchemy.DuplicateSignal(name); s {
	case alchemy.DuplicateNameAffix:
		return s, nil
	default:
		return "", fmt.Errorf("is about the signal %q, which nothing in this service raises", name)
	}
}

// authoredMerge is what a written merge rule may say beyond its shape.
//
// `reject` is refused here rather than only at the point it is applied,
// because the two refusals are about different people. Apply's is about a
// reviewer pressing the wrong button on an item in front of them; this is about
// a policy file, where a rule that can only ever fail is one that fails on a
// night nobody is watching, against a corpus nobody has read.
func authoredMerge(a Authorship) error {
	if a.Verb != VerbAlways {
		return fmt.Errorf("review: the authored rule %q answers a duplicate with %q; a duplicate asks whether two nodes are one node, which only \"always\" answers — with an `into` to merge them, or without one to say they are two and stop asking",
			a.Shape, a.Verb)
	}
	if a.Edit != nil && a.Edit.Into == "" {
		return fmt.Errorf("review: the authored rule %q corrects a duplicate with %+v; a merge says which of the two nodes they both are, with `into`, and nothing else",
			a.Shape, *a.Edit)
	}
	return nil
}

func producerOf(name string) (alchemy.Producer, error) {
	switch p := alchemy.Producer(name); p {
	case alchemy.ProducerDDL, alchemy.ProducerGraphImport, alchemy.ProducerTabular, alchemy.ProducerLLMExtract:
		return p, nil
	case "":
		// The producer is why a reviewer believed an item (see rules.go), and
		// a rule that named none would cross from a schema import to a model's
		// proposal — the one generalisation shapeOf refuses to make.
		return "", fmt.Errorf("names no producer, so it would cover a schema import and a model's guess alike")
	default:
		return "", fmt.Errorf("names the producer %q, which nothing in this service is", name)
	}
}

func violationKindOf(name string) (alchemy.ViolationKind, error) {
	switch k := alchemy.ViolationKind(name); k {
	case alchemy.ViolationUnknownEntityType, alchemy.ViolationUnknownRelationType,
		alchemy.ViolationRelationNotAllowed, alchemy.ViolationDanglingRelation,
		alchemy.ViolationMalformedRow, alchemy.ViolationUnnamedColumn,
		alchemy.ViolationMissingID, alchemy.ViolationDuplicateID:
		return k, nil
	default:
		return "", fmt.Errorf("is about the violation %q, which nothing in this service raises", name)
	}
}

// Validate refuses a rule that cannot explain itself, whichever way it arrived.
//
// It is called at the edges — a rule file at startup, a rule on a job request
// — rather than inside Queue, because that is where a rule stops being this
// package's own work and starts being something a person or another process
// wrote. A rule the queue minted is well-formed by construction, and checking
// it again here could only manage to refuse something this package produced.
func (r Rule) Validate() error {
	if strings.TrimSpace(r.Shape) == "" {
		return fmt.Errorf("review: a rule with no shape covers nothing and can never be matched")
	}
	switch r.Origin {
	case OriginAuthored:
		_, err := Authorship{
			Shape: r.Shape, Verb: r.From.Verb, By: r.From.By,
			Because: r.Because, At: r.From.At, Edit: r.From.Edit, Note: r.From.Note,
		}.check()
		return err
	case OriginReviewed, "":
		// §5c: a rule is recorded with the decision that produced it. The
		// shape is not re-checked: the queue built it from a finding, and a
		// second opinion here about a string this package wrote could only
		// refuse a rule that is legitimately in force.
		if strings.TrimSpace(r.From.By) == "" {
			return fmt.Errorf("review: the rule %q names nobody; a decision nobody signed cannot be written into provenance, and a rule that came from one cannot explain itself", r.Shape)
		}
		if r.From.Verb != VerbAlways {
			return fmt.Errorf("review: the rule %q says it came from a %q decision, and only `always` makes a rule; a rule written by hand has to say so (origin %q) and is checked differently", r.Shape, r.From.Verb, OriginAuthored)
		}
		return nil
	default:
		return fmt.Errorf("review: the rule %q claims the unknown origin %q; a rule was either decided on a finding (%q) or declared in advance (%q), and there is no third warrant", r.Shape, r.Origin, OriginReviewed, OriginAuthored)
	}
}

// ruleFile is what -rules and its kind load: a policy document a person wrote,
// which is deliberately not the JSON encoding of a Rule.
//
// A Rule carries a Decision because a reviewer's rule was one; asking a person
// to write `{"from": {"verb": ..., "by": ...}}` would be asking them to
// impersonate a decision that never happened. The object wrapper is there so
// the file can gain a field later without every existing one becoming invalid.
type ruleFile struct {
	Rules []Authorship `json:"rules"`
}

// LoadRules reads a rule set and refuses it whole if any rule in it cannot
// explain itself.
//
// Whole, rather than loading what parses: a policy file half of which was
// silently ignored is worse than one that was rejected, because the operator
// believes the ignored half is in force. It is the same argument §5's counts
// rest on — the failure that looks like a success is the expensive one.
//
// Unknown fields are refused too. A file that says "reason" where the format
// says "because" is a rule with no stated reason, and accepting it would turn
// a typo into an unexplainable policy.
func LoadRules(r io.Reader) ([]Rule, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var file ruleFile
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("review: reading the rule set: %w", err)
	}
	out := make([]Rule, 0, len(file.Rules))
	for i, a := range file.Rules {
		rule, err := a.Rule()
		if err != nil {
			return nil, fmt.Errorf("review: rule %d of this set: %w", i+1, err)
		}
		out = append(out, rule)
	}
	return out, nil
}
