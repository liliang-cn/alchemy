package cortexdb

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
	for _, id := range []string{completionID(l.opts.RunID), markerID(l.opts.RunID)} {
		if doc, err := store.GetDocument(ctx, id); err != nil || doc == nil {
			continue
		}
		rep.Batches++
		if err := store.DeleteDocument(ctx, id); err != nil {
			return fmt.Errorf("cortexdb: delete run document %s: %w", id, err)
		}
	}
	return nil
}
