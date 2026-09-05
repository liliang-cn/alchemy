package wire

import (
	"github.com/liliang-cn/alchemy/pkg/alchemy"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This file is alchemy's types on their way out. Its counterpart is
// fromproto.go, and the two are meant to be read side by side: a field added
// to one and not the other is exactly the defect the round-trip test exists to
// catch, and reviewing them as a pair is how it is meant to be caught earlier
// than that.

// JobToProto carries a job's state and the two moments that bound it.
func JobToProto(j alchemy.Job) *alchemyv1.Job {
	out := &alchemyv1.Job{
		Id:    j.ID,
		State: JobStateToProto[j.State],
		Stage: j.Stage,
		Error: j.Error,
	}
	// A zero time is left absent rather than sent as the Unix epoch. "This job
	// was created in 1970" is a fact, and a false one; "we did not say" is
	// what actually happened.
	if !j.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(j.CreatedAt)
	}
	if !j.ExpiresAt.IsZero() {
		out.ExpiresAt = timestamppb.New(j.ExpiresAt)
	}
	return out
}

// ProvenanceToProto is the function §5b turns on, and the one with the worst
// history in this package: it copied ten fields and kept copying ten after
// alchemy.Provenance grew By and At, so a record a person had signed went out
// over gRPC with the signature missing. Every field of the struct is listed
// here, in the struct's order, so that the next addition is visibly absent.
func ProvenanceToProto(p alchemy.Provenance) *alchemyv1.Provenance {
	return &alchemyv1.Provenance{
		Source:     p.Source,
		Chunk:      int32(p.Chunk),
		Producer:   ProducerToProto[p.Producer],
		Model:      p.Model,
		Ontology:   p.Ontology,
		Chunking:   p.Chunking,
		Confidence: p.Confidence,
		ReviewedBy: p.ReviewedBy,
		RuleSet:    p.RuleSet,
		RuledBy:    p.RuledBy,
		By:         p.By,
		At:         p.At,
	}
}

// AttributesToProto drops an attribute value protobuf cannot carry rather than
// failing the whole result. The alternative — refusing to return a graph
// because one cell held something structpb has no shape for — would lose the
// other two hundred thousand records over a value nobody asked about.
//
// The values that survive are exactly the JSON value domain
// alchemy.Entity.Attributes declares, so a producer honouring that contract
// loses nothing here; a producer that put a time.Time or an int in the map is
// where a value goes missing, and pkg/preflight is where that is caught.
func AttributesToProto(in map[string]any) *structpb.Struct {
	if len(in) == 0 {
		return nil
	}
	out, err := structpb.NewStruct(in)
	if err == nil {
		return out
	}
	fields := make(map[string]*structpb.Value, len(in))
	for k, v := range in {
		if val, err := structpb.NewValue(v); err == nil {
			fields[k] = val
		}
	}
	return &structpb.Struct{Fields: fields}
}

func EntityToProto(e alchemy.Entity) *alchemyv1.Entity {
	return &alchemyv1.Entity{
		Id:         e.ID,
		Type:       e.Type,
		Name:       e.Name,
		Aliases:    e.Aliases,
		Attributes: AttributesToProto(e.Attributes),
		Provenance: ProvenanceToProto(e.Provenance),
	}
}

// RelationToProto carries Key, which is what makes two parallel edges two
// edges rather than one edge described twice. See alchemy.Relation.Key: five
// of one customer's tables have the shape that needs it, and a job over them
// could never finish while identity was keyed on {from, to, type} alone.
func RelationToProto(r alchemy.Relation) *alchemyv1.Relation {
	return &alchemyv1.Relation{
		From:       r.From,
		To:         r.To,
		Type:       r.Type,
		Key:        r.Key,
		Attributes: AttributesToProto(r.Attributes),
		Provenance: ProvenanceToProto(r.Provenance),
	}
}

func ChunkToProto(c alchemy.Chunk) *alchemyv1.Chunk {
	return &alchemyv1.Chunk{
		Index: int32(c.Index), Text: c.Text, Source: c.Source,
		Strategy: c.Strategy, Heading: c.Heading,
		Start: int32(c.Start), End: int32(c.End),
	}
}

