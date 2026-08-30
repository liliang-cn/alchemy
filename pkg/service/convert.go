package service

import (
	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/wire"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The wire types and the Go types are kept apart deliberately. A pipeline that
// spoke protobuf structs would have gRPC in its signatures forever, and
// pkg/alchemy would stop being the contract §5 says it is — the moment a stage
// takes an *alchemyv1.Entity, the REST gateway of §6 stops being a translation
// and becomes a second source of truth.
//
// The conversion itself is no longer here. It is pkg/wire, and it moved for a
// reason this file could not fix from where it sat: these functions were
// unexported, so the service could turn an alchemy.Result into a message and
// nobody outside could turn a message back. A buyer who fetched a graph over
// gRPC or over the REST gateway and wanted to load it into one of the four
// connectors — every one of which takes an alchemy.Result — had to write the
// converter themselves, which is precisely the defect that produced four
// stores each inventing edge identity, provenance handling and a content
// address. See pkg/wire's package comment.
//
// What is left below is the names this package's own code and tests call, each
// one line, forwarding. They stay because they read better at the call sites
// inside a service that has no other business with the wire — and because a
// second copy of any of these bodies is exactly what pkg/wire exists to
// prevent, so there is none.

func jobToProto(j alchemy.Job) *alchemyv1.Job { return wire.JobToProto(j) }

func entityToProto(e alchemy.Entity) *alchemyv1.Entity { return wire.EntityToProto(e) }

func relationToProto(r alchemy.Relation) *alchemyv1.Relation { return wire.RelationToProto(r) }

func chunkToProto(c alchemy.Chunk) *alchemyv1.Chunk { return wire.ChunkToProto(c) }

func vectorToProto(v alchemy.Vector) *alchemyv1.Vector { return wire.VectorToProto(v) }

func violationToProto(v alchemy.Violation) *alchemyv1.Violation { return wire.ViolationToProto(v) }

func guessToProto(g alchemy.Guess) *alchemyv1.Guess { return wire.GuessToProto(g) }

func conflictToProto(c alchemy.Conflict) *alchemyv1.Conflict { return wire.ConflictToProto(c) }

func duplicateToProto(d alchemy.Duplicate) *alchemyv1.Duplicate { return wire.DuplicateToProto(d) }

func countsToProto(c alchemy.Counts) *alchemyv1.Counts { return wire.CountsToProto(c) }

func modelCallToProto(m alchemy.ModelCall) *alchemyv1.ModelCall { return wire.ModelCallToProto(m) }

func unreadToProto(u alchemy.Unread) *alchemyv1.Unread { return wire.UnreadToProto(u) }

func resultToProto(r alchemy.Result) *alchemyv1.Result { return wire.ResultToProto(r) }

func ruleSetToProto(s alchemy.RuleSet) *alchemyv1.RuleSet { return wire.RuleSetToProto(s) }

func refToProto(r review.Ref) *alchemyv1.Ref { return wire.RefToProto(r) }

func refFromProto(r *alchemyv1.Ref) review.Ref { return wire.RefFromProto(r) }

func decisionFromProto(d *alchemyv1.ReviewDecision) review.Decision {
	return wire.DecisionFromProto(d)
}

func ruleToProto(r *review.Rule) *alchemyv1.ReviewRule { return wire.RuleToProto(r) }

func ruleFromProto(r *alchemyv1.ReviewRule) review.Rule { return wire.RuleFromProto(r) }

func itemToProto(jobID string, it review.Item) *alchemyv1.ReviewItem {
	return wire.ItemToProto(jobID, it)
}

func each[T any, R any](in []T, f func(T) R) []R { return wire.Each(in, f) }

// eventToProto stays here rather than moving to pkg/wire, and the line is
// drawn at the type rather than at the direction: Event is this package's, not
// alchemy's, so a conversion for it in pkg/wire would make that package know
// about the service's own progress reporting. pkg/wire is the correspondence
// between the published contract and the wire, and nothing else belongs in it.
func eventToProto(state alchemy.JobState, e Event) *alchemyv1.JobEvent {
	out := &alchemyv1.JobEvent{
		State:             wire.JobStateToProto[state],
		Stage:             e.Stage,
		Counts:            wire.CountsToProto(e.Counts),
		ModelCalls:        e.ModelCalls,
		ModelCallsByStage: wire.Each(e.ByStage, wire.ModelCallToProto),
		Message:           e.Message,
	}
	if !e.At.IsZero() {
		out.At = timestamppb.New(e.At)
	}
	if e.Conflict != nil {
		out.Conflict = wire.ConflictToProto(*e.Conflict)
	}
	return out
}
