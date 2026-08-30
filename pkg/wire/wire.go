// Package wire is the one place that knows how alchemy's types and the
// generated protobuf types correspond, in both directions, exported.
//
// It exists because the alternative is every consumer writing this themselves,
// and there is measured evidence of what that costs. Four stores — Neo4j,
// pgvector, Qdrant and CortexDB — were written against alchemy.Result with no
// sight of each other, and each had to invent edge identity, a provenance
// convention and a content address for itself. pkg/sink was drawn along the
// line those four found; this package is the same argument one layer down.
// The service returns *alchemyv1.Result over gRPC and the REST gateway returns
// protobuf JSON, and until this package there was no exported way to get from
// either back to the alchemy.Result all four connectors take. A buyer who
// fetched a graph and wanted to load it into their own store had to write the
// converter — which is to say, had to re-derive by reading the proto file
// which enum member means which Go constant, thirteen closed sets at a time.
//
// It is also the answer to a trap. §4 says the JSON is the contract, and a
// consumer who takes that at its word and unmarshals the gateway's HTTP body
// straight into an alchemy.Result gets a document that parses cleanly and is
// wrong: protojson spells alchemy.ProducerGraphImport "PRODUCER_GRAPH_IMPORT"
// where the Go type's own JSON tag is "graph-import", so Producer.Deterministic
// returns false for every record and a graph that was entirely read out of
// schemas reports itself as entirely inferred. No error, no log line. The two
// dialects are not interchangeable and this package is the bridge between
// them; see the test named for what that mistake costs.
//
// # The two directions are tables, not string parsing
//
// Every closed set crosses in an explicit map and its inverse. A closed set on
// the wire that silently accepted an unknown value would be an open set
// wearing the same name, which is exactly what the comment on
// alchemy.ViolationKind refuses. The pairs are declared once and the reverse
// is derived by Invert, so the two halves of a mapping cannot drift apart by
// somebody editing one of them.
//
// A Go map returns the zero value for a key it does not have, so a member
// added to alchemy and forgotten here does not fail — it serialises as
// UNSPECIFIED. That is not a hypothesis: the producer table was missing
// alchemy.ProducerHuman and every human-asserted record went out saying nobody
// had made it. TestEveryProducerHasAWireName and TestEveryClosedSetHasAWireName
// are why that cannot happen twice.
//
// # Why the Go types and the wire types are kept apart at all
//
// A pipeline that spoke protobuf structs would have gRPC in its signatures
// forever, and pkg/alchemy would stop being the contract §5 says it is: the
// moment a stage takes an *alchemyv1.Entity, the REST gateway of §6 stops
// being a translation and becomes a second source of truth. This file is the
// price of that separation, and paying it once in public is cheaper than
// having four buyers pay it each in private.
package wire

