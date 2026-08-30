package qdrant

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// LoadOptions is what a caller can say about one load beyond the result.
type LoadOptions struct {
	// ID names this load. Empty derives it from the fingerprint, which is the
	// answer that needs no coordination: the same result always lands under
	// the same name and a different one never does.
	//
	// Supplying one is for a caller with a better name — an alchemy job ID, a
	// nightly run's date — who then owns not reusing it for a different graph.
	// Reusing it is refused rather than merged.
	ID string
	// Replace removes whatever is under ID and loads this result in its place.
	// It is how a corpus is re-run under a stable name and how a load that
	// died halfway is retried, and it is explicit rather than a default
	// because a connector that replaced by default would make "load the second
	// half of the corpus" delete the first.
	Replace bool
}

// Loaded is what one load did.
type Loaded struct {
	ID          string
	Fingerprint string
	// Already is true when this exact result was already complete in the
	// collection and nothing was written. It is a field rather than an error
	// because a retried nightly job doing nothing is a success, and a caller
	// forced to tell an error it should ignore from one it should not is a
	// caller that will get it wrong.
	Already bool
	// Dimension is the width of this result's vectors, 0 when it had none.
	Dimension  int
	Entities   int
	Relations  int
	Chunks     int
	Vectors    int
	Violations int
	Duplicates int
	// Points is how many points were written, which is the number that makes
	// this store's bill legible; Batches is how many requests it took, which
	// is what an operator wants when one of them failed.
	Points  int
	Batches int
	// Lost is what a vector store could not keep about this graph. See lost().
	// It is on the successful return value on purpose: a connector that said
	// this only in its documentation would be letting a buyer believe they had
	// bought a graph database.
	Lost []string
}

// HeldError refuses a result that still carries an unanswered conflict.
//
// §7.3: "a graph that contradicts itself is worse than no graph, because an
// agent reading it will answer from whichever edge it happened to traverse —
// confidently, with a citation." A connector is the exact step at which that
// stops being a job somebody is watching and becomes a store somebody queries,
// so it is the last place the rule can be enforced and the first place
// breaking it costs something real.
//
// The test for "unanswered" is review.Held, imported rather than
// reimplemented. A second, differently-worded copy of that rule here would be
// a second answer to the question of when a job is finished, and the two would
// disagree the first time either moved.
type HeldError struct {
	Conflicts []alchemy.Conflict
}

func (e *HeldError) Error() string {
	subjects := make([]string, 0, len(e.Conflicts))
	for _, c := range e.Conflicts {
		subjects = append(subjects, fmt.Sprintf("%s (%s)", c.Subject, c.Kind))
	}
	return fmt.Sprintf("qdrant: this result is held for a person: %d conflict(s) are unanswered — %s. "+
		"Nothing was written. Answer them on the Review stream first; a store that took the graph anyway would "+
		"answer questions from whichever side of the contradiction it happened to retrieve",
		len(e.Conflicts), strings.Join(subjects, "; "))
}

// ConflictingLoadError is one load name over two different graphs.
//
// It is an error rather than a merge or an overwrite because Entity.ID says
// nothing across runs: there is no key on which the two could be joined, and
// picking one silently would be the store deciding a question the data does
// not answer. It matters more here than in a store with a primary key, because
// the two graphs' points do not even collide — they would sit side by side
// under one name, and every query scoped to that name would answer from both.
type ConflictingLoadError struct {
	ID       string
	Have     string
	Want     string
	Complete bool
}

func (e *ConflictingLoadError) Error() string {
	state := "was left incomplete"
	if e.Complete {
		state = "is complete"
	}
	if e.Have == e.Want {
		return fmt.Sprintf("qdrant: load %q already holds this same result and %s; "+
			"if it died halfway, load it again with Replace to take the name over", e.ID, state)
	}
	return fmt.Sprintf("qdrant: load %q already holds a different result (%s… vs %s…) and %s. Nothing was written. "+
		"Entity IDs are stable within one result and mean nothing across runs, so these two graphs cannot be merged "+
		"on anything; give this one another ID, or pass Replace to mean it", e.ID, short(e.Have), short(e.Want), state)
}

