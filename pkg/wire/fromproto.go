package wire

import (
	"github.com/liliang-cn/alchemy/pkg/alchemy"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// This file is the direction that did not exist, and its absence is what this
// package was written for. The service emitted *alchemyv1.Result and the
// gateway emitted protobuf JSON, and a buyer who fetched a graph and wanted to
// hand it to one of the four connectors — every one of which takes an
// alchemy.Result — had no exported way to get there.
//
// Every function here is nil-tolerant, because it reads a message somebody
// else built. protobuf has no required fields, so an absent sub-message is a
// thing that happens on a real wire and not only in a malformed one: §8.4 pages
// a large result, and a page that carries entities and no counts is correct.
// A converter that panicked on it would turn a paged read into a crash on page
// two. The Get* accessors the generated code provides are nil-safe by design
// and are used throughout for exactly that reason.
//
// Unknown enum members decode to the zero value of the Go type — the empty
// string — rather than to a guess. That is the same refusal the tables make in
// the other direction: a value this build has never heard of is not silently
// promoted to the nearest one it knows, because an empty producer is visibly
// missing and a wrong producer is not.

// ProvenanceFromProto reads §5b's answer to "who says so" back off the wire.
func ProvenanceFromProto(p *alchemyv1.Provenance) alchemy.Provenance {
	if p == nil {
		return alchemy.Provenance{}
	}
	return alchemy.Provenance{
		Source:     p.GetSource(),
		Chunk:      int(p.GetChunk()),
		Producer:   ProducerFromProto[p.GetProducer()],
		Model:      p.GetModel(),
		Ontology:   p.GetOntology(),
		Chunking:   p.GetChunking(),
		Confidence: p.GetConfidence(),
		ReviewedBy: p.GetReviewedBy(),
		RuleSet:    p.GetRuleSet(),
		RuledBy:    p.GetRuledBy(),
		By:         p.GetBy(),
		At:         p.GetAt(),
	}
}

// AttributesFromProto returns the JSON value domain alchemy.Entity.Attributes
// declares: string, float64, bool, nil, []any and map[string]any, nested to any
// depth. structpb.AsMap produces exactly that set, which is why the contract is
// stated in those words — an attribute that went out as a float64 comes back a
// float64, and there is no number type here that can quietly change width.
//
// An absent or empty struct becomes a nil map rather than an empty one. A
// consumer ranging over either sees nothing, and alchemy's own JSON omits an
// empty attributes object, so nil is the value that round-trips.
func AttributesFromProto(s *structpb.Struct) map[string]any {
	if s == nil || len(s.GetFields()) == 0 {
		return nil
	}
	return s.AsMap()
}

func EntityFromProto(e *alchemyv1.Entity) alchemy.Entity {
	return alchemy.Entity{
		ID:         e.GetId(),
		Type:       e.GetType(),
		Name:       e.GetName(),
		Aliases:    e.GetAliases(),
		Attributes: AttributesFromProto(e.GetAttributes()),
		Provenance: ProvenanceFromProto(e.GetProvenance()),
	}
}

// RelationFromProto restores Key, without which two foreign keys between one
// pair of tables come back as one edge and a consumer acting on the second
// acts on the first. See alchemy.Relation.Key.
func RelationFromProto(r *alchemyv1.Relation) alchemy.Relation {
	return alchemy.Relation{
		From:       r.GetFrom(),
		To:         r.GetTo(),
		Type:       r.GetType(),
		Key:        r.GetKey(),
		Attributes: AttributesFromProto(r.GetAttributes()),
		Provenance: ProvenanceFromProto(r.GetProvenance()),
	}
}

func ChunkFromProto(c *alchemyv1.Chunk) alchemy.Chunk {
	return alchemy.Chunk{
		Index: int(c.GetIndex()), Text: c.GetText(), Source: c.GetSource(),
		Strategy: c.GetStrategy(), Heading: c.GetHeading(),
		Start: int(c.GetStart()), End: int(c.GetEnd()),
	}
}

func VectorFromProto(v *alchemyv1.Vector) alchemy.Vector {
	return alchemy.Vector{Chunk: int(v.GetChunk()), Values: v.GetValues(), Model: v.GetModel()}
}

func ViolationFromProto(v *alchemyv1.Violation) alchemy.Violation {
	return alchemy.Violation{
		Kind: ViolationKindFromProto[v.GetKind()], Detail: v.GetDetail(), Subject: v.GetSubject(),
		About:      AboutFromProto(v.GetAbout()),
		Provenance: ProvenanceFromProto(v.GetProvenance()),
	}
}

// AboutFromProto is AboutToProto's inverse, and the absent message has to come
// back as a zero Ref rather than as a Ref with a kind. A violation about a
// malformed row is about a file; giving it RefEntity because that is what enum
// zero happens to mean would invent a join that resolves to nothing.
//
// It reads no provenance. An alchemy.Ref has none — it answers "which record"
// and nothing else — and the field on the message belongs to review.Ref, which
// RefFromProto handles.
func AboutFromProto(r *alchemyv1.Ref) alchemy.Ref {
	if r == nil {
		return alchemy.Ref{}
	}
	return alchemy.Ref{
		Kind: RefKindFromProto[r.GetKind()], ID: r.GetId(), From: r.GetFrom(),
		To: r.GetTo(), Type: r.GetType(), Key: r.GetKey(),
	}
}

func GuessFromProto(g *alchemyv1.Guess) alchemy.Guess {
	return alchemy.Guess{
		Field: g.GetField(), ChosenAs: g.GetChosenAs(), Alternatives: g.GetAlternatives(),
		Reason: g.GetReason(), Provenance: ProvenanceFromProto(g.GetProvenance()),
	}
}

func ClaimFromProto(c *alchemyv1.Claim) alchemy.Claim {
	return alchemy.Claim{
		Statement: c.GetStatement(), About: AboutFromProto(c.GetAbout()),
		Provenance: ProvenanceFromProto(c.GetProvenance()),
	}
}

func ConflictFromProto(c *alchemyv1.Conflict) alchemy.Conflict {
	return alchemy.Conflict{
		Kind: ConflictKindFromProto[c.GetKind()], Subject: c.GetSubject(), Detail: c.GetDetail(),
		Left: ClaimFromProto(c.GetLeft()), Right: ClaimFromProto(c.GetRight()),
	}
}

func DuplicateFromProto(d *alchemyv1.Duplicate) alchemy.Duplicate {
	return alchemy.Duplicate{
		Signal: DuplicateSignalFromProto[d.GetSignal()], Subject: d.GetSubject(), Detail: d.GetDetail(),
		Left: DuplicateSideFromProto(d.GetLeft()), Right: DuplicateSideFromProto(d.GetRight()),
	}
}

func DuplicateSideFromProto(s *alchemyv1.DuplicateSide) alchemy.DuplicateSide {
	return alchemy.DuplicateSide{
		ID: s.GetId(), Type: s.GetType(), Name: s.GetName(),
		Provenance: ProvenanceFromProto(s.GetProvenance()),
	}
}

// CountsFromProto reads the block §5 says every graph must carry.
//
// An absent Counts message comes back as a zero Counts, which is honest and
// not the same as "the job counted nothing" — the caller who can tell the
// difference is alchemy.Result.Derivable, which recomputes the eleven fields
// that are a function of the slices beside them. A consumer that cares should
// compare the two rather than trust either; that comparison is the point of
// the block, and pkg/preflight is where it is made.
func CountsFromProto(c *alchemyv1.Counts) alchemy.Counts {
	if c == nil {
		return alchemy.Counts{}
	}
	return alchemy.Counts{
		Entities: int(c.GetEntities()), Relations: int(c.GetRelations()),
		Chunks: int(c.GetChunks()), Vectors: int(c.GetVectors()),
		Deterministic: int(c.GetDeterministic()), Inferred: int(c.GetInferred()),
		Violations: int(c.GetViolations()), Conflicts: int(c.GetConflicts()),
		Guesses: int(c.GetGuesses()), Duplicates: int(c.GetDuplicates()),
		ChunksEmpty: int(c.GetChunksEmpty()), ChunksUnread: int(c.GetChunksUnread()),
		Dropped: int(c.GetDropped()),
	}
}

func ModelCallFromProto(m *alchemyv1.ModelCall) alchemy.ModelCall {
	return alchemy.ModelCall{
		Model: m.GetModel(), Stage: m.GetStage(),
		Calls: int(m.GetCalls()), Tokens: int(m.GetTokens()),
	}
}

func UnreadFromProto(u *alchemyv1.Unread) alchemy.Unread {
	return alchemy.Unread{Source: u.GetSource(), Locator: u.GetLocator(), Reason: u.GetReason()}
}

// ResultFromProto turns what the service sends into what every connector in
// connectors/ takes. It is the function that did not exist, and its absence is
// the whole argument for this package: four stores were written against
// alchemy.Result, the API returns alchemyv1.Result, and the step between them
// was left to each buyer to rediscover.
//
// It ignores Result.rules. Those are the `always` rules a job's review
// produced and alchemy.Result deliberately does not carry them — see its
// RuleSets field, which is the job's policy *input* and a different claim. A
// caller who wants the output rules reads them off the message directly; a
// caller who fed one field back as the other would be re-declaring somebody
// else's policy as their own job's finding.
//
// A nil message is an empty result rather than a panic, for the reason at the
// top of this file: a paged read hands this whatever arrived.
func ResultFromProto(r *alchemyv1.Result) alchemy.Result {
	if r == nil {
		return alchemy.Result{}
	}
	return alchemy.Result{
		Job:           r.GetJob(),
		Entities:      Each(r.GetEntities(), EntityFromProto),
		Relations:     Each(r.GetRelations(), RelationFromProto),
		Chunks:        Each(r.GetChunks(), ChunkFromProto),
		Vectors:       Each(r.GetVectors(), VectorFromProto),
		Conflicts:     Each(r.GetConflicts(), ConflictFromProto),
		Violations:    Each(r.GetViolations(), ViolationFromProto),
		Guesses:       Each(r.GetGuesses(), GuessFromProto),
		Duplicates:    Each(r.GetDuplicates(), DuplicateFromProto),
		Counts:        CountsFromProto(r.GetCounts()),
		ModelCalls:    Each(r.GetModelCalls(), ModelCallFromProto),
		Unread:        Each(r.GetUnread(), UnreadFromProto),
		RuleSets:      Each(r.GetRuleSets(), RuleSetFromProto),
		Supersessions: Each(r.GetSupersessions(), SupersessionFromProto),
		Proposals:     Each(r.GetProposals(), ProposalFromProto),
	}
}

func RuleSetFromProto(s *alchemyv1.RuleSet) alchemy.RuleSet {
	return alchemy.RuleSet{Name: s.GetName(), Rules: Each(s.GetRules(), StandingRuleFromProto)}
}

func StandingRuleFromProto(r *alchemyv1.StandingRule) alchemy.StandingRule {
	return alchemy.StandingRule{Name: r.GetName(), Told: r.GetTold()}
}

func SupersessionFromProto(s *alchemyv1.Supersession) alchemy.Supersession {
	return alchemy.Supersession{
		Retires:    s.GetRetires(),
		By:         AboutFromProto(s.GetBy()),
		Reason:     s.GetReason(),
		Provenance: ProvenanceFromProto(s.GetProvenance()),
	}
}

// ProposalFromProto reads a change to a vocabulary back.
//
// Producers is built with Each rather than appended to, so an empty list comes
// back nil and matches what went out. It matters more here than elsewhere: §5b
// asked of a proposal is "who used this type", and a nil list and an empty one
// answer that question identically, so the only thing a difference between
// them could do is break an equality check nobody meant to be about producers.
func ProposalFromProto(p *alchemyv1.Proposal) alchemy.Proposal {
	return alchemy.Proposal{
		Kind:         ProposalKindFromProto[p.GetKind()],
		Type:         p.GetType(),
		Records:      int(p.GetRecords()),
		From:         p.GetFrom(),
		To:           p.GetTo(),
		Sources:      p.GetSources(),
		Producers:    Each(p.GetProducers(), func(pr alchemyv1.Producer) alchemy.Producer { return ProducerFromProto[pr] }),
		DeclaredFrom: p.GetDeclaredFrom(),
		DeclaredTo:   p.GetDeclaredTo(),
		Example:      AboutFromProto(p.GetExample()),
	}
}
