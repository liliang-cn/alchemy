package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The JSON the page draws, and the three decisions in it that are not the
// page's to make.
//
// It is deliberately not a new projection of the graph. Every record in here
// is the message pkg/service sent, marshalled with the same protojson settings
// every other route uses, so a field the page shows is a field a buyer sees by
// curling /v1/jobs/{id}/result. What this file adds is only what a browser
// forces somebody to decide: how much is too much to draw, what a held job
// shows, and how either of those is said out loud rather than silently.

// ViewMaxNodes and ViewMaxEdges are the budget.
//
// §8.4 pages a large result because it does not fit one message; a browser has
// the same problem one layer up and no equivalent of a page. Two hundred
// thousand nodes is not a view, it is a tab that stops responding, and the
// failure mode is the worst one available — the person concludes the service
// is broken rather than that the graph is big.
//
// The numbers are what a force layout run in JavaScript on a canvas settles in
// about a second, which is the honest limit rather than a round one. They are
// exported because the tests must exceed them to prove anything and because
// the page reports them to the person reading a sampled graph: a limit nobody
// can see is a limit nobody can argue with.
const (
	ViewMaxNodes = 1200
	ViewMaxEdges = 3000
	// ViewMaxFindings is per kind. Findings are what §5b says the graph is for,
	// so they get their own budget rather than sharing the nodes' — a graph
	// sampled down to 1,200 nodes must still be able to show all four hundred
	// of its violations.
	ViewMaxFindings = 2000
)

// heldDrain bounds the read of a held job's progress stream.
//
// A held job keeps its hub open — pkg/service says so in as many words,
// because "the reviewer it is waiting for has not connected yet" — so WatchJob
// on one replays what is known and then waits forever for an event that will
// not come until somebody decides something. A browser cannot wait forever, so
// the read is bounded and the bound is short: everything a held job has to say
// is already buffered when the stream opens.
const heldDrain = 3 * time.Second

// viewGraph is the document the page loads.
//
// Result is a raw message rather than a typed one so that the page reads the
// service's own field names — provenance.producer, counts.chunks_empty — and
// not a second vocabulary invented here. Held, Because, Truncated and Shown
// are this file's four additions, and each is a fact about the *view* rather
// than about the graph: what the service refused, and what the browser could
// not draw.
type viewGraph struct {
	Job json.RawMessage `json:"job"`
	// Held is §7.3 at the top level, where nothing can miss it. A held graph
	// must be impossible to mistake for an accepted one, and the page keys its
	// whole appearance off this one boolean.
	Held bool `json:"held"`
	// Because is the service's own sentence, never a paraphrase. When a job is
	// held it is pkg/service explaining that a graph which contradicts itself
	// is not a finished graph; a view that rewrote it would be inventing a
	// second explanation for a decision it did not make.
	Because string `json:"because,omitempty"`
	// Truncated says a budget was hit. It is a separate field from the numbers
	// below because a page can forget to compare two numbers and cannot forget
	// to check a flag.
	Truncated bool      `json:"truncated"`
	Shown     viewShown `json:"shown"`
	// Result carries the whole graph's counts and a sample of its records. The
	// asymmetry is the point: §5's numbers describe the graph, and a viewer
	// that reported the sample's numbers instead would be the failure that
	// looks like a success with a smaller denominator.
	Result json.RawMessage `json:"result"`
}

// viewShown is how much of each kind actually reached the page.
type viewShown struct {
	Entities   int `json:"entities"`
	Relations  int `json:"relations"`
	Conflicts  int `json:"conflicts"`
	Violations int `json:"violations"`
	Duplicates int `json:"duplicates"`
	Guesses    int `json:"guesses"`
}

// graph answers the page's one data request.
func (v *viewer) graph(w http.ResponseWriter, r *http.Request) {
	ctx, ok := v.authorize(r)
	if !ok {
		v.refuse(w, r)
		return
	}
	id := r.PathValue("job_id")
	job, err := v.client.GetJob(ctx, &alchemyv1.GetJobRequest{JobId: id})
	if err != nil {
		http.Error(w, status.Convert(err).Message(), viewStatus(err))
		return
	}

	out, err := v.collect(ctx, id, job)
	if err != nil {
		http.Error(w, status.Convert(err).Message(), viewStatus(err))
		return
	}
	out.Job = marshalProto(job)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		// The response is already begun, so there is nothing honest left to
		// send. It is not silent: the connection ends mid-document and the
		// page's own parse failure is what the person sees.
		return
	}
}

