package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// DimensionError is the refusal that keeps a vector store honest.
//
// A collection fixes its vector size when it is created and Qdrant has no
// ALTER: unlike the pgvector connector, which can bind a late column on a
// schema that has been loading for an hour, there is nothing here to widen.
// The two ways to make the problem disappear are both failures that look like
// successes — padding or truncating answers queries with vectors that no
// longer mean what the model said, and quietly creating a second collection
// answers them with half the corpus and no error. So a mismatch is an error
// carrying both numbers and the model's name, and nothing is written.
type DimensionError struct {
	Collection string
	// Have is the collection's vector width. Zero means the collection holds
	// no vector at all, which is a different problem with a different answer.
	Have int
	// Want is what the caller configured or the result carries.
	Want  int
	Model string
	// Where names the part of the result that disagreed, for the case where a
	// single result is not internally consistent.
	Where string
}

func (e *DimensionError) Error() string {
	switch {
	case e.Where != "":
		return fmt.Sprintf("qdrant: %s: vectors of %d and %d dimensions in one result (model %q); "+
			"a result whose own vectors disagree cannot be stored under either", e.Where, e.Have, e.Want, e.Model)
	case e.Have == 0:
		return fmt.Sprintf("qdrant: collection %q was created with no embedding vector and this result carries "+
			"vector(%d) from model %q; nothing was written. Qdrant cannot add a named vector to a collection that "+
			"exists, so this corpus needs a collection of its own — the alternative would be a store that holds the "+
			"text and cannot search it", e.Collection, e.Want, e.Model)
	default:
		return fmt.Sprintf("qdrant: collection %q stores vector(%d) and this result carries vector(%d) from model %q; "+
			"nothing was written. Load it into a collection of its own, or re-embed the corpus with one model — "+
			"padding or truncating would answer queries with vectors that mean something else",
			e.Collection, e.Have, e.Want, e.Model)
	}
}

// collectionInfo is the part of Qdrant's collection description this connector
// reads. The vectors config is decoded as a raw map because it is one shape
// for a default unnamed vector and another for named ones, and this package
// only ever creates the named one.
type collectionInfo struct {
	// PayloadSchema is what the server says it has indexed. It is read rather
	// than assumed because a collection created by an older version of this
	// connector, or by a buyer's own script, is one an index list can silently
	// disagree with — and the disagreement costs a scan, not an error.
	PayloadSchema map[string]json.RawMessage `json:"payload_schema"`
	Config        struct {
		Params struct {
			Vectors map[string]struct {
				Size     int      `json:"size"`
				Distance Distance `json:"distance"`
			} `json:"vectors"`
		} `json:"params"`
	} `json:"config"`
}

// info reads the collection, or reports that there is none.
func (l *Loader) info(ctx context.Context) (*collectionInfo, error) {
	var ci collectionInfo
	err := l.call(ctx, http.MethodGet, l.path(""), nil, &ci)
	var ae *APIError
	if errors.As(err, &ae) && ae.NotFound() {
		return nil, ErrNoCollection
	}
	if err != nil {
		return nil, err
	}
	return &ci, nil
}

// CollectionDimension reports the width the collection's "text" vector is
// declared at, or 0 if the collection holds no vector.
//
// It asks the server rather than returning what this loader was configured
// with, because the collection's own declaration is the only copy that cannot
// drift from the points in it. A remembered number and a real collection that
// disagree is a store reporting a dimension it does not have.
func (l *Loader) CollectionDimension(ctx context.Context) (int, error) {
	ci, err := l.info(ctx)
	if err != nil {
		return 0, err
	}
	return ci.Config.Params.Vectors[vectorName].Size, nil
}

// indexedFields is which payload fields the server has an index for.
func (l *Loader) indexedFields(ctx context.Context) (map[string]bool, error) {
	ci, err := l.info(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ci.PayloadSchema))
	for k := range ci.PayloadSchema {
		out[k] = true
	}
	return out, nil
}

// EnsureCollection creates the collection, or checks that the one there
// already agrees with this loader.
//
// It is separate from Open for the reason the pgvector connector separates
// Migrate: a deployment should be able to start without every node racing to
// create the same thing, and creating a collection in a buyer's server is a
// side effect that deserves its own call.
//
// With Config.Dimension zero it deliberately does nothing at all. The width is
// a job-time fact — alchemy.Vector.Values is whatever the caller's embedding
// model returned — and a collection is created with its width fixed forever.
// Guessing 768 because it is common would produce a collection that refuses
// the first real result; creating a vectorless one would produce a collection
// that can never hold an embedding. So the decision is deferred to the first
// Load, which is the first moment anybody actually knows.
func (l *Loader) EnsureCollection(ctx context.Context) error {
	if l.dim == 0 {
		return nil
	}
	return l.ensure(ctx, l.dim, "")
}

