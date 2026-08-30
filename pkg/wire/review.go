package wire

import (
	"github.com/liliang-cn/alchemy/pkg/review"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This file crosses pkg/review rather than pkg/alchemy: the queue, the answers
// a person gave it, and the rules those answers minted. It is separate from
// the graph converters because it is a separate contract — a graph is what a
// job produced and this is what a person did to it — and because §5c's
// vocabulary (item, verb, decision, rule, origin) is worth reading in one
// place rather than interleaved with entities and chunks.

// RefToProto carries a review target: which record, as told by which source.
//
// It is AboutToProto plus the provenance, and the extra field is the whole
// difference between the two types. A review target keeps its provenance
// because it is what narrows a decision to the records one source produced,
// and without it a rejection deletes the side of a conflict the reviewer kept.
//
// A zero review.Ref still produces a message here, where AboutToProto would
// produce none. That is deliberate and not an oversight of the same rule: a
// target is what a decision acts on, so a decision with no target is a thing
// the reader has to be able to see arriving, rather than a field that silently
// is not there.
func RefToProto(r review.Ref) *alchemyv1.Ref {
	out := AboutToProto(r.Ref)
	if out == nil {
		out = &alchemyv1.Ref{}
	}
	out.Provenance = ProvenanceToProto(r.Provenance)
	return out
}

// RefFromProto is RefToProto's inverse.
func RefFromProto(r *alchemyv1.Ref) review.Ref {
	return review.Ref{
		Ref:        AboutFromProto(r),
		Provenance: ProvenanceFromProto(r.GetProvenance()),
	}
}

// EditToProto carries a correction. A nil Edit stays nil: review.Edit's empty
// fields already mean "the reviewer did not touch this", so an empty message
// would say a reviewer made an edit that changes nothing — which review.Apply
// treats as an error, because somebody who pressed Edit believes they changed
// something.
func EditToProto(e *review.Edit) *alchemyv1.Edit {
	if e == nil {
		return nil
	}
	return &alchemyv1.Edit{Type: e.Type, Name: e.Name, From: e.From, To: e.To, Into: e.Into}
}

// EditFromProto is EditToProto's inverse, and keeps nil meaning nil.
func EditFromProto(e *alchemyv1.Edit) *review.Edit {
	if e == nil {
		return nil
	}
	return &review.Edit{Type: e.GetType(), Name: e.GetName(), From: e.GetFrom(), To: e.GetTo(), Into: e.GetInto()}
}

// DecisionToProto carries one answer. The job id is a parameter because a
// decision does not carry one — it is an answer to an item, and which job the
// item belongs to is the stream's business — while the message needs one so
// that a decision arriving on its own is addressable.
func DecisionToProto(jobID string, d review.Decision) *alchemyv1.ReviewDecision {
	out := &alchemyv1.ReviewDecision{
		JobId: jobID, ItemId: d.ItemID, Verb: VerbToProto[d.Verb], By: d.By,
		Edit: EditToProto(d.Edit), Note: d.Note,
	}
	if !d.At.IsZero() {
		out.At = timestamppb.New(d.At)
	}
	return out
}

// DecisionFromProto is DecisionToProto's inverse. It drops the job id, which
// belongs to the envelope and not to the answer.
func DecisionFromProto(d *alchemyv1.ReviewDecision) review.Decision {
	out := review.Decision{
		ItemID: d.GetItemId(), Verb: VerbFromProto[d.GetVerb()], By: d.GetBy(),
		Edit: EditFromProto(d.GetEdit()), Note: d.GetNote(),
	}
	if at := d.GetAt(); at != nil {
		out.At = at.AsTime()
	}
	return out
}

// RuleToProto carries a class of question that has already been answered.
func RuleToProto(r *review.Rule) *alchemyv1.ReviewRule {
	if r == nil {
		return nil
	}
	return &alchemyv1.ReviewRule{
		Shape: r.Shape, Kind: ReviewKindToProto[r.Kind],
		From: DecisionToProto("", r.From), Because: r.Because,
		// Sent explicitly even for a reviewer's rule, rather than left at the
		// default. A reader of the wire should not have to know which of two
		// meanings the zero value carries in order to tell the two claims
		// apart, and the whole point of the field is that they are different
		// claims.
		Origin: RuleOriginToProto(r.Origin),
	}
}

// RuleFromProto is RuleToProto's inverse.
func RuleFromProto(r *alchemyv1.ReviewRule) review.Rule {
	return review.Rule{
		Shape: r.GetShape(), Kind: ReviewKindFromProto[r.GetKind()],
		Origin: OriginFromProto[r.GetOrigin()],
		From:   DecisionFromProto(r.GetFrom()), Because: r.GetBecause(),
	}
}

// RuleOriginToProto names a reviewer's rule on the wire even when the Go value
// left it at the zero, which is what a table lookup could not do: review's zero
// Origin means OriginReviewed, and a map from it would send UNSPECIFIED and
// make the weaker warrant indistinguishable from the stronger. See
// OriginFromProto for why the other direction is the one that may rely on the
// zero value.
func RuleOriginToProto(o review.Origin) alchemyv1.RuleOrigin {
	if o == review.OriginAuthored {
		return alchemyv1.RuleOrigin_RULE_ORIGIN_AUTHORED
	}
	return alchemyv1.RuleOrigin_RULE_ORIGIN_REVIEWED
}

// ItemToProto carries one question in a reviewer's queue, with the rule that
// already answered it when there is one — an item carrying a SuppressedBy is
// not a question, it is an answer already given, kept in the queue rather than
// dropped from it.
func ItemToProto(jobID string, it review.Item) *alchemyv1.ReviewItem {
	return &alchemyv1.ReviewItem{
		JobId: jobID, Id: it.ID, Kind: ReviewKindToProto[it.Kind], Rank: int32(it.Rank),
		Index: int32(it.Index), Subject: it.Subject, Summary: it.Summary,
		Shape: it.Shape, Targets: Each(it.Targets, RefToProto),
		SuppressedBy: RuleToProto(it.SuppressedBy),
		Provenance:   ProvenanceToProto(it.Provenance),
	}
}

// ItemFromProto is ItemToProto's inverse, for a client that works a queue it
// received rather than one it built. It drops the job id for the reason
// DecisionFromProto does.
func ItemFromProto(it *alchemyv1.ReviewItem) review.Item {
	out := review.Item{
		ID: it.GetId(), Kind: ReviewKindFromProto[it.GetKind()], Rank: int(it.GetRank()),
		Index: int(it.GetIndex()), Subject: it.GetSubject(), Summary: it.GetSummary(),
		Shape: it.GetShape(), Targets: Each(it.GetTargets(), RefFromProto),
		Provenance: ProvenanceFromProto(it.GetProvenance()),
	}
	// Nil stays nil: review.Open asks exactly this pointer whether a person
	// still has to answer the item, so an empty rule here would silently
	// remove a question from every queue built out of a received one.
	if r := it.GetSuppressedBy(); r != nil {
		rule := RuleFromProto(r)
		out.SuppressedBy = &rule
	}
	return out
}