// collect reads the graph, or the reason there is not one.
//
// The order matters. StreamResult is asked first even for a job that is
// obviously held, because deciding from the job state here would be this
// package holding an opinion about when a result exists — pkg/service's
// finished() is the one place that decides, and asking it is how the view
// stays a client rather than a second implementation. The held path is
// entered only when the service refuses *and* says the job is held.
func (v *viewer) collect(ctx context.Context, id string, job *alchemyv1.Job) (*viewGraph, error) {
	out, err := v.drawable(ctx, id)
	if err == nil {
		return out, nil
	}
	if status.Code(err) != codes.FailedPrecondition || job.GetState() != alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW {
		return nil, err
	}
	// §7.3: a held job is exactly when somebody needs to look, and GetResult
	// refuses it on purpose. The refusal is not weakened and not gone round —
	// what is shown instead is what the service is willing to say about a held
	// job over an RPC, which is WatchJob's counts and every conflict it found.
	return v.heldGraph(ctx, id, status.Convert(err).Message())
}

// drawable reads StreamResult until the budget is spent, then stops.
//
// It stops by cancelling the stream rather than by reading to the end and
// throwing pages away, so a 400,000-record graph costs the service the pages
// the browser will actually use and no more. §8.4's argument for paging is
// that a big result is not one message; this is the reader that argument was
// written for.
func (v *viewer) drawable(ctx context.Context, id string) (*viewGraph, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := v.client.StreamResult(ctx, &alchemyv1.GetResultRequest{JobId: id})
	if err != nil {
		return nil, err
	}
	out := &viewGraph{Result: nil}
	res := &alchemyv1.Result{}
	full := false

	for {
		page, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if page.GetPage() == 0 {
			// The summary rides on the first page, which is pkg/service's
			// decision and the reason this works at all: the numbers needed to
			// distrust the graph arrive before the graph, so a sampled read
			// still reports the whole thing truthfully.
			res.Counts = page.GetCounts()
			res.ModelCalls = page.GetModelCalls()
			res.Unread = page.GetUnread()
			res.Rules = page.GetRules()
			res.RuleSets = page.GetRuleSets()
		}
		res.Entities, full = take(res.Entities, page.GetEntities(), ViewMaxNodes, &out.Truncated)
		res.Relations, _ = take(res.Relations, page.GetRelations(), ViewMaxEdges, &out.Truncated)
		res.Conflicts, _ = take(res.Conflicts, page.GetConflicts(), ViewMaxFindings, &out.Truncated)
		res.Violations, _ = take(res.Violations, page.GetViolations(), ViewMaxFindings, &out.Truncated)
		res.Duplicates, _ = take(res.Duplicates, page.GetDuplicates(), ViewMaxFindings, &out.Truncated)
		res.Guesses, _ = take(res.Guesses, page.GetGuesses(), ViewMaxFindings, &out.Truncated)
		if page.GetLast() {
			break
		}
		if full && len(res.Relations) >= ViewMaxEdges {
			// Both budgets are spent, so every remaining page is records this
			// view will not draw. Stopping here is what keeps a graph too
			// large to draw from also being a graph too large to fetch.
			out.Truncated = true
			break
		}
	}

	res.Relations = connected(res.Entities, res.Relations)
	out.Shown = shownOf(res)
	out.Result = marshalProto(res)
	return out, nil
}