func VectorToProto(v alchemy.Vector) *alchemyv1.Vector {
	return &alchemyv1.Vector{Chunk: int32(v.Chunk), Values: v.Values, Model: v.Model}
}

func ViolationToProto(v alchemy.Violation) *alchemyv1.Violation {
	return &alchemyv1.Violation{
		Kind: ViolationKindToProto[v.Kind], Detail: v.Detail, Subject: v.Subject,
		About:      AboutToProto(v.About),
		Provenance: ProvenanceToProto(v.Provenance),
	}
}

// AboutToProto carries the record a finding is about, in fields.
//
// A zero Ref becomes no message rather than an empty one, and that is the whole
// of what this function adds over RefToProto. A violation about a malformed row
// is about a file and not about a graph record, and an empty Ref on the wire
// would say "the entity with no id" — a claim that is both false and joinable,
// which is the worse of the two ways to be wrong.
//
// The provenance field of the Ref is deliberately left unset. The violation
// carries its own beside it, and two copies of one fact on one message is two
// answers that can disagree; see the field's comment in the proto for why a
// review target is the case where it does belong.
func AboutToProto(r alchemy.Ref) *alchemyv1.Ref {
	if r == (alchemy.Ref{}) {
		return nil
	}
	return &alchemyv1.Ref{
		Kind: RefKindToProto[r.Kind], Id: r.ID, From: r.From, To: r.To, Type: r.Type, Key: r.Key,
	}
}

func GuessToProto(g alchemy.Guess) *alchemyv1.Guess {
	return &alchemyv1.Guess{
		Field: g.Field, ChosenAs: g.ChosenAs, Alternatives: g.Alternatives,
		Reason: g.Reason, Provenance: ProvenanceToProto(g.Provenance),
	}
}

// ClaimToProto carries one side of a conflict, including the record that side
// was read from.
//
// About goes through AboutToProto for the reason a violation's does: a side
// that names no record must arrive as no message and not as an empty Ref, which
// would say "the entity with no id" — false, and joinable, which is the worse
// of the two ways to be wrong. A store reading `_contradicts` off these is the
// consumer that would act on it.
func ClaimToProto(c alchemy.Claim) *alchemyv1.Claim {
	return &alchemyv1.Claim{
		Statement: c.Statement, About: AboutToProto(c.About),
		Provenance: ProvenanceToProto(c.Provenance),
	}
}

func ConflictToProto(c alchemy.Conflict) *alchemyv1.Conflict {
	return &alchemyv1.Conflict{
		Kind: ConflictKindToProto[c.Kind], Subject: c.Subject, Detail: c.Detail,
		Left: ClaimToProto(c.Left), Right: ClaimToProto(c.Right),
	}
}

func DuplicateToProto(d alchemy.Duplicate) *alchemyv1.Duplicate {
	return &alchemyv1.Duplicate{
		Signal: DuplicateSignalToProto[d.Signal], Subject: d.Subject, Detail: d.Detail,
		Left: DuplicateSideToProto(d.Left), Right: DuplicateSideToProto(d.Right),
	}
}

func DuplicateSideToProto(s alchemy.DuplicateSide) *alchemyv1.DuplicateSide {
	return &alchemyv1.DuplicateSide{
		Id: s.ID, Type: s.Type, Name: s.Name, Provenance: ProvenanceToProto(s.Provenance),
	}
}

func CountsToProto(c alchemy.Counts) *alchemyv1.Counts {
	return &alchemyv1.Counts{
		Entities: int32(c.Entities), Relations: int32(c.Relations),
		Deterministic: int32(c.Deterministic), Inferred: int32(c.Inferred),
		Violations: int32(c.Violations), Conflicts: int32(c.Conflicts),
		Guesses: int32(c.Guesses), ChunksEmpty: int32(c.ChunksEmpty),
		ChunksUnread: int32(c.ChunksUnread), Dropped: int32(c.Dropped),
		Duplicates: int32(c.Duplicates),
		Chunks:     int32(c.Chunks), Vectors: int32(c.Vectors),
	}
}