// DuplicateEntityError is a result whose own entity IDs collide.
//
// alchemy.Entity.ID "is how relations refer to entities", so two entities under
// one ID make every relation naming it ambiguous. It is a broken result rather
// than a storage problem, and here it would also be a silent one: both
// entities derive the same point ID, so the second would overwrite the first
// and the load would report two entities written where the store holds one.
type DuplicateEntityError struct {
	ID string
}

func (e *DuplicateEntityError) Error() string {
	return fmt.Sprintf("qdrant: two entities in this result have the ID %q, and relations refer to entities by ID; "+
		"nothing was written, because they derive one point and either one of them would make some edge point at the wrong node", e.ID)
}

// Load writes one result into the collection.
//
// The order of the steps is the design. Everything that can refuse — the held
// conflict, the colliding entity IDs, the result's own dimension — happens
// before the collection is so much as created, so a refusal leaves the server
// exactly as it found it. Everything that writes happens under a load marker
// that says complete=false, and every read in this package excludes loads
// whose marker does not say true. §8.4 makes a large load many requests, so a
// failure in the middle is an ordinary event; what this arrangement buys is
// that the failure leaves a load a reader can see is half-loaded rather than a
// collection that looks finished.
func (l *Loader) Load(ctx context.Context, res alchemy.Result, opts LoadOptions) (Loaded, error) {
	if held := review.Held(res); len(held) > 0 {
		return Loaded{}, &HeldError{Conflicts: held}
	}
	if err := checkEntityIDs(res); err != nil {
		return Loaded{}, err
	}
	dim, model, err := dimensionOf(res)
	if err != nil {
		return Loaded{}, err
	}
	fp, err := Fingerprint(res)
	if err != nil {
		return Loaded{}, err
	}
	out := Loaded{
		Fingerprint: fp, Dimension: dim, ID: opts.ID,
		Entities:   len(res.Entities),
		Relations:  len(res.Relations),
		Chunks:     len(res.Chunks),
		Vectors:    len(res.Vectors),
		Violations: len(res.Violations),
		Duplicates: len(res.Duplicates),
		Lost:       lost(res, dim),
	}
	if out.ID == "" {
		out.ID = "ld_" + fp[:24]
	}

	// The collection is created here, at the width this result actually
	// carries, because there is no later moment: Qdrant fixes the width at
	// creation and has no ALTER.
	if err := l.ensure(ctx, dim, model); err != nil {
		return Loaded{}, err
	}
	prior, err := l.marker(ctx, out.ID)
	if err != nil {
		return Loaded{}, err
	}
	if !opts.Replace {
		if prior != nil && prior.Fingerprint == fp && prior.Complete {
			out.Already = true
			return out, nil
		}
		if prior != nil {
			return Loaded{}, &ConflictingLoadError{ID: out.ID, Have: prior.Fingerprint, Want: fp, Complete: prior.Complete}
		}
		// A different name over the same graph is still the same graph. Doing
		// it again under a second name would double every answer for a corpus
		// nobody changed, which is the one thing idempotency has to prevent.
		if id, ok, err := l.completeFingerprint(ctx, fp); err != nil {
			return Loaded{}, err
		} else if ok {
			out.ID, out.Already = id, true
			return out, nil
		}
	} else if prior != nil {
		if err := l.deleteLoad(ctx, out.ID); err != nil {
			return Loaded{}, err
		}
	}

	if err := l.claim(ctx, out); err != nil {
		return Loaded{}, err
	}
	batches := build(res, fp, out.ID)
	for _, b := range batches {
		n, reqs, err := l.upsert(ctx, b)
		out.Points += n
		out.Batches += reqs
		if err != nil {
			// Nothing is undone. The marker still says complete=false, every
			// read excludes it, and Loads() shows an operator exactly which
			// load stopped and how much of it arrived — which is more useful
			// than a rollback that would itself be a bulk operation able to
			// fail halfway.
			return out, err
		}
	}
	if err := l.complete(ctx, out, res); err != nil {
		return out, err
	}
	return out, nil
}

