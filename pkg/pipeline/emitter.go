package pipeline

import (
	"context"
	"sync"
	"time"
)

// emitter carries events to the caller without ever letting the caller's
// reading speed decide how fast the job runs.
//
// The rule it implements is a decision, and it is this: **the pipeline never
// blocks on the events channel, and a conflict is never dropped.** Both halves
// are needed, and they pull against each other.
//
// Blocking was rejected first. A caller who logs each event, or who is a
// browser on the far end of an SSE bridge, would otherwise be able to stall an
// import; and "the job stopped because nobody was watching" is a failure whose
// cause is invisible from inside the job.
//
// Dropping everything was rejected second, and this is the interesting half.
// §7.3 promises an operator learns about a conflict "in minute three, not at
// minute one hundred and twenty" — a promise a dropped conflict event breaks
// silently, since the alternative source of that news is the result, two hours
// later. So the queue holds every stage change and every conflict, and it is
// only progress that is allowed to be lost: a progress event carries
// cumulative counts, so an unsent one is superseded rather than missed by the
// next reading. The queue therefore holds at most one progress event at a time
// and grows only with things that are genuinely news.
//
// At the end the queue is drained rather than abandoned, and that is the half
// this design got wrong first. Abandoning it made "a conflict is never
// dropped" true only while the job was running: the last stage changes and any
// conflict still queued were lost on a coin flip between delivering and
// quitting, which showed up as a test that passed fourteen times in fifteen.
// A promise that holds until the moment the job finishes is not a promise.
//
// The drain is bounded, and the bound is the whole design here. Draining
// without one trades a lost event for a hung job — a caller who walked away
// from the channel would hold the job open forever, which on a server is a
// worse bug than the one being fixed. So the job waits for a reader, but not
// indefinitely: a reader that is there takes everything in microseconds, and a
// reader that is gone costs drainGrace once, at the end, and nothing else.
//
// Neither half of this is free and it is worth being plain about which way
// each error goes. Too short a grace and a real consumer misses the last
// stage change. Too long and a dead consumer delays a finished job. The
// asymmetry decides it: the first failure is silent and the second is a
// measurable pause, so the grace is set well above the cost of a channel
// handoff and well under anything an operator would notice in a job that
// takes minutes.
type emitter struct {
	out chan<- Event
	// ctx is the only thing that can cut delivery short. It is what keeps the
	// contract above from being a way to hang: a caller who stops reading
	// stops the job by cancelling, not by walking away.
	ctx context.Context

	mu    sync.Mutex
	queue []Event
	// progress is the position in queue of the unsent progress reading, or -1.
	// It is tracked rather than searched so that a job with ten thousand
	// conflicts queued does not scan them on every count update.
	progress int

	wake chan struct{}
	quit chan struct{}
	done chan struct{}
}

// newEmitter starts the delivery goroutine, or returns nil for a caller that
// asked for no events. A nil *emitter is a working emitter that sends nothing,
// which is what keeps "events may be nil" from being a branch at every call
// site.
func newEmitter(ctx context.Context, out chan<- Event) *emitter {
	if out == nil {
		return nil
	}
	e := &emitter{
		out:      out,
		ctx:      ctx,
		progress: -1,
		wake:     make(chan struct{}, 1),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go e.loop()
	return e
}

// send queues an event. It never blocks for longer than the lock.
func (e *emitter) send(ev Event) {
	if e == nil {
		return
	}
	e.mu.Lock()
	if ev.Kind == EventProgress && e.progress >= 0 {
		// The newest reading replaces the unsent one, in its place. Counts are
		// cumulative, so nothing a reader could act on is lost — the number
		// they get is the number that is true.
		e.queue[e.progress] = ev
	} else {
		if ev.Kind == EventProgress {
			e.progress = len(e.queue)
		}
		e.queue = append(e.queue, ev)
	}
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default: // already awake; the loop will see the queue.
	}
}

func (e *emitter) loop() {
	defer close(e.done)
	for {
		if ev, ok := e.next(); ok {
			if !e.deliver(ev) {
				return
			}
			continue
		}
		select {
		case <-e.wake:
		case <-e.ctx.Done():
			return
		case <-e.quit:
			// The job has finished. What is still queued is news the caller
			// has not heard yet — a stage change, or a conflict §7.3 promised
			// them — so it is delivered before the channel closes rather than
			// dropped for being late.
			e.drain()
			return
		}
	}
}

// drainGrace is how long a finished job waits for a reader that is not there.
// See the type comment for why it is bounded and why this size.
const drainGrace = 2 * time.Second

// drain delivers what is left to a reader that is still listening, and gives
// up on one that is not.
func (e *emitter) drain() {
	timer := time.NewTimer(drainGrace)
	defer timer.Stop()
	for {
		ev, ok := e.next()
		if !ok {
			return
		}
		select {
		case e.out <- ev:
		case <-e.ctx.Done():
			return
		case <-timer.C:
			// Nobody is reading. The job is over and its facts are in the
			// return value; holding it open for an absent reader is how a
			// server acquires a goroutine leak per abandoned watcher.
			return
		}
	}
}

// deliver blocks until the caller takes the event or gives up on the job.
// While the job runs there is no grace: a reader who is merely slow should
// slow nothing, and the queue is what absorbs that.
func (e *emitter) deliver(ev Event) bool {
	select {
	case e.out <- ev:
		return true
	case <-e.ctx.Done():
		return false
	case <-e.quit:
		// The job finished while this event was waiting for a reader. Put it
		// back so the bounded drain gets its turn at it rather than losing it
		// to whichever branch the runtime picked.
		e.unshift(ev)
		e.drain()
		return false
	}
}

// unshift returns an undelivered event to the front of the queue.
func (e *emitter) unshift(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.queue = append([]Event{ev}, e.queue...)
	if e.progress >= 0 {
		e.progress++
	}
	if ev.Kind == EventProgress {
		e.progress = 0
	}
}

func (e *emitter) next() (Event, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.queue) == 0 {
		return Event{}, false
	}
	ev := e.queue[0]
	e.queue = e.queue[1:]
	if e.progress == 0 {
		e.progress = -1
	} else if e.progress > 0 {
		e.progress--
	}
	return ev, true
}

// close delivers whatever is still queued and then closes the caller's
// channel, so that a caller ranging over it finishes when the job does and
// finishes having heard everything.
//
// It closes the channel it was given, which means Run owns that channel for
// the duration of the call: give each Run its own. The alternative — leaving
// it open — turns the ordinary way to consume a stream, a range loop, into a
// hang after a job that has already returned.
func (e *emitter) close() {
	if e == nil {
		return
	}
	close(e.quit)
	<-e.done
	close(e.out)
}
