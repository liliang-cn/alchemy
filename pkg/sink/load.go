package sink

import (
	"context"
	"errors"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/preflight"
)

// ErrExists is a store refusing a name that is taken by a different graph.
//
// It is a sentinel here rather than in each connector because it is the one
// answer the envelope specifies: two different things under one name is a
// question, nothing in the data answers it, and Ident.Replace is how a caller
// says which they meant. What the store does to check is its own — a marker
// document, a row with a state column, a property on a run node — and none of
// that reaches here.
var ErrExists = errors.New("sink: the name holds a different graph")

// DefaultBatch is how many records travel in one call when the caller says
// nothing.
//
// It is a round number and it is meant to be overridden. The right value is a
// property of the store's round trip and its transaction size, which is exactly
// the kind of thing §4.1 leaves to the store — a connector that knows better
// passes its own.
const DefaultBatch = 1000

// Options is what a caller may say about one load. It is deliberately three
// fields: everything else a store is configured with belongs to the store.
type Options struct {
	// Load overrides the name. Empty takes alchemy.Result.Job, and a result
	// that names no job is named after its own content — a load with no name
	// could not be found again.
	Load string
	// Replace overwrites a load of the same name that holds a different graph.
	Replace bool
	// Batch is how many records travel in one call. Zero is DefaultBatch.
	Batch int
}

// Load drives one whole result into a store.
//
// It is the adapter for a caller that already holds an alchemy.Result, which is
// every caller today, and it is deliberately not a second way in: what it does
// is what a reader of §8.4's pages would do, in the order those pages already
// arrive. A future caller that never materialises the result drives the same Tx
// with the same calls in the same order, and the store cannot tell them apart.
//
// The pre-flight is asked before the store is opened, so a refusable result
// never becomes a connection, a collection or a row. §4.1: what every sink had
// to write for itself belongs above the interface, and this is that sentence
// executed once instead of four times.
//
// Any failure after Begin aborts. §4.1 again: a half-written load has to be
// observable rather than merely unlikely, so the store is left saying it is
// unfinished instead of looking finished.
func Load(ctx context.Context, s Sink, res alchemy.Result, opts Options) (Report, error) {
	if err := preflight.Refuse(res); err != nil {
		return Report{}, err
	}
	id := Ident{
		Load:    name(opts.Load, res),
		Digest:  Digest(res),
		Replace: opts.Replace,
		Vectors: vectorsOf(res),
	}
	// Named after the content only once the digest exists, so the fallback is
	// the same string a store would compute for the same graph.
	if id.Load == "" {
		id.Load = "ld_" + id.Digest[:24]
	}

	tx, err := s.Begin(ctx, id)
	if err != nil {
		return Report{}, err
	}
	rep, err := stream(ctx, tx, res, batchOf(opts.Batch))
	// The report travels on the failure path too, carrying what did land.
	// §7.2's rule one level up is the same one: "a failed job that reports no
	// calls makes an expensive retry look free", and an operator holding a
	// half-written store needs to know which load to clean up and how much of
	// it is there.
	rep.Load, rep.Digest = orElse(rep.Load, id.Load), orElse(rep.Digest, id.Digest)
	if err != nil {
		// The abort's own error is deliberately dropped in favour of the one
		// that caused it. A caller told "abort failed" instead of "the endpoint
		// refused this batch" has been handed the second-most-useful sentence.
		_ = tx.Abort(ctx)
		return rep, err
	}
	return rep, nil
}

// orElse fills in the identity the driver asked for only where the store did
// not answer.
//
// A store may finish a load under a different name than the one it was given —
// two loaders racing on one graph under two names both write, and the one that
// loses the store's uniqueness check has to resolve to the graph that is
// actually there. A driver that overwrote that would hand the caller a load
// nobody can find.
func orElse(got, fallback string) string {
	if got != "" {
		return got
	}
	return fallback
}

