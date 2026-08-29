package pipeline

import "sync"

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
// What it does not do is wait at the end. Undelivered events are abandoned
// when the job finishes, because the return value carries the same facts and
// better: a caller who stopped reading gets a Result or a *HeldError, not a
// hang.
type emitter struct {
	out chan<- Event

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
func newEmitter(out chan<- Event) *emitter {
	if out == nil {
		return nil
	}
	e := &emitter{
		out:      out,
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
		ev, ok := e.next()
		if !ok {
			select {
			case <-e.wake:
				continue
			case <-e.quit:
				return
			}
		}
		select {
		case e.out <- ev:
		case <-e.quit:
			return
		}
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

// close stops delivery and closes the caller's channel, so that a caller
// ranging over it finishes when the job does.
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
