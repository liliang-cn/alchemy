// Package neo4j loads an alchemy.Result into a Neo4j graph.
//
// It is a tool a buyer runs, not a write path the service grew. DESIGN.md §4
// decided that alchemy returns and does not store, and nothing here is
// reachable from pkg/service, pkg/pipeline or cmd/alchemy — the dependency
// runs one way, from this module to the core, and the core's `require` block
// is the checkable form of that argument.
//
// What it puts in the graph, and why, in one place:
//
//   - An Entity becomes a node labelled with the base label and its ontology
//     type. A Relation becomes a relationship of its declared type.
//   - Provenance is flattened onto both, under a reserved property prefix, so
//     that §5b's guarantee — every entity and relation can name its source,
//     chunk and producer — survives the trip in the same shape for both.
//   - Nothing is keyed on Entity.ID alone. See Options.RunID.
//   - A result carrying an unanswered conflict is refused (§7.3).
//   - The findings and the counts are loaded too, beside the graph rather than
//     inside it (findings.go).
package neo4j

import (
	"context"
	"fmt"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Loader writes results into one Neo4j database under one set of options.
type Loader struct {
	driver driver.DriverWithContext
	opts   Options
	// owned says whether Close should shut the driver down. A caller who
	// handed us a driver they also use elsewhere would not thank us for
	// closing it.
	owned bool
}

// Open dials a Neo4j server and returns a Loader. It verifies connectivity
// before returning, because a driver constructor that lazily fails means the
// first error a caller sees is attributed to their data.
func Open(ctx context.Context, uri, user, password string, o Options) (*Loader, error) {
	d, err := driver.NewDriverWithContext(uri, driver.BasicAuth(user, password, ""))
	if err != nil {
		return nil, fmt.Errorf("neo4j: %w", err)
	}
	if err := d.VerifyConnectivity(ctx); err != nil {
		_ = d.Close(ctx)
		return nil, fmt.Errorf("neo4j: cannot reach %s: %w", uri, err)
	}
	l := New(d, o)
	l.owned = true
	return l, nil
}

// New wraps a driver the caller already has.
func New(d driver.DriverWithContext, o Options) *Loader {
	return &Loader{driver: d, opts: o.withDefaults()}
}

func (l *Loader) Close(ctx context.Context) error {
	if !l.owned {
		return nil
	}
	return l.driver.Close(ctx)
}

// Report says what a Load did. It is returned rather than logged because
// everything in it is a fact about the graph the caller now has, and a fact a
// caller cannot see is one they cannot act on.
type Report struct {
	Run    string
	Digest string

	Entities   int
	Relations  int
	Chunks     int
	Violations int
	Duplicates int
	Guesses    int
	Unread     int

	// SkippedRelations names the edges that were not written because an
	// endpoint was not in the result. See preflight.
	SkippedRelations []string
	// SkippedVectors is how many embeddings were left behind. This connector
	// loads a graph; a vector belongs in the store bought for vectors, and
	// dropping them without saying so would be the silent loss the rest of
	// this design refuses.
	SkippedVectors int

	// Batches is how many transactions it took, which is the number an
	// operator needs when a load dies halfway.
	Batches int
	// Replay is true when the run was already present with the same digest,
	// so this Load converged on a graph that was already there.
	Replay bool
}

// Load writes a whole result into the graph.
//
// The sequence is deliberate and the reason is §8.4: a large result does not
// fit in one transaction, so a load is many transactions, so a load can fail
// with part of the graph written. A half-loaded graph is survivable; a
// half-loaded graph nobody can tell is half-loaded is not. So:
//
//  1. Everything checkable without a database is checked first (preflight),
//     and the load is refused before a single write if anything is wrong.
//  2. A run marker is written in its own transaction, saying `_complete:
//     false`. From this instant until the last batch lands, the graph
//     truthfully says it is mid-import.
//  3. The batches run.
//  4. The marker is completed, with the digest and the counts.
//
// A crash at any point leaves a run whose `_complete` is false, which is one
// query to find and — because every write is a MERGE keyed on identity — one
// re-Load to finish. Convergence is the reason the writes are MERGEs rather
// than CREATEs even though CREATE is faster: a retry has to be a retry.
func (l *Loader) Load(ctx context.Context, res alchemy.Result) (Report, error) {
	p, err := preflight(res, l.opts)
	if err != nil {
		return Report{}, err
	}
	rep := Report{
		Run: l.opts.RunID, Digest: p.digest,
		SkippedRelations: p.skipped, SkippedVectors: len(res.Vectors),
	}

	if err := l.ensureIndex(ctx); err != nil {
		return rep, err
	}
	replay, err := l.claimRun(ctx, p)
	if err != nil {
		return rep, err
	}
	rep.Replay = replay

	if err := l.writeEntities(ctx, p, &rep); err != nil {
		return rep, err
	}
	if err := l.writeRelations(ctx, p, &rep); err != nil {
		return rep, err
	}
	if err := l.writeChunks(ctx, p, &rep); err != nil {
		return rep, err
	}
	if err := l.writeFindings(ctx, p, &rep); err != nil {
		return rep, err
	}
	if err := l.completeRun(ctx, p, &rep); err != nil {
		return rep, err
	}
	return rep, nil
}

// internal labels are derived from the base label rather than fixed, so that
// the base label is a real escape hatch: an ontology with a type called
// "AlchemyRun" is a collision the buyer can move out of by renaming their
// namespace, instead of being told their vocabulary is wrong.
func (l *Loader) internalLabel(kind string) string { return l.opts.BaseLabel + kind }

func (o Options) internalLabels() []string {
	return []string{o.BaseLabel + "Run", o.BaseLabel + "Chunk", o.BaseLabel + "Violation",
		o.BaseLabel + "Duplicate", o.BaseLabel + "Guess", o.BaseLabel + "Unread"}
}

// indexNames is the set of indexes this Loader creates. It is a method so that
// a caller undoing an import has the same list the loader used.
func (l *Loader) indexNames() []string {
	n, _ := quoteIdent("alchemy_" + l.opts.BaseLabel + "_run_id")
	return []string{n}
}

// ensureIndex creates the index every MERGE in this package looks nodes up by.
// Without it a load is a sequence of full scans and the first real import is
// the one that never finishes.
func (l *Loader) ensureIndex(ctx context.Context) error {
	base, err := quoteIdent(l.opts.BaseLabel)
	if err != nil {
		return fmt.Errorf("neo4j: base label: %w", err)
	}
	p := l.opts.ReservedPrefix
	run, _ := quoteIdent(p + keyRun)
	id, _ := quoteIdent(p + keyID)
	stmt := fmt.Sprintf("CREATE INDEX %s IF NOT EXISTS FOR (n:%s) ON (n.%s, n.%s)",
		l.indexNames()[0], base, run, id)
	return l.write(ctx, func(tx driver.ManagedTransaction) error {
		_, err := tx.Run(ctx, stmt, nil)
		return err
	})
}

// claimRun writes the marker, and is where "what does loading the same result
// twice do?" is answered.
//
//   - Same run ID, same digest: a replay. The MERGEs converge on the graph
//     that is already there, which is what makes a crashed load finishable by
//     running the same command again.
//   - Same run ID, different digest: refused. The caller is telling the store
//     two different things about one import, and there is nothing in the data
//     to decide which is current. Options.Overwrite is the way to say so on
//     purpose.
//   - A different run ID: a different graph. Nothing is merged across runs,
//     ever, because Entity.ID says nothing across runs and joining on it would
//     be entity resolution done wrong (§5 defers it to a second release).
func (l *Loader) claimRun(ctx context.Context, p *plan) (bool, error) {
	base, _ := quoteIdent(l.opts.BaseLabel)
	runLabel, err := quoteIdent(l.internalLabel("Run"))
	if err != nil {
		return false, err
	}
	pre := l.opts.ReservedPrefix
	recs, err := l.read(ctx, fmt.Sprintf("MATCH (r:%s:%s {`%s%s`: $run}) RETURN r.`%s%s` AS digest, r.`%scomplete` AS complete",
		base, runLabel, pre, keyID, pre, "digest", pre), map[string]any{"run": l.opts.RunID})
	if err != nil {
		return false, err
	}
	replay := false
	if len(recs) > 0 {
		prev, _ := recs[0]["digest"].(string)
		switch {
		case prev == p.digest:
			replay = true
		case l.opts.Overwrite:
			if err := l.deleteRun(ctx); err != nil {
				return false, err
			}
		default:
			return false, fmt.Errorf("%w: run %q holds a graph with digest %s, this result is %s; "+
				"use a new RunID, or Options.Overwrite to replace it", ErrRunExists, l.opts.RunID, short(prev), short(p.digest))
		}
	}

	// Written incomplete and only later completed, so that the window in which
	// the graph is partial is a window in which the graph says so.
	stmt := fmt.Sprintf("MERGE (r:%s:%s {`%s%s`: $run}) SET r.`%s%s` = $run, r.`%scomplete` = false, r.`%sstarted_at` = $now, r.`%sdigest` = $digest",
		base, runLabel, pre, keyID, pre, keyRun, pre, pre, pre)
	return replay, l.write(ctx, func(tx driver.ManagedTransaction) error {
		_, err := tx.Run(ctx, stmt, map[string]any{"run": l.opts.RunID, "now": time.Now().UTC(), "digest": p.digest})
		return err
	})
}

// completeRun flips the marker and writes the numbers §5 obliges a graph to
// carry: "every returned graph is accompanied by the numbers needed to
// distrust it". They are on the run node rather than left in the JSON, because
// a graph in Neo4j whose quality numbers are in a file on somebody's laptop is
// a graph you merely have.
func (l *Loader) completeRun(ctx context.Context, p *plan, rep *Report) error {
	base, _ := quoteIdent(l.opts.BaseLabel)
	runLabel, _ := quoteIdent(l.internalLabel("Run"))
	pre := l.opts.ReservedPrefix
	props := map[string]any{
		pre + "complete":    true,
		pre + "finished_at": time.Now().UTC(),
		pre + "digest":      p.digest,
		// The loader's own numbers, beside the pipeline's: they differ when a
		// relation was skipped, and the difference is the thing a buyer would
		// otherwise have to find by counting.
		pre + "loaded_entities":  int64(rep.Entities),
		pre + "loaded_relations": int64(rep.Relations),
		pre + "skipped_relations": func() []any {
			out := make([]any, 0, len(rep.SkippedRelations))
			for _, s := range rep.SkippedRelations {
				out = append(out, s)
			}
			return out
		}(),
		pre + "skipped_vectors": int64(rep.SkippedVectors),
	}
	for k, v := range countProps(p.res.Counts, pre) {
		props[k] = v
	}
	stmt := fmt.Sprintf("MATCH (r:%s:%s {`%s%s`: $run}) SET r += $props", base, runLabel, pre, keyID)
	rep.Batches++
	return l.write(ctx, func(tx driver.ManagedTransaction) error {
		_, err := tx.Run(ctx, stmt, map[string]any{"run": l.opts.RunID, "props": props})
		return err
	})
}

// countProps flattens Counts. Every field is written, including the zeros: a
// missing property reads as "this loader did not know about that number",
// which is a different claim from "that number was nought".
func countProps(c alchemy.Counts, pre string) map[string]any {
	return map[string]any{
		pre + "count_entities": int64(c.Entities), pre + "count_relations": int64(c.Relations),
		pre + "count_deterministic": int64(c.Deterministic), pre + "count_inferred": int64(c.Inferred),
		pre + "count_violations": int64(c.Violations), pre + "count_conflicts": int64(c.Conflicts),
		pre + "count_guesses": int64(c.Guesses), pre + "count_duplicates": int64(c.Duplicates),
		pre + "count_chunks_empty": int64(c.ChunksEmpty), pre + "count_chunks_unread": int64(c.ChunksUnread),
		pre + "count_dropped": int64(c.Dropped),
	}
}

// deleteRun removes everything one run wrote, in bites. A single DETACH DELETE
// over a four-hundred-thousand-node run is a transaction the server has to
// hold in memory, which is the failure mode §8.4 names one level up.
func (l *Loader) deleteRun(ctx context.Context) error {
	base, _ := quoteIdent(l.opts.BaseLabel)
	pre := l.opts.ReservedPrefix
	stmt := fmt.Sprintf("MATCH (n:%s {`%s%s`: $run}) WITH n LIMIT $limit DETACH DELETE n RETURN count(n) AS n", base, pre, keyRun)
	for {
		var deleted int64
		err := l.write(ctx, func(tx driver.ManagedTransaction) error {
			res, err := tx.Run(ctx, stmt, map[string]any{"run": l.opts.RunID, "limit": int64(l.opts.BatchSize)})
			if err != nil {
				return err
			}
			rec, err := res.Single(ctx)
			if err != nil {
				return err
			}
			deleted, _ = rec.Values[0].(int64)
			return nil
		})
		if err != nil {
			return err
		}
		if deleted == 0 {
			return nil
		}
	}
}

// write and read are the only two places a session is opened. ExecuteWrite is
// the driver's managed transaction: it retries the transient failures (leader
// switches, deadlocks) that a batch loader would otherwise turn into a
// half-loaded graph for no reason.
func (l *Loader) write(ctx context.Context, fn func(driver.ManagedTransaction) error) error {
	s := l.driver.NewSession(ctx, driver.SessionConfig{DatabaseName: l.opts.Database, AccessMode: driver.AccessModeWrite})
	defer s.Close(ctx)
	_, err := s.ExecuteWrite(ctx, func(tx driver.ManagedTransaction) (any, error) {
		return nil, fn(tx)
	})
	return err
}

func (l *Loader) read(ctx context.Context, cypher string, params map[string]any) ([]map[string]any, error) {
	s := l.driver.NewSession(ctx, driver.SessionConfig{DatabaseName: l.opts.Database, AccessMode: driver.AccessModeRead})
	defer s.Close(ctx)
	out, err := s.ExecuteRead(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, params)
		if err != nil {
			return nil, err
		}
		recs, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		maps := make([]map[string]any, 0, len(recs))
		for _, r := range recs {
			maps = append(maps, r.AsMap())
		}
		return maps, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]map[string]any), nil
}

func short(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	if digest == "" {
		return "(none)"
	}
	return digest
}