// ensure makes the collection exist at width dim, or refuses because it exists
// at another. dim of 0 creates a collection with no vector at all, which is
// the right shape for a result that carries none (a DDL import has no chunks
// and therefore no embeddings) and a dead end for one that does — see
// DimensionError.
//
// It retries a lost creation race rather than reporting it. Two processes
// loading the first two results of a corpus at once is an ordinary event — a
// deployment starting, a nightly fan-out — and the loser is looking at exactly
// the collection it wanted. It cannot simply read the collection back on the
// spot, either: for a moment after the 409 the server can still answer 404 for
// it, which is how this was found. So the loser waits and asks again, and only
// a disagreement about the width is an error.
func (l *Loader) ensure(ctx context.Context, dim int, model string) error {
	var last error
	for attempt := range 4 {
		if attempt > 0 {
			// Short and growing: the window is the server finishing a create,
			// which is milliseconds, and a caller blocked here is a caller
			// whose load has not started.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
			}
		}
		err := l.ensureOnce(ctx, dim, model)
		if err == nil {
			return nil
		}
		var de *DimensionError
		if errors.As(err, &de) {
			return err
		}
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			last = err
			continue
		}
		return err
	}
	return last
}

func (l *Loader) ensureOnce(ctx context.Context, dim int, model string) error {
	ci, err := l.info(ctx)
	if err != nil && !errors.Is(err, ErrNoCollection) {
		return err
	}
	if err == nil {
		have := ci.Config.Params.Vectors[vectorName].Size
		if have == dim || dim == 0 {
			// A vectorless result into a collection that has a vector is fine:
			// its entities and relations are vectorless points either way.
			return l.ensureIndexes(ctx)
		}
		return &DimensionError{Collection: l.collection, Have: have, Want: dim, Model: model}
	}

	body := map[string]any{"vectors": map[string]any{}}
	if dim > 0 {
		body["vectors"] = map[string]any{
			vectorName: map[string]any{"size": dim, "distance": string(l.dist)},
		}
	}
	if err := l.call(ctx, http.MethodPut, l.path(""), body, nil); err != nil {
		return err
	}
	return l.ensureIndexes(ctx)
}

// payloadIndexes is every field this package filters on, with the schema
// Qdrant needs to index it.
//
// Qdrant will filter on an unindexed payload field, which is what makes this
// list easy to leave out and expensive to have left out: without an index the
// filter is a scan of every point in the collection, so the query that was
// three milliseconds on the test corpus is three seconds on the buyer's, and
// nothing anywhere says why. The rule for what is on this list is therefore
// mechanical — every field any query in this package puts in a filter is here,
// and nothing else is, because an index that is never filtered on is memory
// and write amplification bought for nothing.
var payloadIndexes = []struct {
	field  string
	schema string
}{
	// The two every query carries: which sort of point, and which load.
	{keyKind, "keyword"},
	{keyLoad, "keyword"},
	// Around's two steps: entities extracted from a hit chunk, then the edges
	// touching those entities. These three are what make a one-hop traversal a
	// lookup rather than a scan, and they are the closest this store gets to
	// what a graph database does in its storage engine.
	{keyProvChunk, "integer"},
	{keyRelFrom, "keyword"},
	{keyRelTo, "keyword"},
	{keyEntityID, "keyword"},
	{keyRelDangling, "bool"},
	// §5b as a query: "a person auditing it can filter to the half that was
	// guessed", and the neighbouring questions — which file, which model,
	// which vocabulary, and what nobody has reviewed.
	{keyProvProducer, "keyword"},
	{keyProvDeterministic, "bool"},
	{keyProvSource, "keyword"},
	{keyProvModel, "keyword"},
	{keyProvOntology, "keyword"},
	{keyProvReviewedBy, "keyword"},
	// The ontology type, for "every Service in this corpus".
	{keyType, "keyword"},
	// Chunk lookup by index, for reading a hit's neighbours in the source.
	{keyChunkIndex, "integer"},
}

// ensureIndexes creates the payload indexes. Creating one that exists is a
// no-op on the server, so this runs on every EnsureCollection rather than only
// on creation — which is what makes a collection created by an older version
// of this connector pick up a new index instead of quietly filtering by scan.
//
// wait=true on each one is not politeness, and it was learned the hard way:
// without it the server acknowledges every request and creates only the first
// index, because the rest arrive while the collection is still applying that
// one. Nothing fails, no query returns a wrong answer, and every filter in
// this package becomes a full scan — which is why the test for this asks the
// server what it has indexed rather than trusting that the calls were made.
func (l *Loader) ensureIndexes(ctx context.Context) error {
	for _, idx := range payloadIndexes {
		body := map[string]any{"field_name": idx.field, "field_schema": idx.schema}
		if err := l.call(ctx, http.MethodPut, l.path("/index?wait=true"), body, nil); err != nil {
			return fmt.Errorf("qdrant: indexing payload field %q: %w", idx.field, err)
		}
	}
	return nil
}

// DropCollection removes the collection and everything in it. It is the undo
// for an import, and it is a separate verb from Delete (which removes one
// load) because "I imported the wrong corpus" and "I imported this corpus
// twice" are different mistakes with different blast radii.
func (l *Loader) DropCollection(ctx context.Context) error {
	err := l.call(ctx, http.MethodDelete, l.path("?wait=true"), nil, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.NotFound() {
		return nil
	}
	return err
}
