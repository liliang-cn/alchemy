package runner

import (
	"time"

	"github.com/liliang-cn/alchemy/pkg/pipeline"
	"github.com/liliang-cn/alchemy/pkg/service"
)

// forward translates the pipeline's progress into the service's, one event at
// a time, until the pipeline closes its side.
//
// It drains to the end rather than returning on the first slow send, because
// the pipeline's contract is that a caller consumes the channel until it
// closes; abandoning it would leave the emitter's goroutine parked on a send
// that nobody will ever receive.
func forward(from <-chan pipeline.Event, to chan<- service.Event) {
	for e := range from {
		if to == nil {
			continue
		}
		to <- translate(e)
	}
}

// translate turns one pipeline event into one service event.
//
// Item is deliberately never set. A review item is built by review.Queue, from
// the finished result, with an Index into the slice the finding lives in, and
// pkg/service offers the queue that way once the run is over. An item minted
// here from a conflict as it was found would carry an index into a graph that
// is still growing, and pkg/review's Apply writes a reviewer's name onto the
// finding at that index — so an early answer would be recorded against the
// wrong one. §7.3's "learn in minute three" is kept by Conflict below, which is
// what a watcher reads, without inventing a queue entry the queue cannot yet
// describe.
func translate(e pipeline.Event) service.Event {
	calls, total := e.ModelCalls, int64(0)
	for _, c := range calls {
		total += int64(c.Calls)
	}
	return service.Event{
		At:     time.Now(),
		Stage:  e.Stage,
		Counts: e.Counts,
		// The pointer is the pipeline's own copy of the conflict, taken per
		// event, so passing it on shares nothing that will be written again.
		Conflict:   e.Conflict,
		ModelCalls: total,
		ByStage:    calls,
		Message:    message(e),
	}
}

// message is the one human sentence an event carries. It names the source when
// there is one, because "extract" tells an operator watching a hundred-file
// import nothing about where the job has got to.
func message(e pipeline.Event) string {
	if e.Source == "" {
		return string(e.Kind)
	}
	return string(e.Kind) + " " + e.Source
}
