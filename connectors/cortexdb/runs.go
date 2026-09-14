package cortexdb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	cdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// author is stamped on every document this connector creates, so that "what did
// alchemy put here" is one query on a store that is also somebody's brain. It is
// the only handle a reader has: CortexDB's documents have no namespace, and a
// prefix match on the id would depend on nobody else ever choosing the same one.
const author = "alchemy"

// listLimit bounds the run listing. A run is two documents plus one per source
// file, so this is tens of thousands of imports — and it is a bound rather than
// a page because Incomplete answers an operator's question at a moment, not a
// paging API.
const listLimit = 100000

// Incomplete names the runs that were started and never finished, newest
// first.
//
// It is the reader for the invariant Load maintains: the marker is written
// before the first batch and the completion after the last, so a marker with no
// completion beside it is a load that died halfway. §8.4 makes that state
// reachable — a large result is many writes — and the whole argument for
// tolerating it is that it is *identifiable*. A store that could be half-loaded
// with no way to ask would be the silent loss this design refuses everywhere
// else.
//
// The answer is a run ID because that is what a caller does something with: put
// it back in Options.RunID and run the same load again. Every write is an upsert
// keyed on the run, so finishing a crashed import is re-running it.
func (l *Loader) Incomplete(ctx context.Context) ([]string, error) {
	docs, err := l.cortex.Vector().ListDocumentsWithFilter(ctx, author, listLimit)
	if err != nil {
		return nil, fmt.Errorf("cortexdb: list runs: %w", err)
	}
	started, done := map[string]bool{}, map[string]bool{}
	for _, d := range docs {
		id, ok := strings.CutPrefix(d.ID, runNodeID(""))
		if !ok {
			continue
		}
		if run, complete := strings.CutSuffix(id, ":complete"); complete {
			done[run] = true
			continue
		}
		started[id] = true
	}
	var open []string
	for run := range started {
		if !done[run] {
			open = append(open, run)
		}
	}
	sort.Strings(open)
	return open, nil
}

// deleteRun removes everything one run put in the store, which is what
// Ident.Replace means.
//
// The envelope makes replacement part of the contract — §4.1: "a different one
// under the same name refuses unless told to replace" — and a store that
// refused both would leave a caller who genuinely means to re-import a corpus
// under a stable name with nowhere to go but a new name every night. This
// package had no such path before, which was an omission rather than a
// position: it argued only that replacing *by default* would be wrong.
//
// It goes through CortexDB's own two deletes rather than SQL, and the order is
// load-bearing. DeleteDocumentGraph removes a document's chunk and document
// nodes, its relation edges, and the entities it alone asserted — detaching
// rather than deleting the ones another document also claims, which is the
// behaviour a shared brain needs. DeleteDocument then removes the record and
// cascades to the embeddings, which the graph delete deliberately does not
// touch because they live in the caller's collection.
//
// A run's own two documents go last, so that a crash midway leaves the marker
// standing and Incomplete still names the run: half a deletion that says it is
// half done is the same property the load itself keeps.
func (l *Loader) deleteRun(ctx context.Context, rep *Report) error {
	store := l.cortex.Vector()
	docs, err := store.ListDocumentsWithFilter(ctx, author, listLimit)
	if err != nil {
		return fmt.Errorf("cortexdb: list documents of run %s: %w", l.opts.RunID, err)
	}
	prefix := documentID(l.opts.RunID, "")
	tools := l.cortex.GraphRAGTools()
	for _, d := range docs {
		if !strings.HasPrefix(d.ID, prefix) {
			continue
		}
		rep.Batches++
		if _, err := tools.DeleteDocumentGraph(ctx, cdb.ToolDeleteDocumentGraphRequest{DocumentID: d.ID}); err != nil {
			return fmt.Errorf("cortexdb: delete graph of %s: %w", d.ID, err)
		}
		if err := store.DeleteDocument(ctx, d.ID); err != nil {
			return fmt.Errorf("cortexdb: delete document %s: %w", d.ID, err)
		}
	}
	// The completion goes and the marker stays.
	//
	// This runs from Commit, between the last refusal and the first write, and
	// the marker standing with no completion beside it is exactly what
	// Incomplete() reports: a run whose graph is being written right now looks
	// the same as one that died mid-write, which is the honest reading of both.
	// Removing the marker here would leave the store, for the length of the
	// write, holding a graph that no run admits to.
	id := completionID(l.opts.RunID)
	if doc, err := store.GetDocument(ctx, id); err == nil && doc != nil {
		rep.Batches++
		if err := store.DeleteDocument(ctx, id); err != nil {
			return fmt.Errorf("cortexdb: delete run document %s: %w", id, err)
		}
	}
	return nil
}

