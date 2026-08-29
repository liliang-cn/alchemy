package service

import (
	"context"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/job"
	"github.com/liliang-cn/alchemy/pkg/review"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// GetResult returns a finished graph in one message, or refuses.
//
// It refuses in two situations and they are different sentences on purpose. A
// job that is not finished has no result yet — including a held one, because
// §7.3's whole point is that a graph with an unanswered conflict is not a
// finished graph and handing it back as one would be the confident wrong
// answer this design exists to prevent. And a result that will not fit in one
// message is refused rather than truncated (§8.4): a caller should never have
// to discover gRPC's 4MB limit by receiving three quarters of a graph.
func (s *Server) GetResult(ctx context.Context, req *alchemyv1.GetResultRequest) (*alchemyv1.Result, error) {
	res, err := s.finished(ctx, req.GetJobId())
	if err != nil {
		return nil, wireError(err)
	}
	out := resultToProto(res)
	out.Rules = s.rulesOf(req.GetJobId())
	if n := proto.Size(out); n > s.cfg.MaxResultBytes {
		return nil, wireError(tooLarge(
			"result of job %s is %d bytes, over the %d byte limit for one message; use StreamResult, which pages it",
			req.GetJobId(), n, s.cfg.MaxResultBytes))
	}
	return out, nil
}

// StreamResult pages a large result. §8.4: a big result is not one message.
//
// The summary rides on the first page rather than the last. An operator
// deciding whether to read a two-hundred-thousand-record graph is deciding on
// §5's numbers, and making them arrive after every entity would mean paying
// for the graph in order to learn whether to trust it.
func (s *Server) StreamResult(req *alchemyv1.GetResultRequest, stream grpc.ServerStreamingServer[alchemyv1.ResultPage]) error {
	ctx := stream.Context()
	res, err := s.finished(ctx, req.GetJobId())
	if err != nil {
		return wireError(err)
	}

	size := int(req.GetPageSize())
	if size <= 0 {
		size = s.cfg.PageSize
	}
	p := &pager{res: res, size: size}
	for n := 0; ; n++ {
		// Checked every page rather than at the start: a client that hangs up
		// halfway through a large graph should stop the send loop then, not
		// after the last page it will never read.
		if err := ctx.Err(); err != nil {
			return wireError(err)
		}
		page, more := p.next()
		page.Page = int32(n)
		page.Last = !more
		if n == 0 {
			page.Counts = countsToProto(res.Counts)
			page.ModelCalls = each(res.ModelCalls, modelCallToProto)
			page.Unread = each(res.Unread, unreadToProto)
			page.Rules = s.rulesOf(req.GetJobId())
		}
		if err := stream.Send(page); err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
}

// finished is the one place that decides whether there is a result to hand
// over, so GetResult and StreamResult cannot disagree about a held job.
func (s *Server) finished(ctx context.Context, id string) (alchemy.Result, error) {
	if id == "" {
		return alchemy.Result{}, invalid("get_result: no job ID")
	}
	j, err := s.store.Get(ctx, id)
	if err != nil {
		return alchemy.Result{}, err
	}
	r := s.runFor(id)
	if r == nil {
		return alchemy.Result{}, job.ErrNotFound
	}
	switch j.State {
	case alchemy.JobSucceeded:
	case alchemy.JobNeedsReview:
		res, _ := r.pending()
		return alchemy.Result{}, wrongState(
			"job %s is held for a person: %d conflict(s) are unanswered, and a graph that contradicts itself is not a finished graph. Answer them on the Review stream",
			id, len(review.Held(res)))
	default:
		return alchemy.Result{}, wrongState("job %s is %s, so it has no result", id, j.State)
	}
	res, ok := r.pending()
	if !ok {
		return alchemy.Result{}, wrongState("job %s finished without a result", id)
	}
	return res, nil
}

// rulesOf is the policy this job's review wrote down, for the caller to keep
// and supply to the next job.
func (s *Server) rulesOf(id string) []*alchemyv1.ReviewRule {
	r := s.runFor(id)
	if r == nil {
		return nil
	}
	rules := r.recorded()
	out := make([]*alchemyv1.ReviewRule, 0, len(rules))
	for i := range rules {
		out = append(out, ruleToProto(&rules[i]))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pager walks a result once, in a fixed order, handing out pages of at most
// size records.
//
// The order puts the findings first — conflicts, then violations, then guesses
// — for the same reason the summary rides on the first page: they are what a
// reader needs in order to judge the graph, and a client that stops reading
// after two pages should have stopped with the warnings in hand rather than
// with the first thousand entities.
type pager struct {
	res  alchemy.Result
	size int
	at   [7]int
}

func (p *pager) next() (*alchemyv1.ResultPage, bool) {
	page := &alchemyv1.ResultPage{}
	left := p.size

	page.Conflicts, left = takeInto(p.res.Conflicts, &p.at[0], left, conflictToProto)
	page.Violations, left = takeInto(p.res.Violations, &p.at[1], left, violationToProto)
	page.Guesses, left = takeInto(p.res.Guesses, &p.at[2], left, guessToProto)
	page.Entities, left = takeInto(p.res.Entities, &p.at[3], left, entityToProto)
	page.Relations, left = takeInto(p.res.Relations, &p.at[4], left, relationToProto)
	page.Chunks, left = takeInto(p.res.Chunks, &p.at[5], left, chunkToProto)
	page.Vectors, _ = takeInto(p.res.Vectors, &p.at[6], left, vectorToProto)

	return page, p.more()
}

// more reports whether anything is left. It compares cursors against lengths
// rather than tracking a remaining count, so a category added to Result and
// forgotten in next() shows up as a page that never ends rather than as
// records that silently vanish.
func (p *pager) more() bool {
	return p.at[0] < len(p.res.Conflicts) ||
		p.at[1] < len(p.res.Violations) ||
		p.at[2] < len(p.res.Guesses) ||
		p.at[3] < len(p.res.Entities) ||
		p.at[4] < len(p.res.Relations) ||
		p.at[5] < len(p.res.Chunks) ||
		p.at[6] < len(p.res.Vectors)
}

func takeInto[T, R any](in []T, at *int, budget int, f func(T) R) ([]R, int) {
	if budget <= 0 || *at >= len(in) {
		return nil, budget
	}
	end := min(*at+budget, len(in))
	out := each(in[*at:end], f)
	budget -= end - *at
	*at = end
	return out, budget
}
