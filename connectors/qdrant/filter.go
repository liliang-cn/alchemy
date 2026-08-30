package qdrant

import (
	"context"
	"net/http"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Filter narrows a read to the records a buyer means.
//
// It is a struct of named questions rather than a pass-through of Qdrant's own
// filter JSON, and that is a decision worth stating: a pass-through would be
// more expressive and would leak the payload key names into every caller, so
// that renaming a key here would break code nobody here can see. The questions
// on it are the ones §5b promises can be asked, plus the two — which load,
// which kind — that every read has to answer.
type Filter struct {
	// Loads restricts the read to these loads. Empty means every load that
	// finished, and that default is the one worth thinking about: a buyer who
	// re-imports a corpus without deleting the old load has two copies of
	// every record and gets every answer twice. This connector will not choose
	// for them — merging two runs is the thing it refuses to do — so this is
	// how they choose.
	Loads []string
	// Kinds restricts to entities, relations, chunks, violations, duplicates
	// or loads. Empty means all of them.
	Kinds []string
	// Type is an ontology type: an entity's type or a relation's type.
	Type string
	// Source is Provenance.Source — the file or connection a record came from.
	Source string
	// Producer is Provenance.Producer.
	Producer alchemy.Producer
	// Inferred true is §5b's "filter to the half that was guessed", and false
	// is the other half. It is a pointer because "only the guessed ones",
	// "only the stated ones" and "either" are three questions and a bool has
	// room for two.
	Inferred *bool
	// Model is the model named in provenance, for a buyer with two pipelines.
	Model string
	// Ontology is the vocabulary the extraction was constrained by.
	Ontology string
	// Reviewed true is "somebody put their name on this", false is "nobody
	// has". §5c makes review additive to provenance rather than a flag, so
	// this asks whether the field is there at all.
	Reviewed *bool
	// Dangling narrows to relations whose endpoint is not in the same load.
	// There is no "only the whole ones" beside it because that is the default
	// reading of the store; this is here because a store with no foreign keys
	// has no other way to answer "what did we fail to join", and §7.3 delivers
	// those edges rather than refusing them.
	Dangling bool
	// Entities restricts to these entity IDs. It is what makes a hop a lookup
	// rather than a scan followed by a client-side filter, and it is on Filter
	// rather than private to Around because "give me these nodes" is the
	// commonest thing a caller holding a list of ids wants.
	Entities []string
	// Chunks restricts to records extracted from these chunk indexes. It is
	// how Around gets from a search hit to the graph, and it is on Filter
	// rather than private to that method because "what did we extract from
	// page 14" is a question a buyer asks directly.
	Chunks []int
}

// match is one equality condition.
func match(key string, value any) map[string]any {
	return map[string]any{"key": key, "match": map[string]any{"value": value}}
}

// matchAny is a set membership condition, which is how every "one of these
// loads" and "one of these chunks" is expressed.
func matchAny(key string, values any) map[string]any {
	return map[string]any{"key": key, "match": map[string]any{"any": values}}
}

func isEmpty(key string) map[string]any {
	return map[string]any{"is_empty": map[string]any{"key": key}}
}

// conditions renders everything on the filter except the load scope, which
// needs the server.
func (f Filter) conditions() (must, mustNot []map[string]any) {
	if len(f.Kinds) > 0 {
		must = append(must, matchAny(keyKind, f.Kinds))
	}
	if f.Type != "" {
		must = append(must, match(keyType, f.Type))
	}
	if f.Source != "" {
		must = append(must, match(keyProvSource, f.Source))
	}
	if f.Producer != "" {
		must = append(must, match(keyProvProducer, string(f.Producer)))
	}
	if f.Inferred != nil {
		// The stored field is the positive one — deterministic — because that
		// is the rule pkg/alchemy owns and this connector materialises rather
		// than re-derives. Inferred is its negation and nothing else.
		must = append(must, match(keyProvDeterministic, !*f.Inferred))
	}
	if f.Model != "" {
		must = append(must, match(keyProvModel, f.Model))
	}
	if f.Ontology != "" {
		must = append(must, match(keyProvOntology, f.Ontology))
	}
	if f.Reviewed != nil {
		// An unreviewed record has no prov_reviewed_by key at all, because
		// provenancePayload omits empty optionals. So the question is about
		// the key's presence, which is what is_empty asks.
		if *f.Reviewed {
			mustNot = append(mustNot, isEmpty(keyProvReviewedBy))
		} else {
			must = append(must, isEmpty(keyProvReviewedBy))
		}
	}
	if len(f.Entities) > 0 {
		must = append(must, matchAny(keyEntityID, f.Entities))
	}
	if f.Dangling {
		must = append(must, match(keyRelDangling, true))
	}
	if len(f.Chunks) > 0 {
		must = append(must, matchAny(keyProvChunk, f.Chunks))
	}
	return must, mustNot
}

// resolve turns a Filter into the JSON Qdrant takes, scoping it to loads that
// actually finished.
//
// The extra round trip that costs is the price of atomic visibility. The
// alternative — stamping every point with a "complete" flag when the load
// finishes — makes a read one request instead of two, and makes publishing a
// load a bulk write that can itself fail halfway, which would produce a load
// that is visible in parts. One point flipping is the only publish that cannot
// half-happen, so the read pays.
func (l *Loader) resolve(ctx context.Context, f Filter) (map[string]any, error) {
	must, mustNot := f.conditions()
	loads := f.Loads
	if len(loads) == 0 {
		var err error
		loads, err = l.completeIDs(ctx)
		if err != nil {
			return nil, err
		}
	}
	// An empty list here is not "no scope"; it is "no load qualifies", and it
	// has to match nothing rather than everything. matchAny over an empty list
	// does exactly that, which is why the empty case is not special-cased away.
	must = append(must, matchAny(keyLoad, loads))
	out := map[string]any{"must": must}
	if len(mustNot) > 0 {
		out["must_not"] = mustNot
	}
	return out, nil
}

// Count is how many records match. It is exact rather than estimated, because
// the questions this store is asked — how many edges nobody reviewed, how many
// came out of this file — are questions whose answer is being counted on.
func (l *Loader) Count(ctx context.Context, f Filter) (int, error) {
	flt, err := l.resolve(ctx, f)
	if err != nil {
		return 0, err
	}
	var out struct {
		Count int `json:"count"`
	}
	body := map[string]any{"filter": flt, "exact": true}
	if err := l.call(ctx, http.MethodPost, l.path("/points/count"), body, &out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

// scrolled is one point as a read gets it back.
type scrolled struct {
	ID      string         `json:"id"`
	Payload map[string]any `json:"payload"`
}

// scroll pages through every point matching a filter.
//
// The paging is not optional at any size worth having: Qdrant caps a scroll
// page, and a reader that took the first page for the answer would return a
// prefix of the truth with no error — §8.4's lesson about a big result not
// being one message, arriving here as a big answer not being one response.
func (l *Loader) scroll(ctx context.Context, flt map[string]any, limit int) ([]scrolled, error) {
	var out []scrolled
	var offset any
	page := l.batch
	for {
		if limit > 0 {
			if remaining := limit - len(out); remaining <= 0 {
				return out, nil
			} else if remaining < page {
				page = remaining
			}
		}
		body := map[string]any{
			"filter": flt, "limit": page,
			"with_payload": true, "with_vector": false,
		}
		if offset != nil {
			body["offset"] = offset
		}
		var res struct {
			Points []scrolled `json:"points"`
			Next   any        `json:"next_page_offset"`
		}
		if err := l.call(ctx, http.MethodPost, l.path("/points/scroll"), body, &res); err != nil {
			return nil, err
		}
		out = append(out, res.Points...)
		if res.Next == nil || len(res.Points) == 0 {
			return out, nil
		}
		offset = res.Next
	}
}
