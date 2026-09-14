package wire

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// Putting a paged result back together.
//
// StreamResult exists because §8.4 says a large result is not one message, and
// GetResult refuses one that does not fit rather than truncating it. That half
// was built and this half was not, so every consumer that met a graph over the
// limit met a refusal naming a method it would have had to reassemble itself —
// and none of them did. A sixty-seven document corpus produced a 3.05MB result
// and could not be loaded into a store at all: extracted, reviewed, answered,
// and stuck at the last step, which is the worst place for a pipeline to stop.
//
// It lives here rather than in any one consumer because there is one right way
// to do it and several plausible wrong ones. The summary rides on the first
// page only, so a reassembler that took Counts from the last page would report
// zero; the pages are ordered and a reassembler that ignored Page would not
// notice a gap; and the job identity is on page zero precisely so a consumer
// does not fill it in from what it remembers asking for. All three are the
// kind of mistake that produces a plausible result rather than an error.

// Pages reassembles an ordered sequence of ResultPage into the Result they
// were cut from.
//
// The pages must arrive in the order the server sent them, which is the order
// a gRPC server stream delivers. A page out of sequence, a missing one, or a
// sequence with no final page is an error rather than a smaller graph: a
// consumer about to write this into a store has no way to tell a corpus that
// was small from one it received three quarters of, and that is exactly the
// distinction §8.4 refuses to blur.
func Pages(pages []*alchemyv1.ResultPage) (alchemy.Result, error) {
	if len(pages) == 0 {
		return alchemy.Result{}, fmt.Errorf("wire: no pages: a result is at least one page, even when it is empty")
	}
	var out alchemy.Result
	for i, p := range pages {
		if p == nil {
			return alchemy.Result{}, fmt.Errorf("wire: page %d is missing", i)
		}
		if got := int(p.GetPage()); got != i {
			return alchemy.Result{}, fmt.Errorf("wire: page %d arrived where page %d was expected; a gap in a paged graph is not a smaller graph", got, i)
		}
		if p.GetLast() && i != len(pages)-1 {
			return alchemy.Result{}, fmt.Errorf("wire: page %d says it is the last and %d more followed", i, len(pages)-1-i)
		}
		if i == 0 {
			// The summary and the identity ride once, on page zero.
			out.Job = p.GetJob()
			out.Counts = CountsFromProto(p.GetCounts())
			out.ModelCalls = Each(p.GetModelCalls(), ModelCallFromProto)
			out.Unread = Each(p.GetUnread(), UnreadFromProto)
			out.RuleSets = Each(p.GetRuleSets(), RuleSetFromProto)
		}
		out.Entities = append(out.Entities, Each(p.GetEntities(), EntityFromProto)...)
		out.Relations = append(out.Relations, Each(p.GetRelations(), RelationFromProto)...)
		out.Chunks = append(out.Chunks, Each(p.GetChunks(), ChunkFromProto)...)
		out.Vectors = append(out.Vectors, Each(p.GetVectors(), VectorFromProto)...)
		out.Conflicts = append(out.Conflicts, Each(p.GetConflicts(), ConflictFromProto)...)
		out.Violations = append(out.Violations, Each(p.GetViolations(), ViolationFromProto)...)
		out.Guesses = append(out.Guesses, Each(p.GetGuesses(), GuessFromProto)...)
		out.Duplicates = append(out.Duplicates, Each(p.GetDuplicates(), DuplicateFromProto)...)
		out.Supersessions = append(out.Supersessions, Each(p.GetSupersessions(), SupersessionFromProto)...)
		out.Proposals = append(out.Proposals, Each(p.GetProposals(), ProposalFromProto)...)
	}
	if !pages[len(pages)-1].GetLast() {
		return alchemy.Result{}, fmt.Errorf(
			"wire: the stream ended without a last page, so this is part of a graph and there is no way to tell how much")
	}
	return out, nil
}
