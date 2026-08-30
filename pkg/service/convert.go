package service

import (
	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The wire types and the Go types are kept apart deliberately, and this file is
// the price of that. A pipeline that spoke protobuf structs would have gRPC in
// its signatures forever, and pkg/alchemy would stop being the contract §5
// says it is — the moment a stage takes an *alchemyv1.Entity, the REST gateway
// of §6 stops being a translation and becomes a second source of truth.
//
// The enum maps go both ways through explicit tables rather than through
// string parsing. A closed set on the wire that silently accepted an unknown
// value would be an open set wearing the same name, which is exactly what the
// comment on ViolationKind in pkg/alchemy refuses.

var jobStates = map[alchemy.JobState]alchemyv1.JobState{
	alchemy.JobPending:     alchemyv1.JobState_JOB_STATE_PENDING,
	alchemy.JobRunning:     alchemyv1.JobState_JOB_STATE_RUNNING,
	alchemy.JobNeedsReview: alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW,
	alchemy.JobSucceeded:   alchemyv1.JobState_JOB_STATE_SUCCEEDED,
	alchemy.JobFailed:      alchemyv1.JobState_JOB_STATE_FAILED,
	alchemy.JobExpired:     alchemyv1.JobState_JOB_STATE_EXPIRED,
	alchemy.JobCancelled:   alchemyv1.JobState_JOB_STATE_CANCELLED,
}

var sourceKinds = map[alchemyv1.SourceKind]alchemy.SourceKind{
	alchemyv1.SourceKind_SOURCE_KIND_TABULAR:  alchemy.SourceTabular,
	alchemyv1.SourceKind_SOURCE_KIND_DDL:      alchemy.SourceDDL,
	alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT: alchemy.SourceDocument,
	alchemyv1.SourceKind_SOURCE_KIND_GRAPH:    alchemy.SourceGraph,
}

var wireSourceKinds = invert(sourceKinds)

var producers = map[alchemy.Producer]alchemyv1.Producer{
	alchemy.ProducerDDL:         alchemyv1.Producer_PRODUCER_DDL,
	alchemy.ProducerGraphImport: alchemyv1.Producer_PRODUCER_GRAPH_IMPORT,
	alchemy.ProducerTabular:     alchemyv1.Producer_PRODUCER_TABULAR,
	alchemy.ProducerLLMExtract:  alchemyv1.Producer_PRODUCER_LLM_EXTRACT,
	alchemy.ProducerHuman:       alchemyv1.Producer_PRODUCER_HUMAN,
}

var wireProducers = invert(producers)

var violationKinds = map[alchemy.ViolationKind]alchemyv1.ViolationKind{
	alchemy.ViolationUnknownEntityType:   alchemyv1.ViolationKind_VIOLATION_KIND_UNKNOWN_ENTITY_TYPE,
	alchemy.ViolationUnknownRelationType: alchemyv1.ViolationKind_VIOLATION_KIND_UNKNOWN_RELATION_TYPE,
	alchemy.ViolationRelationNotAllowed:  alchemyv1.ViolationKind_VIOLATION_KIND_RELATION_NOT_ALLOWED,
	alchemy.ViolationDanglingRelation:    alchemyv1.ViolationKind_VIOLATION_KIND_DANGLING_RELATION,
	alchemy.ViolationMalformedRow:        alchemyv1.ViolationKind_VIOLATION_KIND_MALFORMED_ROW,
	alchemy.ViolationUnnamedColumn:       alchemyv1.ViolationKind_VIOLATION_KIND_UNNAMED_COLUMN,
	alchemy.ViolationMissingID:           alchemyv1.ViolationKind_VIOLATION_KIND_MISSING_ID,
	alchemy.ViolationDuplicateID:         alchemyv1.ViolationKind_VIOLATION_KIND_DUPLICATE_ID,
}

var conflictKinds = map[alchemy.ConflictKind]alchemyv1.ConflictKind{
	alchemy.ConflictEntityAttributes:   alchemyv1.ConflictKind_CONFLICT_KIND_ENTITY_ATTRIBUTES,
	alchemy.ConflictEntityType:         alchemyv1.ConflictKind_CONFLICT_KIND_ENTITY_TYPE,
	alchemy.ConflictRelationDirection:  alchemyv1.ConflictKind_CONFLICT_KIND_RELATION_DIRECTION,
	alchemy.ConflictContradiction:      alchemyv1.ConflictKind_CONFLICT_KIND_CONTRADICTION,
	alchemy.ConflictRelationAttributes: alchemyv1.ConflictKind_CONFLICT_KIND_RELATION_ATTRIBUTES,
	alchemy.ConflictCardinality:        alchemyv1.ConflictKind_CONFLICT_KIND_CARDINALITY,
}

var reviewKinds = map[review.Kind]alchemyv1.ReviewKind{
	review.KindConflict:      alchemyv1.ReviewKind_REVIEW_KIND_CONFLICT,
	review.KindViolation:     alchemyv1.ReviewKind_REVIEW_KIND_VIOLATION,
	review.KindGuess:         alchemyv1.ReviewKind_REVIEW_KIND_GUESS,
	review.KindDuplicate:     alchemyv1.ReviewKind_REVIEW_KIND_DUPLICATE,
	review.KindLowConfidence: alchemyv1.ReviewKind_REVIEW_KIND_LOW_CONFIDENCE,
}

var duplicateSignals = map[alchemy.DuplicateSignal]alchemyv1.DuplicateSignal{
	alchemy.DuplicateNameAffix:           alchemyv1.DuplicateSignal_DUPLICATE_SIGNAL_NAME_AFFIX,
	alchemy.DuplicateNameAcrossProducers: alchemyv1.DuplicateSignal_DUPLICATE_SIGNAL_NAME_ACROSS_PRODUCERS,
}

var wireReviewKinds = invert(reviewKinds)

var verbs = map[alchemyv1.ReviewVerb]review.Verb{
	alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT: review.VerbAccept,
	alchemyv1.ReviewVerb_REVIEW_VERB_REJECT: review.VerbReject,
	alchemyv1.ReviewVerb_REVIEW_VERB_EDIT:   review.VerbEdit,
	alchemyv1.ReviewVerb_REVIEW_VERB_ALWAYS: review.VerbAlways,
}

var wireVerbs = invert(verbs)

var refKinds = map[review.RefKind]alchemyv1.RefKind{
	review.RefEntity:   alchemyv1.RefKind_REF_KIND_ENTITY,
	review.RefRelation: alchemyv1.RefKind_REF_KIND_RELATION,
}

var wireRefKinds = invert(refKinds)

// origins map review's two warrants onto the wire. UNSPECIFIED is deliberately
// absent from wireOrigins so that it decodes to review's zero value, which is
// a reviewer's rule: every rule that could exist before this field did was
// minted from a decision, and the wire default has to mean what the callers
// who wrote it meant. It is also the direction that fails safe — a lost marker
// under-claims a rule's warrant rather than over-claiming it.
var origins = map[review.Origin]alchemyv1.RuleOrigin{
	review.OriginReviewed: alchemyv1.RuleOrigin_RULE_ORIGIN_REVIEWED,
	review.OriginAuthored: alchemyv1.RuleOrigin_RULE_ORIGIN_AUTHORED,
}

var wireOrigins = invert(origins)

// invert builds the other direction of a table, so the two halves of a mapping
// cannot drift apart by somebody editing one of them.
func invert[K, V comparable](in map[K]V) map[V]K {
	out := make(map[V]K, len(in))
	for k, v := range in {
		out[v] = k
	}
	return out
}

func jobToProto(j alchemy.Job) *alchemyv1.Job {
	out := &alchemyv1.Job{
		Id:    j.ID,
		State: jobStates[j.State],
		Stage: j.Stage,
		Error: j.Error,
	}
	if !j.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(j.CreatedAt)
	}
	if !j.ExpiresAt.IsZero() {
		out.ExpiresAt = timestamppb.New(j.ExpiresAt)
	}
	return out
}

