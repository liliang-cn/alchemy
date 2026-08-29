package pipeline

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// EventKind says what an event is telling the caller.
type EventKind string

const (
	// EventStage — the job entered a stage.
	EventStage EventKind = "stage"
	// EventProgress — the running counts changed.
	EventProgress EventKind = "progress"
	// EventConflict — a conflict was found. §7.3: "an operator watching a
	// two-hour import should learn in minute three that it will need them, not
	// at minute one hundred and twenty."
	EventConflict EventKind = "conflict"
)

// Event is one thing the caller is told while the job runs.
type Event struct {
	Kind EventKind
	// Stage is the pipeline stage the event came from.
	Stage string
	// Source is the file being worked on, when the event is about one.
	Source string
	// Counts is the job's running total. It is cumulative rather than a delta,
	// which is what lets a superseded progress event be dropped without losing
	// anything: the newest reading is the whole story. See emitter.
	Counts alchemy.Counts
	// ModelCalls is what the job has spent so far, aggregated the same way the
	// result's is. §7.2: a running count is what lets "a job whose bill is
	// growing faster than expected be cancelled while it runs rather than
	// after it finishes".
	ModelCalls []alchemy.ModelCall
	// Conflict is set when Kind is EventConflict.
	Conflict *alchemy.Conflict
}

// emit sends one event, filling in the running numbers every event carries.
// It never blocks the job; see emitter.
func (r *run) emit(ev Event) {
	if r.events == nil {
		return
	}
	ev.Counts = r.running()
	ev.ModelCalls = aggregate(r.modelCalls)
	r.events.send(ev)
}

// progress reports the running counts under the stage that changed them.
func (r *run) progress(stage, source string) {
	r.emit(Event{Kind: EventProgress, Stage: stage, Source: source})
}

// found records conflicts and tells the caller about each one as it is found.
//
// The telling is here rather than at the end of the stage that found them
// because §7.3 is specific about when: "an operator watching a two-hour import
// should learn in minute three that it will need them, not at minute one
// hundred and twenty". Every conflict in this job passes through this
// function, so there is one place that promise can be broken and it is this
// one.
func (r *run) found(conflicts ...alchemy.Conflict) {
	for _, c := range conflicts {
		r.conflicts = append(r.conflicts, c)
		found := c
		r.emit(Event{Kind: EventConflict, Stage: r.stage, Source: c.Right.Provenance.Source, Conflict: &found})
	}
}

// running is the job's counts as they stand. It is the same function that
// computes the final block, over the same fields, so a caller watching the
// numbers climb and a caller reading them at the end are reading one thing.
func (r *run) running() alchemy.Counts {
	return r.counts(alchemy.Result{
		Entities:   r.entities,
		Relations:  r.relations,
		Violations: r.violations,
		Conflicts:  r.conflicts,
		Guesses:    r.guesses,
		Unread:     r.unread,
	})
}

// aggregate collapses the calls every stage reported into one line per model
// and stage. §7.2: the job reports how many model calls it made, by model and
// stage — one line per pair, or the report is a log rather than a total.
func aggregate(calls []alchemy.ModelCall) []alchemy.ModelCall {
	if len(calls) == 0 {
		return nil
	}
	type key struct{ model, stage string }
	order := make([]key, 0, len(calls))
	sums := make(map[key]alchemy.ModelCall, len(calls))
	for _, c := range calls {
		k := key{c.Model, c.Stage}
		got, seen := sums[k]
		if !seen {
			order = append(order, k)
			got = alchemy.ModelCall{Model: c.Model, Stage: c.Stage}
		}
		got.Calls += c.Calls
		got.Tokens += c.Tokens
		sums[k] = got
	}
	out := make([]alchemy.ModelCall, 0, len(order))
	for _, k := range order {
		out = append(out, sums[k])
	}
	return out
}
