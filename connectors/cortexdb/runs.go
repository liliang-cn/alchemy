package cortexdb

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