func provenanceToProto(p alchemy.Provenance) *alchemyv1.Provenance {
	return &alchemyv1.Provenance{
		Source:     p.Source,
		Chunk:      int32(p.Chunk),
		Producer:   producers[p.Producer],
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

func provenanceFromProto(p *alchemyv1.Provenance) alchemy.Provenance {
	if p == nil {
		return alchemy.Provenance{}
	}
	return alchemy.Provenance{
		Source:     p.GetSource(),
		Chunk:      int(p.GetChunk()),
		Producer:   wireProducers[p.GetProducer()],
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

// attributesToProto drops an attribute value protobuf cannot carry rather than
// failing the whole result. The alternative — refusing to return a graph
// because one cell held something structpb has no shape for — would lose the
// other two hundred thousand records over a value nobody asked about.
func attributesToProto(in map[string]any) *structpb.Struct {
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

func entityToProto(e alchemy.Entity) *alchemyv1.Entity {
	return &alchemyv1.Entity{
		Id:         e.ID,
		Type:       e.Type,
		Name:       e.Name,
		Attributes: attributesToProto(e.Attributes),
		Provenance: provenanceToProto(e.Provenance),
	}
}

func relationToProto(r alchemy.Relation) *alchemyv1.Relation {
	return &alchemyv1.Relation{
		From:       r.From,
		To:         r.To,
		Type:       r.Type,
		Key:        r.Key,
		Attributes: attributesToProto(r.Attributes),
		Provenance: provenanceToProto(r.Provenance),
	}
}

func chunkToProto(c alchemy.Chunk) *alchemyv1.Chunk {
	return &alchemyv1.Chunk{
		Index: int32(c.Index), Text: c.Text, Source: c.Source,
		Strategy: c.Strategy, Heading: c.Heading,
		Start: int32(c.Start), End: int32(c.End),
	}
}

func vectorToProto(v alchemy.Vector) *alchemyv1.Vector {
	return &alchemyv1.Vector{Chunk: int32(v.Chunk), Values: v.Values, Model: v.Model}
}

func violationToProto(v alchemy.Violation) *alchemyv1.Violation {
	return &alchemyv1.Violation{
		Kind: violationKinds[v.Kind], Detail: v.Detail, Subject: v.Subject,
		About:      aboutToProto(v.About),
		Provenance: provenanceToProto(v.Provenance),
	}
}

// aboutToProto carries the record a finding is about, in fields.
//
// A zero Ref becomes no message rather than an empty one, and that is the whole
// of what this function adds over refToProto. A violation about a malformed row
// is about a file and not about a graph record, and an empty Ref on the wire
// would say "the entity with no id" — a claim that is both false and joinable,
// which is the worse of the two ways to be wrong.
//
// The provenance field of the Ref is deliberately left unset. The violation
// carries its own beside it, and two copies of one fact on one message is two
// answers that can disagree; see the field's comment in the proto for why a
// review target is the case where it does belong.
func aboutToProto(r alchemy.Ref) *alchemyv1.Ref {
	if r == (alchemy.Ref{}) {
		return nil
	}
	return &alchemyv1.Ref{
		Kind: refKinds[r.Kind], Id: r.ID, From: r.From, To: r.To, Type: r.Type, Key: r.Key,
	}
}

func guessToProto(g alchemy.Guess) *alchemyv1.Guess {
	return &alchemyv1.Guess{
		Field: g.Field, ChosenAs: g.ChosenAs, Alternatives: g.Alternatives,
		Reason: g.Reason, Provenance: provenanceToProto(g.Provenance),
	}
}

func claimToProto(c alchemy.Claim) *alchemyv1.Claim {
	return &alchemyv1.Claim{Statement: c.Statement, Provenance: provenanceToProto(c.Provenance)}
}

func conflictToProto(c alchemy.Conflict) *alchemyv1.Conflict {
	return &alchemyv1.Conflict{
		Kind: conflictKinds[c.Kind], Subject: c.Subject, Detail: c.Detail,
		Left: claimToProto(c.Left), Right: claimToProto(c.Right),
	}
}

func duplicateToProto(d alchemy.Duplicate) *alchemyv1.Duplicate {
	return &alchemyv1.Duplicate{
		Signal: duplicateSignals[d.Signal], Subject: d.Subject, Detail: d.Detail,
		Left: duplicateSideToProto(d.Left), Right: duplicateSideToProto(d.Right),
	}
}

func duplicateSideToProto(s alchemy.DuplicateSide) *alchemyv1.DuplicateSide {
	return &alchemyv1.DuplicateSide{
		Id: s.ID, Type: s.Type, Name: s.Name, Provenance: provenanceToProto(s.Provenance),
	}
}

func countsToProto(c alchemy.Counts) *alchemyv1.Counts {
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

func modelCallToProto(m alchemy.ModelCall) *alchemyv1.ModelCall {
	return &alchemyv1.ModelCall{Model: m.Model, Stage: m.Stage, Calls: int32(m.Calls), Tokens: int32(m.Tokens)}
}

func unreadToProto(u alchemy.Unread) *alchemyv1.Unread {
	return &alchemyv1.Unread{Source: u.Source, Locator: u.Locator, Reason: u.Reason}
}

func each[T any, R any](in []T, f func(T) R) []R {
	if len(in) == 0 {
		return nil
	}
	out := make([]R, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

func resultToProto(r alchemy.Result) *alchemyv1.Result {
	return &alchemyv1.Result{
		// The identity the result already carries. §4 makes the JSON the
		// contract and §6 makes this a translation of it, so a field the
		// document has and the message does not is a second contract.
		Job:           r.Job,
		Entities:      each(r.Entities, entityToProto),
		Relations:     each(r.Relations, relationToProto),
		Chunks:        each(r.Chunks, chunkToProto),
		Vectors:       each(r.Vectors, vectorToProto),
		Conflicts:     each(r.Conflicts, conflictToProto),
		Violations:    each(r.Violations, violationToProto),
		Guesses:       each(r.Guesses, guessToProto),
		Duplicates:    each(r.Duplicates, duplicateToProto),
		Counts:        countsToProto(r.Counts),
		ModelCalls:    each(r.ModelCalls, modelCallToProto),
		Unread:        each(r.Unread, unreadToProto),
		RuleSets:      each(r.RuleSets, ruleSetToProto),
		Supersessions: each(r.Supersessions, supersessionToProto),
	}
}

// ruleSetToProto carries the policy a job's records were extracted under.
//
// It is the resolution table for Provenance.rule_set, and the reason both
// halves are on the same message: a record naming a set the reader has to be
// handed separately is a graph that explains itself only to whoever already
// has the operator's rule file, which is not what §5b promises.
func ruleSetToProto(s alchemy.RuleSet) *alchemyv1.RuleSet {
	return &alchemyv1.RuleSet{Name: s.Name, Rules: each(s.Rules, standingRuleToProto)}
}

func standingRuleToProto(r alchemy.StandingRule) *alchemyv1.StandingRule {
	return &alchemyv1.StandingRule{Name: r.Name, Told: r.Told}
}

func refToProto(r review.Ref) *alchemyv1.Ref {
	out := aboutToProto(r.Ref)
	if out == nil {
		out = &alchemyv1.Ref{}
	}
	// A review target keeps its provenance: it is what narrows a decision to
	// the records one source produced, and without it a rejection deletes the
	// side of a conflict the reviewer kept.
	out.Provenance = provenanceToProto(r.Provenance)
	return out
}

func refFromProto(r *alchemyv1.Ref) review.Ref {
	return review.Ref{
		Ref: alchemy.Ref{
			Kind: wireRefKinds[r.GetKind()], ID: r.GetId(), From: r.GetFrom(),
			To: r.GetTo(), Type: r.GetType(), Key: r.GetKey(),
		},
		Provenance: provenanceFromProto(r.GetProvenance()),
	}
}

func editToProto(e *review.Edit) *alchemyv1.Edit {
	if e == nil {
		return nil
	}
	return &alchemyv1.Edit{Type: e.Type, Name: e.Name, From: e.From, To: e.To, Into: e.Into}
}

func editFromProto(e *alchemyv1.Edit) *review.Edit {
	if e == nil {
		return nil
	}
	return &review.Edit{Type: e.GetType(), Name: e.GetName(), From: e.GetFrom(), To: e.GetTo(), Into: e.GetInto()}
}

func decisionToProto(jobID string, d review.Decision) *alchemyv1.ReviewDecision {
	out := &alchemyv1.ReviewDecision{
		JobId: jobID, ItemId: d.ItemID, Verb: wireVerbs[d.Verb], By: d.By,
		Edit: editToProto(d.Edit), Note: d.Note,
	}
	if !d.At.IsZero() {
		out.At = timestamppb.New(d.At)
	}
	return out
}

func decisionFromProto(d *alchemyv1.ReviewDecision) review.Decision {
	out := review.Decision{
		ItemID: d.GetItemId(), Verb: verbs[d.GetVerb()], By: d.GetBy(),
		Edit: editFromProto(d.GetEdit()), Note: d.GetNote(),
	}
	if at := d.GetAt(); at != nil {
		out.At = at.AsTime()
	}
	return out
}

func ruleToProto(r *review.Rule) *alchemyv1.ReviewRule {
	if r == nil {
		return nil
	}
	return &alchemyv1.ReviewRule{
		Shape: r.Shape, Kind: reviewKinds[r.Kind],
		From: decisionToProto("", r.From), Because: r.Because,
		// Sent explicitly even for a reviewer's rule, rather than left at the
		// default. A reader of the wire should not have to know which of two
		// meanings the zero value carries in order to tell the two claims
		// apart, and the whole point of the field is that they are different
		// claims.
		Origin: originToProto(r.Origin),
	}
}

func ruleFromProto(r *alchemyv1.ReviewRule) review.Rule {
	return review.Rule{
		Shape: r.GetShape(), Kind: wireReviewKinds[r.GetKind()],
		Origin: wireOrigins[r.GetOrigin()],
		From:   decisionFromProto(r.GetFrom()), Because: r.GetBecause(),
	}
}

// originToProto names a reviewer's rule on the wire even when the Go value
// left it at the zero. See origins.
func originToProto(o review.Origin) alchemyv1.RuleOrigin {
	if o == review.OriginAuthored {
		return alchemyv1.RuleOrigin_RULE_ORIGIN_AUTHORED
	}
	return alchemyv1.RuleOrigin_RULE_ORIGIN_REVIEWED
}

func itemToProto(jobID string, it review.Item) *alchemyv1.ReviewItem {
	return &alchemyv1.ReviewItem{
		JobId: jobID, Id: it.ID, Kind: reviewKinds[it.Kind], Rank: int32(it.Rank),
		Index: int32(it.Index), Subject: it.Subject, Summary: it.Summary,
		Shape: it.Shape, Targets: each(it.Targets, refToProto),
		SuppressedBy: ruleToProto(it.SuppressedBy),
		Provenance:   provenanceToProto(it.Provenance),
	}
}

func eventToProto(state alchemy.JobState, e Event) *alchemyv1.JobEvent {
	out := &alchemyv1.JobEvent{
		State:             jobStates[state],
		Stage:             e.Stage,
		Counts:            countsToProto(e.Counts),
		ModelCalls:        e.ModelCalls,
		ModelCallsByStage: each(e.ByStage, modelCallToProto),
		Message:           e.Message,
	}
	if !e.At.IsZero() {
		out.At = timestamppb.New(e.At)
	}
	if e.Conflict != nil {
		out.Conflict = conflictToProto(*e.Conflict)
	}
	return out
}

// supersessionToProto carries a statement that a record is over.
//
// It travels with its own provenance rather than borrowing the superseding
// record's, because a reviewer may retire a record a model proposed: those are
// two claims by two parties and a reader must be able to ask "who says so" of
// each. See alchemy.Supersession.
func supersessionToProto(s alchemy.Supersession) *alchemyv1.Supersession {
	return &alchemyv1.Supersession{
		Retires:    s.Retires,
		By:         aboutToProto(s.By),
		Reason:     s.Reason,
		Provenance: provenanceToProto(s.Provenance),
	}
}