// checkEntityIDs is a pass over the result the type system does not do.
// Entity.ID is documented as "stable within one result", which a caller can
// read as a promise and a producer can break.
func checkEntityIDs(res alchemy.Result) error {
	seen := make(map[string]bool, len(res.Entities))
	for _, e := range res.Entities {
		if seen[e.ID] {
			return &DuplicateEntityError{ID: e.ID}
		}
		seen[e.ID] = true
	}
	return nil
}

// upsert writes one kind's points, in batches, and reports how many points and
// how many requests it took.
//
// wait=true on every request is a deliberate cost. Qdrant will acknowledge a
// write before it is searchable, and a load that returned while its points
// were still landing would make "the load finished" and "the load can be
// queried" two different moments — which is precisely the ambiguity the
// complete marker exists to remove.
func (l *Loader) upsert(ctx context.Context, b batchOf) (int, int, error) {
	written, reqs := 0, 0
	for start := 0; start < len(b.points); start += l.batch {
		end := min(start+l.batch, len(b.points))
		body := map[string]any{"points": b.points[start:end]}
		if err := l.call(ctx, http.MethodPut, l.path("/points?wait=true"), body, nil); err != nil {
			return written, reqs, fmt.Errorf("qdrant: writing %s points %d–%d: %w", b.kind, start, end, err)
		}
		written += end - start
		reqs++
		if l.hooks.afterBatch != nil {
			if err := l.hooks.afterBatch(string(b.kind), written); err != nil {
				return written, reqs, err
			}
		}
	}
	return written, reqs, nil
}

// claim writes the marker before any data.
//
// The marker is what makes a half-written load detectable rather than merely
// unlikely: it exists from before the first point, it says complete=false, and
// every read in this package resolves the set of complete loads before it
// filters. A connector that wrote the marker last would leave a window in
// which the points are there and nothing knows they are partial, which is the
// one outcome this much machinery is bought to avoid.
func (l *Loader) claim(ctx context.Context, out Loaded) error {
	p := base(out.ID, kindLoad)
	p[keyFingerprint] = out.Fingerprint
	p[keyComplete] = false
	p[keyStartedAt] = time.Now().UTC().Format(time.RFC3339Nano)
	p[keyDimension] = out.Dimension
	p[keyLost] = out.Lost
	body := map[string]any{"points": []point{{
		ID: pointID(out.Fingerprint, kindLoad, out.ID), Vector: vectorless(), Payload: p,
	}}}
	return l.call(ctx, http.MethodPut, l.path("/points?wait=true"), body, nil)
}

// complete flips the marker and is the only statement that makes a load
// visible. §5's numbers are written here rather than at claim time, because a
// marker advertising counts while its points were still arriving would be a
// store making claims it could not yet answer with.
func (l *Loader) complete(ctx context.Context, out Loaded, res alchemy.Result) error {
	p := base(out.ID, kindLoad)
	p[keyFingerprint] = out.Fingerprint
	p[keyComplete] = true
	p[keyStartedAt] = time.Now().UTC().Format(time.RFC3339Nano)
	p[keyFinishedAt] = time.Now().UTC().Format(time.RFC3339Nano)
	p[keyDimension] = out.Dimension
	p[keyLost] = out.Lost
	p[keyPoints] = out.Points
	// §5's obligation travels with the graph: "every returned graph is
	// accompanied by the numbers needed to distrust it". A store that kept the
	// records and dropped the counts kept the half that looks good.
	p[keyCounts] = res.Counts
	// The findings that are read whole rather than filtered on. The conflicts
	// here are answered ones by construction — an unanswered one refused the
	// load — and keeping them records that a person decided, rather than that
	// nothing was ever in question.
	p[keyConflicts] = res.Conflicts
	p[keyGuesses] = res.Guesses
	p[keyUnread] = res.Unread
	p[keyRuleSets] = res.RuleSets
	p[keyModelCalls] = res.ModelCalls
	body := map[string]any{"points": []point{{
		ID: pointID(out.Fingerprint, kindLoad, out.ID), Vector: vectorless(), Payload: p,
	}}}
	if err := l.call(ctx, http.MethodPut, l.path("/points?wait=true"), body, nil); err != nil {
		return fmt.Errorf("qdrant: completing load %s: %w", out.ID, err)
	}
	return nil
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
