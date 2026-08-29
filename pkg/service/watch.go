package service

import (
	"context"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/job"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc"
)

// WatchJob streams progress.
//
// §6 chose a server stream over polling or SSE, and §7.2 and §7.3 say what has
// to be on it: a running model-call count, so a job whose bill is growing
// faster than expected can be cancelled while it runs, and a conflict at the
// moment it is found, so an operator watching a two-hour import learns in
// minute three that it will need them.
//
// The first message is the job as it stands, before any event. A client that
// attaches to a job already at "extract" would otherwise see nothing until the
// next tick and have no way to tell a working job from a wedged one.
func (s *Server) WatchJob(req *alchemyv1.WatchJobRequest, stream grpc.ServerStreamingServer[alchemyv1.JobEvent]) error {
	ctx := stream.Context()
	id := req.GetJobId()
	if id == "" {
		return wireError(invalid("watch_job: no job ID"))
	}
	j, err := s.store.Get(ctx, id)
	if err != nil {
		return wireError(err)
	}
	r := s.runFor(id)
	if r == nil {
		return wireError(job.ErrNotFound)
	}

	events, unsubscribe := r.hub.watch()
	// Unsubscribing here is what makes a disconnecting client cost nothing: the
	// hub goes on publishing to the job's other watchers and stops writing to
	// this one, and the channel is closed rather than left for the collector.
	defer unsubscribe()

	// The greeting carries what has been spent so far, not a zero. A watcher
	// that attaches to a job already running reads the first event as the
	// truth about the bill — that is what §7.2 offers a stream for — and a
	// greeting of zero is a job that appears to start over.
	calls, byStage, counts := r.hub.spend()
	if err := stream.Send(eventToProto(j.State, Event{
		At: time.Now(), Stage: j.Stage, ModelCalls: calls, ByStage: byStage, Counts: counts,
	})); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			// The caller hung up or timed out. Neither is this service's
			// failure, and neither should stop the job — §8.1 makes a job a
			// unit of work, not a thing a connection owns.
			return wireError(ctx.Err())
		case e, ok := <-events:
			if !ok {
				return s.sendFinal(stream, id, r)
			}
			if err := stream.Send(eventToProto(s.stateOf(id), e)); err != nil {
				return err
			}
		}
	}
}

// sendFinal reports where the job ended, because the last thing on the stream
// is what a client that watched to the end will act on.
func (s *Server) sendFinal(stream grpc.ServerStreamingServer[alchemyv1.JobEvent], id string, r *jobRun) error {
	j, err := s.store.Get(context.Background(), id)
	if err != nil {
		// The job was deleted while being watched. There is nothing left to
		// report and nothing went wrong, so the stream simply ends.
		return nil
	}
	calls, byStage, counts := r.hub.spend()
	return stream.Send(eventToProto(j.State, Event{
		At: time.Now(), Stage: j.Stage, Message: j.Error,
		ModelCalls: calls, ByStage: byStage, Counts: counts,
	}))
}

// stateOf is the job's state right now, for stamping onto an event the runner
// produced. The runner does not know the state — §7.3 makes that the service's
// decision — so it is read here rather than carried on the Event.
func (s *Server) stateOf(id string) alchemy.JobState {
	j, err := s.store.Get(context.Background(), id)
	if err != nil {
		return ""
	}
	return j.State
}