// heldGraph is what a held job shows.
//
// Counts and conflicts, and deliberately nothing else. Not because more would
// be uninteresting — the violations, guesses and duplicates of a held job are
// computed and would be worth seeing — but because the only RPC that carries
// them for a job in this state is Review, which is a bidirectional stream with
// no honest HTTP translation (review.go) and which attaching to is not a
// read-only act. What is here is everything WatchJob will say, which is
// everything this view can honestly claim to know.
func (v *viewer) heldGraph(ctx context.Context, id, because string) (*viewGraph, error) {
	ctx, cancel := context.WithTimeout(ctx, heldDrain)
	defer cancel()

	stream, err := v.client.WatchJob(ctx, &alchemyv1.WatchJobRequest{JobId: id})
	if err != nil {
		return nil, err
	}
	res := &alchemyv1.Result{}
	for {
		event, err := stream.Recv()
		if err != nil {
			// Any end is an end here, including the deadline: a held job's hub
			// stays open for the reviewer who has not arrived yet, so the
			// stream does not finish on its own and running out of time is the
			// normal way this loop ends rather than a failure to report.
			break
		}
		if event.GetCounts() != nil {
			res.Counts = event.GetCounts()
		}
		if c := event.GetConflict(); c != nil {
			res.Conflicts = append(res.Conflicts, c)
		}
		if len(res.Conflicts) >= int(res.GetCounts().GetConflicts()) && res.GetCounts().GetConflicts() > 0 {
			// Everything the hub replays on attaching is buffered before the
			// first Recv, so having as many conflicts as the counts declare
			// means the replay is done and the rest of the wait would be spent
			// on an event that only a reviewer's decision can produce.
			break
		}
	}
	shown := shownOf(res)
	return &viewGraph{
		Held: true, Because: because,
		// Truncated means the same thing here as it does for a graph too large
		// to draw: what is on the screen is less than what the job has. A held
		// job whose hub was pruned, or whose second conflict was found after
		// the last event a watcher could still receive, must not look like a
		// job with one conflict in it — a person who resolves what they can
		// see and finds the job still held has been told nothing by the view
		// that would explain it.
		Truncated: shown.Conflicts < int(res.GetCounts().GetConflicts()),
		Shown:     shown,
		Result:    marshalProto(res),
	}, nil
}

// take appends up to a budget and reports that it clipped.
//
// The sample is the head of the service's own order rather than a random
// subset, which makes it reproducible — two loads of the same held graph show
// the same records — and puts the findings first, since pkg/service's pager
// pages conflicts, violations and guesses before entities precisely so that a
// client which stops reading early stops with the warnings in hand.
func take[T any](into, from []T, budget int, truncated *bool) ([]T, bool) {
	room := budget - len(into)
	if room <= 0 {
		*truncated = *truncated || len(from) > 0
		return into, true
	}
	if len(from) > room {
		*truncated = true
		from = from[:room]
	}
	into = append(into, from...)
	return into, len(into) >= budget
}

// connected drops edges whose ends were not drawn.
//
// A sampled graph is a subgraph, and an edge to a node that is not on the
// screen is a line into empty space — the renderer would have to invent a
// position for a node nobody chose to show. The count of what survived is in
// Shown, so the loss is reported rather than absorbed.
func connected(entities []*alchemyv1.Entity, relations []*alchemyv1.Relation) []*alchemyv1.Relation {
	if len(relations) == 0 {
		return relations
	}
	drawn := make(map[string]struct{}, len(entities))
	for _, e := range entities {
		drawn[e.GetId()] = struct{}{}
	}
	out := relations[:0]
	for _, rel := range relations {
		_, from := drawn[rel.GetFrom()]
		_, to := drawn[rel.GetTo()]
		if from && to {
			out = append(out, rel)
		}
	}
	return out
}

func shownOf(res *alchemyv1.Result) viewShown {
	return viewShown{
		Entities:   len(res.GetEntities()),
		Relations:  len(res.GetRelations()),
		Conflicts:  len(res.GetConflicts()),
		Violations: len(res.GetViolations()),
		Duplicates: len(res.GetDuplicates()),
		Guesses:    len(res.GetGuesses()),
	}
}

// marshalProto renders a message the way every other route in this package
// renders one, so the page and a curl user read the same field names.
//
// A marshalling failure becomes a JSON null rather than an error, and that is
// a considered trade rather than laziness: the alternative is failing the
// whole request over one unrenderable record, and a page that shows a graph
// with a hole in it and a null where the job should be is more useful to the
// person debugging it than a 500 with no graph at all.
func marshalProto(m any) json.RawMessage {
	raw, err := jsonMarshaler().Marshal(m)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}