// stream is everything between Begin and Commit, split out so that the abort
// path above is one place rather than seven.
// The counts are the driver's own rather than the store's, and that is what
// makes them available on the failure path: this is the only party that knows
// how much it handed over, and a store that recounted them would be answering
// from a view it only half has once a load has died.
func stream(ctx context.Context, tx Tx, res alchemy.Result, batch int) (Report, error) {
	var rep Report
	if !tx.Converged() {
		// Entities first, whole, before any relation. It is the contract Tx
		// states and the reason it is a contract: every one of the four stores
		// decides what to do with an edge by asking whether both ends are
		// there, and a store that met the edge first would have to buffer.
		for b := range batches(len(res.Entities), batch) {
			if err := tx.Entities(ctx, res.Entities[b.from:b.to]); err != nil {
				return rep, fmt.Errorf("sink: entities %d..%d: %w", b.from, b.to, err)
			}
			rep.Entities += b.to - b.from
		}
		for b := range batches(len(res.Relations), batch) {
			if err := tx.Relations(ctx, res.Relations[b.from:b.to]); err != nil {
				return rep, fmt.Errorf("sink: relations %d..%d: %w", b.from, b.to, err)
			}
			rep.Relations += b.to - b.from
		}
		chunks := pair(res)
		for b := range batches(len(chunks), batch) {
			if err := tx.Chunks(ctx, chunks[b.from:b.to]); err != nil {
				return rep, fmt.Errorf("sink: chunks %d..%d: %w", b.from, b.to, err)
			}
			rep.Chunks += b.to - b.from
			for _, c := range chunks[b.from:b.to] {
				if c.Vector != nil {
					rep.Vectors++
				}
			}
		}
		// The findings travel after the records they are about, so a store that
		// links a violation to its subject finds the subject already there.
		// They are one call because they are small by construction — one per
		// broken record, one per mapped column, one per unreadable page — and a
		// store that wanted them batched would be optimising the rarest part of
		// a load.
		f := findingsOf(res)
		if err := tx.Findings(ctx, f); err != nil {
			return rep, fmt.Errorf("sink: findings: %w", err)
		}
		rep.Violations, rep.Duplicates = len(f.Violations), len(f.Duplicates)
		rep.Guesses, rep.Unread = len(f.Guesses), len(f.Unread)
		// Last, and after the records for the same reason the findings are: a
		// store that links a retirement to the record it names finds the record
		// already written on the occasions this result contains it. Batched
		// because a correction pass retires as much as it restates, and because
		// a load that dies halfway through one owes an operator the number of
		// retirements that landed — which only the party handing them over
		// knows.
		for b := range batches(len(res.Supersessions), batch) {
			if err := tx.Supersessions(ctx, res.Supersessions[b.from:b.to]); err != nil {
				return rep, fmt.Errorf("sink: supersessions %d..%d: %w", b.from, b.to, err)
			}
			rep.Supersessions += b.to - b.from
		}
	}
	done, err := tx.Commit(ctx, summaryOf(res))
	if err != nil {
		return rep, fmt.Errorf("sink: commit: %w", err)
	}
	// The store's half of the report: what it took to write, what it could not
	// keep, and the name it resolved the load to. The counts stay the driver's.
	rep.Batches, rep.Lost = done.Batches, done.Lost
	rep.Load, rep.Digest = done.Load, done.Digest
	if done.Converged {
		rep.Converged = true
	}
	// Set rather than assigned, for the reason the name is: Commit is also
	// where a store discovers that somebody else committed this graph first,
	// and it says so by returning Converged even though it was not converged
	// when the writes began.
	if tx.Converged() {
		rep.Converged = true
	}
	return rep, nil
}

// pair puts every chunk together with its embedding.
//
// This is the join that used to happen in four stores, and doing it once here
// is what lets Chunk carry both. The lookup is safe because pkg/preflight has
// already refused a result in which two chunks share an index or two vectors
// name one chunk — which is precisely why that check is above the line and not
// in whichever store happened to write it.
func pair(res alchemy.Result) []Chunk {
	if len(res.Chunks) == 0 {
		return nil
	}
	byChunk := make(map[int]int, len(res.Vectors))
	for i, v := range res.Vectors {
		byChunk[v.Chunk] = i
	}
	out := make([]Chunk, 0, len(res.Chunks))
	for _, c := range res.Chunks {
		pc := Chunk{Chunk: c}
		if i, ok := byChunk[c.Index]; ok {
			pc.Vector, pc.Model = res.Vectors[i].Values, res.Vectors[i].Model
		}
		out = append(out, pc)
	}
	return out
}

func findingsOf(res alchemy.Result) Findings {
	return Findings{
		Violations: res.Violations,
		Duplicates: res.Duplicates,
		Guesses:    res.Guesses,
		Unread:     res.Unread,
	}
}

func summaryOf(res alchemy.Result) Summary {
	return Summary{
		Counts:     res.Counts,
		Conflicts:  res.Conflicts,
		RuleSets:   res.RuleSets,
		ModelCalls: res.ModelCalls,
	}
}

// vectorsOf reads the width the store has to bind. pkg/preflight has already
// refused a result whose vectors disagree, so the first one speaks for all of
// them; a result with none reports zero, which is a real answer and not a
// missing one.
func vectorsOf(res alchemy.Result) Vectors {
	if len(res.Vectors) == 0 {
		return Vectors{}
	}
	return Vectors{Dimension: len(res.Vectors[0].Values), Model: res.Vectors[0].Model}
}

func name(override string, res alchemy.Result) string {
	if override != "" {
		return override
	}
	return res.Job
}

func batchOf(n int) int {
	if n <= 0 {
		return DefaultBatch
	}
	return n
}

type span struct{ from, to int }

// batches walks a slice in windows. It is an iterator rather than a slice of
// slices so that a store handed four hundred thousand records never has a
// second copy of the index alongside them.
func batches(n, size int) func(func(span) bool) {
	return func(yield func(span) bool) {
		for at := 0; at < n; at += size {
			end := min(at+size, n)
			if !yield(span{from: at, to: end}) {
				return
			}
		}
	}
}