// Run is one load this store holds, and enough about it to decide anything.
type Run struct {
	// ID is the run's name — Options.RunID, and what Drop takes.
	ID string `json:"id"`
	// Digest is the graph the run holds. Two runs with one digest hold the
	// same graph; the same run with a second digest is the refusal Load makes
	// unless the caller said replace.
	Digest string `json:"digest"`
	// Started is when the marker was written, Finished when the completion
	// landed beside it. A zero Finished is the half-written run Incomplete
	// reports, and the two fields are separate so a reader sees which.
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	// Counts is what the run wrote, read back off its completion document.
	//
	// It was missing, and the catalogue was the poorer for it in a way that
	// only showed at the other end: a caller asking what this store holds got
	// a list of names and two timestamps, and a caller dropping a load was
	// told it had removed a run with nothing in it. The numbers were on disk
	// the whole time — §5 obliges a graph to carry the numbers needed to
	// distrust it, and completeRun writes them — and nothing read them.
	//
	// Zero for a run that never finished, which is the honest answer: a load
	// that died mid-write has no completion and therefore no count of what it
	// managed to put in. Incomplete() names those.
	Counts alchemy.Counts `json:"counts,omitzero"`
}

// Runs names every load this store holds, newest first.
//
// Incomplete answers a narrower question and stays: "which runs died halfway"
// is an operator's alarm, and this is the catalogue. They read the same two
// documents — the marker written before the first batch and the completion
// written after the last — because there is no third record of a load and
// inventing one would be a second answer to drift from the first.
//
// A store that can be written to and not enumerated is one whose contents are
// known only to whoever wrote them, which on a shared brain is nobody.
func (l *Loader) Runs(ctx context.Context) ([]Run, error) {
	docs, err := l.cortex.Vector().ListDocumentsWithFilter(ctx, author, listLimit)
	if err != nil {
		return nil, fmt.Errorf("cortexdb: list runs: %w", err)
	}
	byID := map[string]*Run{}
	for _, d := range docs {
		id, ok := strings.CutPrefix(d.ID, runNodeID(""))
		if !ok {
			continue
		}
		run, complete := strings.CutSuffix(id, ":complete")
		r, seen := byID[run]
		if !seen {
			r = &Run{ID: run}
			byID[run] = r
		}
		if complete {
			// The completion reuses runMarker and stamps Started with the
			// moment it was written, which is when the run finished. Read as
			// the finish rather than renamed, because the document on disk is
			// what it is and a second name for one field is a second thing to
			// keep in step.
			var fin runMarker
			_ = json.Unmarshal([]byte(d.Content), &fin)
			r.Finished, r.Counts = fin.Started, fin.Counts
			continue
		}
		var m runMarker
		_ = json.Unmarshal([]byte(d.Content), &m)
		r.Digest, r.Started = m.Digest, m.Started
	}
	out := make([]Run, 0, len(byID))
	for _, r := range byID {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Started.Equal(out[j].Started) {
			return out[i].ID < out[j].ID
		}
		return out[i].Started.After(out[j].Started)
	})
	return out, nil
}

// Drop removes everything one run put in the store and reports what it took.
//
// This is the one verb in this package that destroys. Everything else adds,
// upserts or refuses; even a replace only removes in order to write the same
// name back. So it is deliberately separate from Load rather than a flag on
// it: "overwrite this load with that one" and "this load should not be here"
// are different sentences, and the second one has no result to hand over
// afterwards.
//
// What it takes is what deleteRun takes, which is the run's documents, the
// graph they assert, and the embeddings that cascade from them — entities
// another document also names are detached rather than deleted, because a
// shared brain's node does not belong to the last load that mentioned it. The
// marker and the completion go too: a run whose graph is gone must not still
// be in Runs, or Incomplete would start reporting a load nobody can finish.
//
// The report is the caller's record of a thing that cannot be undone. Nothing
// here writes an audit entry — this package holds no ledger — and a product
// that offers this verb owes its own.
func (l *Loader) Drop(ctx context.Context) (Report, error) {
	if strings.TrimSpace(l.opts.RunID) == "" {
		return Report{}, ErrNoRunID
	}
	rep := Report{Run: l.opts.RunID}
	before, err := l.Runs(ctx)
	if err != nil {
		return rep, err
	}
	var held bool
	for _, r := range before {
		if r.ID == l.opts.RunID {
			held, rep.Digest = true, r.Digest
			// What the run wrote, so the answer to "I dropped a load, what
			// went?" is a number rather than a name. Read off the completion
			// rather than counted during the delete, because the delete goes
			// through DeleteDocumentGraph and that detaches a node another
			// document also claims instead of removing it — so this is what
			// the run put in, which is the upper bound on what left, and the
			// two are the same number on a store holding one load.
			rep.Entities, rep.Relations = r.Counts.Entities, r.Counts.Relations
			rep.Chunks = r.Counts.Chunks
		}
	}
	if !held {
		return rep, fmt.Errorf("%w: no run named %q in this store", ErrNoRun, l.opts.RunID)
	}
	if err := l.deleteRun(ctx, &rep); err != nil {
		return rep, err
	}
	// deleteRun keeps the marker, because it runs mid-write when a replace
	// calls it. A drop is not mid-anything and the marker is the last thing
	// saying this run is here.
	store := l.cortex.Vector()
	id := markerID(l.opts.RunID)
	if doc, err := store.GetDocument(ctx, id); err == nil && doc != nil {
		rep.Batches++
		if err := store.DeleteDocument(ctx, id); err != nil {
			return rep, fmt.Errorf("cortexdb: delete run marker %s: %w", id, err)
		}
	}
	return rep, nil
}