func ModelCallToProto(m alchemy.ModelCall) *alchemyv1.ModelCall {
	return &alchemyv1.ModelCall{Model: m.Model, Stage: m.Stage, Calls: int32(m.Calls), Tokens: int32(m.Tokens)}
}

func UnreadToProto(u alchemy.Unread) *alchemyv1.Unread {
	return &alchemyv1.Unread{Source: u.Source, Locator: u.Locator, Reason: u.Reason}
}

// ResultToProto is the whole graph and everything a reader needs to distrust
// it, on the wire.
//
// It does not set Result.rules. Those are the `always` rules a job's review
// produced, which alchemy.Result deliberately does not carry — see its RuleSets
// field for the distinction between a job's policy input and its policy output
// — and the service attaches them beside the result. ResultFromProto drops them
// again for the same reason, which is the one asymmetry in this package that is
// intentional rather than a defect.
func ResultToProto(r alchemy.Result) *alchemyv1.Result {
	return &alchemyv1.Result{
		// The identity the result already carries. §4 makes the JSON the
		// contract and §6 makes this a translation of it, so a field the
		// document has and the message does not is a second contract.
		Job:           r.Job,
		Entities:      Each(r.Entities, EntityToProto),
		Relations:     Each(r.Relations, RelationToProto),
		Chunks:        Each(r.Chunks, ChunkToProto),
		Vectors:       Each(r.Vectors, VectorToProto),
		Conflicts:     Each(r.Conflicts, ConflictToProto),
		Violations:    Each(r.Violations, ViolationToProto),
		Guesses:       Each(r.Guesses, GuessToProto),
		Duplicates:    Each(r.Duplicates, DuplicateToProto),
		Counts:        CountsToProto(r.Counts),
		ModelCalls:    Each(r.ModelCalls, ModelCallToProto),
		Unread:        Each(r.Unread, UnreadToProto),
		RuleSets:      Each(r.RuleSets, RuleSetToProto),
		Supersessions: Each(r.Supersessions, SupersessionToProto),
		Proposals:     Each(r.Proposals, ProposalToProto),
	}
}

// RuleSetToProto carries the policy a job's records were extracted under.
//
// It is the resolution table for Provenance.rule_set, and the reason both
// halves are on the same message: a record naming a set the reader has to be
// handed separately is a graph that explains itself only to whoever already
// has the operator's rule file, which is not what §5b promises.
func RuleSetToProto(s alchemy.RuleSet) *alchemyv1.RuleSet {
	return &alchemyv1.RuleSet{Name: s.Name, Rules: Each(s.Rules, StandingRuleToProto)}
}

func StandingRuleToProto(r alchemy.StandingRule) *alchemyv1.StandingRule {
	return &alchemyv1.StandingRule{Name: r.Name, Told: r.Told}
}

// SupersessionToProto carries a statement that a record is over.
//
// It travels with its own provenance rather than borrowing the superseding
// record's, because a reviewer may retire a record a model proposed: those are
// two claims by two parties and a reader must be able to ask "who says so" of
// each. See alchemy.Supersession.
func SupersessionToProto(s alchemy.Supersession) *alchemyv1.Supersession {
	return &alchemyv1.Supersession{
		Retires:    s.Retires,
		By:         AboutToProto(s.By),
		Reason:     s.Reason,
		Provenance: ProvenanceToProto(s.Provenance),
	}
}

// ProposalToProto carries the vocabulary a corpus is asking for.
//
// The producers are converted through the same table every record uses, so a
// producer missing from it reaches a caller as PRODUCER_UNSPECIFIED here too —
// which is what TestEveryProducerHasAWireName exists to stop.
func ProposalToProto(p alchemy.Proposal) *alchemyv1.Proposal {
	out := &alchemyv1.Proposal{
		Kind:         ProposalKindToProto[p.Kind],
		Type:         p.Type,
		Records:      int32(p.Records),
		From:         p.From,
		To:           p.To,
		Sources:      p.Sources,
		DeclaredFrom: p.DeclaredFrom,
		DeclaredTo:   p.DeclaredTo,
		Example:      AboutToProto(p.Example),
	}
	for _, pr := range p.Producers {
		out.Producers = append(out.Producers, ProducerToProto[pr])
	}
	return out
}