import (
	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// The tables below are named for the direction they convert in, matching the
// functions that use them: XToProto is keyed by the Go value, XFromProto by
// the wire value. They moved here from pkg/service, where they were called
// `producers` and `wireProducers` and were unexported — which is the whole of
// what made this package necessary.

// JobStateToProto has no inverse because nothing reads one back: a job's state
// is the service's answer to a caller, never a caller's claim to the service.
var JobStateToProto = map[alchemy.JobState]alchemyv1.JobState{
	alchemy.JobPending:     alchemyv1.JobState_JOB_STATE_PENDING,
	alchemy.JobRunning:     alchemyv1.JobState_JOB_STATE_RUNNING,
	alchemy.JobNeedsReview: alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW,
	alchemy.JobSucceeded:   alchemyv1.JobState_JOB_STATE_SUCCEEDED,
	alchemy.JobFailed:      alchemyv1.JobState_JOB_STATE_FAILED,
	alchemy.JobExpired:     alchemyv1.JobState_JOB_STATE_EXPIRED,
	alchemy.JobCancelled:   alchemyv1.JobState_JOB_STATE_CANCELLED,
}

// SourceKindFromProto is declared in the direction it is read in — a source
// kind arrives from a caller's upload and is checked with the two-value form,
// because an unrecognised kind there is a request to refuse rather than a zero
// value to carry on with.
var SourceKindFromProto = map[alchemyv1.SourceKind]alchemy.SourceKind{
	alchemyv1.SourceKind_SOURCE_KIND_TABULAR:  alchemy.SourceTabular,
	alchemyv1.SourceKind_SOURCE_KIND_DDL:      alchemy.SourceDDL,
	alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT: alchemy.SourceDocument,
	alchemyv1.SourceKind_SOURCE_KIND_GRAPH:    alchemy.SourceGraph,
}

// SourceKindToProto is SourceKindFromProto read the other way.
var SourceKindToProto = Invert(SourceKindFromProto)

// ProducerToProto is the table §5b rests on. A miss here does not report an
// error; it reports that nobody made the record.
var ProducerToProto = map[alchemy.Producer]alchemyv1.Producer{
	alchemy.ProducerDDL:         alchemyv1.Producer_PRODUCER_DDL,
	alchemy.ProducerGraphImport: alchemyv1.Producer_PRODUCER_GRAPH_IMPORT,
	alchemy.ProducerTabular:     alchemyv1.Producer_PRODUCER_TABULAR,
	alchemy.ProducerLLMExtract:  alchemyv1.Producer_PRODUCER_LLM_EXTRACT,
	alchemy.ProducerHuman:       alchemyv1.Producer_PRODUCER_HUMAN,
}

// ProducerFromProto is ProducerToProto read the other way.
var ProducerFromProto = Invert(ProducerToProto)

// ProposalKindToProto distinguishes the three changes a proposal can ask for.
// They go in different lists of an ontology and the third one widens a
// declaration rather than adding one, so a consumer applying a proposal that
// arrived as UNSPECIFIED would be changing a vocabulary without being told
// which kind of change it is.
var ProposalKindToProto = map[alchemy.ProposalKind]alchemyv1.ProposalKind{
	alchemy.ProposalEntity:       alchemyv1.ProposalKind_PROPOSAL_KIND_ENTITY,
	alchemy.ProposalRelation:     alchemyv1.ProposalKind_PROPOSAL_KIND_RELATION,
	alchemy.ProposalRelationEnds: alchemyv1.ProposalKind_PROPOSAL_KIND_RELATION_ENDS,
}

// ProposalKindFromProto is ProposalKindToProto read the other way.
var ProposalKindFromProto = Invert(ProposalKindToProto)

var ViolationKindToProto = map[alchemy.ViolationKind]alchemyv1.ViolationKind{
	alchemy.ViolationUnknownEntityType:   alchemyv1.ViolationKind_VIOLATION_KIND_UNKNOWN_ENTITY_TYPE,
	alchemy.ViolationUnknownRelationType: alchemyv1.ViolationKind_VIOLATION_KIND_UNKNOWN_RELATION_TYPE,
	alchemy.ViolationRelationNotAllowed:  alchemyv1.ViolationKind_VIOLATION_KIND_RELATION_NOT_ALLOWED,
	alchemy.ViolationDanglingRelation:    alchemyv1.ViolationKind_VIOLATION_KIND_DANGLING_RELATION,
	alchemy.ViolationMalformedRow:        alchemyv1.ViolationKind_VIOLATION_KIND_MALFORMED_ROW,
	alchemy.ViolationUnnamedColumn:       alchemyv1.ViolationKind_VIOLATION_KIND_UNNAMED_COLUMN,
	alchemy.ViolationMissingID:           alchemyv1.ViolationKind_VIOLATION_KIND_MISSING_ID,
	alchemy.ViolationDuplicateID:         alchemyv1.ViolationKind_VIOLATION_KIND_DUPLICATE_ID,
}

// ViolationKindFromProto is ViolationKindToProto read the other way.
var ViolationKindFromProto = Invert(ViolationKindToProto)

var ConflictKindToProto = map[alchemy.ConflictKind]alchemyv1.ConflictKind{
	alchemy.ConflictEntityAttributes:   alchemyv1.ConflictKind_CONFLICT_KIND_ENTITY_ATTRIBUTES,
	alchemy.ConflictEntityType:         alchemyv1.ConflictKind_CONFLICT_KIND_ENTITY_TYPE,
	alchemy.ConflictRelationDirection:  alchemyv1.ConflictKind_CONFLICT_KIND_RELATION_DIRECTION,
	alchemy.ConflictContradiction:      alchemyv1.ConflictKind_CONFLICT_KIND_CONTRADICTION,
	alchemy.ConflictRelationAttributes: alchemyv1.ConflictKind_CONFLICT_KIND_RELATION_ATTRIBUTES,
	alchemy.ConflictCardinality:        alchemyv1.ConflictKind_CONFLICT_KIND_CARDINALITY,
}

// ConflictKindFromProto is ConflictKindToProto read the other way. A conflict
// holds a job (§7.3), so one that reaches a caller as UNSPECIFIED is a job
// held for a reason nobody can read.
var ConflictKindFromProto = Invert(ConflictKindToProto)

var ReviewKindToProto = map[review.Kind]alchemyv1.ReviewKind{
	review.KindConflict:      alchemyv1.ReviewKind_REVIEW_KIND_CONFLICT,
	review.KindViolation:     alchemyv1.ReviewKind_REVIEW_KIND_VIOLATION,
	review.KindGuess:         alchemyv1.ReviewKind_REVIEW_KIND_GUESS,
	review.KindDuplicate:     alchemyv1.ReviewKind_REVIEW_KIND_DUPLICATE,
	review.KindLowConfidence: alchemyv1.ReviewKind_REVIEW_KIND_LOW_CONFIDENCE,
}

// ReviewKindFromProto is ReviewKindToProto read the other way.
var ReviewKindFromProto = Invert(ReviewKindToProto)

var DuplicateSignalToProto = map[alchemy.DuplicateSignal]alchemyv1.DuplicateSignal{
	alchemy.DuplicateNameAffix:           alchemyv1.DuplicateSignal_DUPLICATE_SIGNAL_NAME_AFFIX,
	alchemy.DuplicateNameAcrossProducers: alchemyv1.DuplicateSignal_DUPLICATE_SIGNAL_NAME_ACROSS_PRODUCERS,
	alchemy.DuplicateAlias:               alchemyv1.DuplicateSignal_DUPLICATE_SIGNAL_ALIAS,
}

// DuplicateSignalFromProto is DuplicateSignalToProto read the other way. A
// reviewer answering "are these the same?" is entitled to know what asked
// them, and a signal that arrived as UNSPECIFIED tells them nothing at all.
var DuplicateSignalFromProto = Invert(DuplicateSignalToProto)

// VerbFromProto is declared in the direction it is read in: a verb arrives
// from a reviewer and the service acts on it.
var VerbFromProto = map[alchemyv1.ReviewVerb]review.Verb{
	alchemyv1.ReviewVerb_REVIEW_VERB_ACCEPT: review.VerbAccept,
	alchemyv1.ReviewVerb_REVIEW_VERB_REJECT: review.VerbReject,
	alchemyv1.ReviewVerb_REVIEW_VERB_EDIT:   review.VerbEdit,
	alchemyv1.ReviewVerb_REVIEW_VERB_ALWAYS: review.VerbAlways,
}

// VerbToProto is VerbFromProto read the other way.
var VerbToProto = Invert(VerbFromProto)

var RefKindToProto = map[alchemy.RefKind]alchemyv1.RefKind{
	alchemy.RefEntity:   alchemyv1.RefKind_REF_KIND_ENTITY,
	alchemy.RefRelation: alchemyv1.RefKind_REF_KIND_RELATION,
}

// RefKindFromProto is RefKindToProto read the other way.
var RefKindFromProto = Invert(RefKindToProto)

// OriginToProto maps review's two warrants onto the wire. Prefer
// RuleOriginToProto for converting a value: a Go zero Origin means a
// reviewer's rule, and only the function says so.
var OriginToProto = map[review.Origin]alchemyv1.RuleOrigin{
	review.OriginReviewed: alchemyv1.RuleOrigin_RULE_ORIGIN_REVIEWED,
	review.OriginAuthored: alchemyv1.RuleOrigin_RULE_ORIGIN_AUTHORED,
}

// OriginFromProto is OriginToProto read the other way. UNSPECIFIED is
// deliberately absent from it so that the wire default decodes to review's
// zero value, which is a reviewer's rule: every rule that could exist before
// this field did was minted from a decision, and the wire default has to mean
// what the callers who wrote it meant. It is also the direction that fails
// safe — a lost marker under-claims a rule's warrant rather than over-claiming
// it.
var OriginFromProto = Invert(OriginToProto)

// Invert builds the other direction of a table, so the two halves of a mapping
// cannot drift apart by somebody editing one of them.
func Invert[K, V comparable](in map[K]V) map[V]K {
	out := make(map[V]K, len(in))
	for k, v := range in {
		out[v] = k
	}
	return out
}

// Each maps a slice, and returns nil rather than an empty slice for empty
// input. That is not a micro-optimisation: alchemy's own JSON omits empty
// lists, so a converter that produced []T{} where the original had nil would
// make the round trip lossy in the one direction nobody would think to test.
func Each[T any, R any](in []T, f func(T) R) []R {
	if len(in) == 0 {
		return nil
	}
	out := make([]R, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}
