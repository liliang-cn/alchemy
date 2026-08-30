package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Load is one import as the store holds it.
type Load struct {
	ID          string
	Fingerprint string
	// Complete is false for a load that started and never finished. Every read
	// in this package excludes those; this field is how an operator sees them
	// anyway, which is the whole point of writing the marker first.
	Complete   bool
	Dimension  int
	StartedAt  time.Time
	FinishedAt time.Time
	Points     int
	// Counts is §5's block as it was returned with the result. It is kept
	// because a graph without the numbers needed to distrust it is the release
	// that section refuses to ship, and a store that drops them on the way in
	// has undone that for every reader downstream.
	Counts alchemy.Counts
	// Lost is what this store could not keep about that graph.
	Lost []string
}

// Loads lists what the collection holds, incomplete loads included. They are
// included on purpose: the reads hide them from queries, and an operator
// asking what is in the store is precisely the person who should see the one
// that has been loading for six hours.
func (l *Loader) Loads(ctx context.Context) ([]Load, error) {
	pts, err := l.scroll(ctx, map[string]any{"must": []map[string]any{match(keyKind, string(kindLoad))}}, 0)
	if err != nil {
		if errors.Is(err, ErrNoCollection) {
			return nil, nil
		}
		var ae *APIError
		if errors.As(err, &ae) && ae.NotFound() {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Load, 0, len(pts))
	for _, p := range pts {
		out = append(out, readLoad(p.Payload))
	}
	// Ordered by when they started, so that a list read by a person reads as
	// the history it is.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out, nil
}

func readLoad(p map[string]any) Load {
	ld := Load{
		ID:          str(p[keyLoad]),
		Fingerprint: str(p[keyFingerprint]),
		Dimension:   num(p[keyDimension]),
		Points:      num(p[keyPoints]),
	}
	ld.Complete, _ = p[keyComplete].(bool)
	ld.StartedAt, _ = time.Parse(time.RFC3339Nano, str(p[keyStartedAt]))
	ld.FinishedAt, _ = time.Parse(time.RFC3339Nano, str(p[keyFinishedAt]))
	// The counts went in as the struct and come back as a JSON object, so they
	// are re-decoded rather than picked apart field by field: a field added to
	// alchemy.Counts should arrive here without this function being edited.
	if raw, err := json.Marshal(p[keyCounts]); err == nil {
		_ = json.Unmarshal(raw, &ld.Counts)
	}
	for _, s := range asSlice(p[keyLost]) {
		ld.Lost = append(ld.Lost, str(s))
	}
	return ld
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// marker reads one load's marker, or nil when there is none.
func (l *Loader) marker(ctx context.Context, id string) (*Load, error) {
	flt := map[string]any{"must": []map[string]any{
		match(keyKind, string(kindLoad)), match(keyLoad, id),
	}}
	pts, err := l.scroll(ctx, flt, 2)
	if err != nil {
		return nil, err
	}
	if len(pts) == 0 {
		return nil, nil
	}
	ld := readLoad(pts[0].Payload)
	return &ld, nil
}

// completeFingerprint finds a finished load carrying this exact graph, under
// whatever name it was given.
func (l *Loader) completeFingerprint(ctx context.Context, fp string) (string, bool, error) {
	flt := map[string]any{"must": []map[string]any{
		match(keyKind, string(kindLoad)), match(keyFingerprint, fp), match(keyComplete, true),
	}}
	pts, err := l.scroll(ctx, flt, 1)
	if err != nil || len(pts) == 0 {
		return "", false, err
	}
	return str(pts[0].Payload[keyLoad]), true, nil
}

// completeIDs is the set of loads a read is allowed to answer from.
func (l *Loader) completeIDs(ctx context.Context) ([]string, error) {
	flt := map[string]any{"must": []map[string]any{
		match(keyKind, string(kindLoad)), match(keyComplete, true),
	}}
	pts, err := l.scroll(ctx, flt, 0)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(pts))
	for _, p := range pts {
		out = append(out, str(p.Payload[keyLoad]))
	}
	return out, nil
}

// deleteLoad removes every point of one load, marker included.
//
// It is a single delete-by-filter rather than the batched loop the pgvector
// connector needs, and that is Qdrant doing something genuinely better: the
// server owns the iteration, so there is no client-side transaction to hold
// open across four hundred thousand rows and no vacuum horizon to pin. What is
// given up is atomicity — a delete that fails halfway leaves some of the load
// — which is why the marker goes last: a half-deleted load still has its
// marker, still reads as incomplete, and is still excluded from every query.
func (l *Loader) deleteLoad(ctx context.Context, id string) error {
	for _, flt := range []map[string]any{
		{"must": []map[string]any{match(keyLoad, id)}, "must_not": []map[string]any{match(keyKind, string(kindLoad))}},
		{"must": []map[string]any{match(keyLoad, id), match(keyKind, string(kindLoad))}},
	} {
		body := map[string]any{"filter": flt}
		if err := l.call(ctx, http.MethodPost, l.path("/points/delete?wait=true"), body, nil); err != nil {
			return fmt.Errorf("qdrant: deleting load %s: %w", id, err)
		}
	}
	return nil
}

// Delete removes one load by name. A buyer who re-imports a corpus and wants
// the old one gone has to be able to say so, and saying it through the
// connector gets the marker-last ordering rather than a filter somebody writes
// by hand that takes the marker first.
func (l *Loader) Delete(ctx context.Context, id string) error {
	return l.deleteLoad(ctx, id)
}

// Swept is what one sweep removed.
type Swept struct {
	// Abandoned names the loads that were left incomplete and have now been
	// removed. It is a list rather than a count because an operator watching
	// this number climb needs to know which loads, so they can go and find out
	// what is killing them.
	Abandoned []string
}

// Sweep removes loads that started and never finished.
//
// The cutoff is a duration compared against the marker's own started_at, and
// the comparison is made here rather than by the server because Qdrant has no
// now(). That is a real weakness next to the pgvector connector, which pushes
// the arithmetic into the database precisely so that the answer to "is this
// load stale" does not depend on which machine asked. Here it does, so the
// cutoff wants to be comfortably longer than a load takes, and a machine with
// a wrong clock will sweep the wrong things.
func (l *Loader) Sweep(ctx context.Context, olderThan time.Duration) (Swept, error) {
	if olderThan <= 0 {
		return Swept{}, fmt.Errorf("qdrant: a sweep cutoff of %v would remove loads that are still running", olderThan)
	}
	loads, err := l.Loads(ctx)
	if err != nil {
		return Swept{}, err
	}
	var out Swept
	cutoff := time.Now().UTC().Add(-olderThan)
	for _, ld := range loads {
		if ld.Complete || !ld.StartedAt.Before(cutoff) {
			continue
		}
		if err := l.deleteLoad(ctx, ld.ID); err != nil {
			return out, err
		}
		out.Abandoned = append(out.Abandoned, ld.ID)
	}
	return out, nil
}
