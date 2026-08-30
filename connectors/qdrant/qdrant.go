// Package qdrant loads an alchemy.Result into Qdrant.
//
// It is a connector a buyer runs, not a write path the service grew.
// DESIGN.md §4 decided alchemy "returns; it does not store", and this is the
// cost that decision names — "our own projects gain a thin write layer".
// Nothing here is reachable from pkg/service, pkg/pipeline or cmd/alchemy, and
// it lives in the connectors module so that a buyer who wants Neo4j does not
// pay for Qdrant.
//
// # What a graph becomes in a vector store, and what is lost
//
// Qdrant is not a graph store. It holds points: an id, an optional vector, and
// a JSON payload. So "load a knowledge graph into a vector database" is a
// modelling question with no default answer, and the honest half of this
// package is the answer and its price.
//
// The answer: one collection, four sorts of point, one named vector.
//
//   - A chunk is a point with the "text" vector and the chunk's text in its
//     payload. This is the only kind alchemy.Result has a vector for, and
//     therefore the only kind similarity search can reach.
//   - An entity is a point with no vector. Qdrant permits that, and it is the
//     truthful shape: alchemy carries no embedding for an entity, so inventing
//     one — hashing the name, averaging the chunks it appeared in — would be
//     this connector asserting a semantic fact the pipeline never produced,
//     which is precisely the inference wearing a producer's badge that §2.1
//     warns about. What it costs is that entities are not similarity-
//     searchable; they are filterable and retrievable, which is what a payload
//     store can honestly offer.
//   - A relation is also a vectorless point, and this is where the loss is
//     real enough to be printed rather than documented. In Neo4j an edge is a
//     traversable thing the storage engine walks; here it is a row with
//     from/to ids in its payload. One hop is a filtered scroll — fast, with
//     the payload index this package creates — and n hops are n round trips.
//     There is no path query, no variable-length match, no shortest path, and
//     no way to ask a question about the shape of the graph. A buyer who
//     loaded a graph into Qdrant did not load a graph; they loaded its
//     records, indexed for retrieval. Load says so in Loaded.Lost, and the
//     load marker in the collection says so in its payload, because a
//     connector that returned a bare success here would be letting somebody
//     believe they had a graph database.
//   - The findings and the counts (§5's "numbers needed to distrust it") are
//     points too, because Qdrant has no catalog and a fact that is not in a
//     payload is a fact this store cannot hold.
//
// Two compensations are deliberate, and both are denormalisation, which is the
// only tool a store without joins has. A relation point carries its endpoints'
// names and types, not just their ids, so that an edge is readable without a
// second query into a store that cannot join. And a chunk point carries the
// ids of the entities extracted from it, so that "which text is about this,
// and what did we extract from it" is one search rather than a search and a
// scroll.
//
// # What a buyer can ask afterwards
//
// Three questions, and they are the reason this exists rather than a JSON dump
// in an object store: Search — which text is nearest this query embedding;
// Around — what was extracted from the text a search found, and what those
// records connect to; and Records — every record matching a provenance filter,
// which is §5b's "filter to the half that was guessed" as an actual query.
//
// # The other three properties
//
//   - Provenance survives whole. Every field of alchemy.Provenance is a
//     payload key on every entity, relation and violation, and the ones a
//     buyer filters on are payload-indexed.
//   - A half-written load is detectable. §8.4 makes a large load many
//     requests, so a failure in the middle is a normal event; the load marker
//     is written first saying complete=false, every read this package does
//     excludes incomplete loads, and Loads() shows an operator the one that
//     has been loading for six hours.
//   - A held job cannot be loaded (§7.3), tested with review.Held rather than
//     a second copy of that rule.
package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// vectorName is the one named vector this connector declares.
//
// It is named rather than the collection's default unnamed vector because a
// named vector is per-point optional, which is exactly the fact the model
// needs to express: a chunk has an embedding and an entity does not, in one
// collection. It also leaves room for a corpus re-embedded by a second model
// to arrive as a second named vector without every point ID changing.
const vectorName = "text"

// DefaultBatch is how many points go in one upsert when Config.Batch is zero.
// It is a compromise with no magic in it: large enough that the per-request
// round trip disappears, small enough that one request's JSON body is bounded
// and a failed batch is a small amount of work to have lost. Chunk points
// carry both the source text and a float32 array, so the body of a batch is
// roughly Batch × (text + 4 × dimension) bytes and a bigger number here is a
// bigger thing to hold in memory on both ends.
const DefaultBatch = 256

// Distance is the metric the collection is built for.
//
// It is configuration rather than something read off the result, because
// alchemy.Vector carries the model's name and not the geometry that model was
// trained for. Getting it wrong is not an error at any point: the load
// succeeds, the search returns results, and they are quietly the wrong ones —
// a recall problem that presents as a quality problem months later.
type Distance string

const (
	// Cosine is the default, because embeddings are normally compared by angle
	// and because a model that returns unnormalised vectors makes Euclid
	// answer a question about magnitude nobody asked. The pgvector connector
	// defaults to cosine for the same reason; two stores of one corpus
	// disagreeing about the metric would rank the same query differently.
	Cosine Distance = "Cosine"
	// Dot is the inner product, for models that document it.
	Dot Distance = "Dot"
	// Euclid is L2.
	Euclid Distance = "Euclid"
	// Manhattan is L1.
	Manhattan Distance = "Manhattan"
)

func (d Distance) valid() (Distance, error) {
	switch d {
	case "":
		return Cosine, nil
	case Cosine, Dot, Euclid, Manhattan:
		return d, nil
	}
	return "", fmt.Errorf("qdrant: %q is not a distance; use Cosine, Dot, Euclid or Manhattan", d)
}

// Config is what a loader needs beyond a server.
type Config struct {
	// Collection is where points go. It is required and has no default: a
	// connector that invented a collection name would put a buyer's corpus
	// somewhere they did not choose, and a collection per corpus is how two
	// unrelated imports share one server without answering each other's
	// queries.
	Collection string
	// Dimension binds the collection's vector size at EnsureCollection time.
	// Zero is the normal case and means "learn it from the first result that
	// carries vectors"; see EnsureCollection for why a job-time fact is
	// allowed to reach the schema late rather than be guessed early.
	Dimension int
	// Distance is the metric. Empty means Cosine.
	Distance Distance
	// Batch is how many points one upsert carries. Zero means DefaultBatch.
	Batch int
	// APIKey is sent as the api-key header when set. The connector does not
	// read it from the environment: a library that picks up credentials
	// nobody passed it is one that talks to the wrong server on the day two
	// are configured.
	APIKey string
	// HTTP lets a caller supply their own client — a proxy, a tuned transport,
	// a test double. Nil means a client with a generous timeout, because a
	// batch upsert of a large corpus is a slow request rather than a hung one.
	HTTP *http.Client
}

// collectionRE is Qdrant's own constraint, applied here so that a bad name
// fails at construction with a sentence rather than at the first request with
// a URL-shaped error. It also keeps the name out of trouble when it is
// interpolated into a path.
var collectionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,254}$`)

// Loader writes results into one collection.
type Loader struct {
	base       string
	http       *http.Client
	collection string
	dim        int
	dist       Distance
	batch      int
	apiKey     string
	// hooks is the test seam for proving what a load that dies halfway leaves
	// behind. §8.4's real question is not whether a big load works but what a
	// broken one leaves, and that cannot be asserted without breaking one on
	// purpose. It is unexported and nil on every path a caller can reach.
	hooks hooks
}

type hooks struct {
	afterBatch func(kind string, n int) error
}

// Open returns a loader for a server. It does not check that the server is
// there, because every method that needs it says so plainly when it is not,
// and a constructor that dials makes a cleanup path — which is where Open is
// most often called from — fail for the wrong reason.
func Open(ctx context.Context, baseURL string, cfg Config) (*Loader, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("qdrant: %q is not a usable Qdrant REST URL; it wants a scheme and a host, e.g. http://localhost:6333", baseURL)
	}
	if !collectionRE.MatchString(cfg.Collection) {
		return nil, fmt.Errorf("qdrant: %q is not a usable collection name; Qdrant takes letters, digits, underscore and hyphen", cfg.Collection)
	}
	dist, err := cfg.Distance.valid()
	if err != nil {
		return nil, err
	}
	if cfg.Dimension < 0 {
		return nil, fmt.Errorf("qdrant: dimension %d is not a dimension", cfg.Dimension)
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultBatch
	}
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	return &Loader{
		base: u.String(), http: hc, collection: cfg.Collection,
		dim: cfg.Dimension, dist: dist, batch: cfg.Batch, apiKey: cfg.APIKey,
	}, nil
}

// Collection is where this loader writes, for a caller that wants to say so in
// a log line.
func (l *Loader) Collection() string { return l.collection }

// APIError is a Qdrant response that was not a success.
//
// It keeps the status and the server's own message rather than flattening both
// into a string, because the two cases a caller acts on differently — "there
// is no such collection" and "that request was wrong" — are told apart by the
// status, and a connector that made them both `errors.New` would push a string
// match onto every caller.
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("qdrant: %s %s: %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// NotFound reports whether the server said the thing addressed is not there.
func (e *APIError) NotFound() bool { return e.Status == http.StatusNotFound }

// ErrNoCollection is returned when a read or a load needs the collection to
// exist and it does not. It is a sentinel because "run EnsureCollection first"
// is a different instruction from "that request was malformed", and a caller
// automating a first-run should be able to tell.
var ErrNoCollection = errors.New("qdrant: the collection does not exist; call EnsureCollection first")

// envelope is the shape every Qdrant REST response has: a result, a status
// that is either the string "ok" or an object carrying the error, and a
// server-side timing.
type envelope struct {
	Result json.RawMessage `json:"result"`
	Status json.RawMessage `json:"status"`
}

// call performs one request and decodes the result into out.
//
// Every request this package makes goes through here, which is what makes the
// api-key header, the context, and the error shape one decision rather than
// twelve.
func (l *Loader) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("qdrant: encoding a %s %s: %w", method, path, err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, l.base+path, rdr)
	if err != nil {
		return fmt.Errorf("qdrant: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if l.apiKey != "" {
		req.Header.Set("api-key", l.apiKey)
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	// Bounded, because an error page from something that is not Qdrant — a
	// proxy, a login portal — should not be read into memory in full before it
	// can be reported as "that was not Qdrant".
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("qdrant: %s %s: reading the response: %w", method, path, err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return &APIError{Status: resp.StatusCode, Method: method, Path: path,
			Message: fmt.Sprintf("the response was not Qdrant's JSON: %s", clip(string(raw)))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Method: method, Path: path, Message: statusMessage(env.Status, raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("qdrant: %s %s: reading the result: %w", method, path, err)
	}
	return nil
}

// statusMessage digs the server's sentence out of the status field, which is
// the string "ok" on success and an object with an "error" on failure.
func statusMessage(status json.RawMessage, whole []byte) string {
	var obj struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(status, &obj); err == nil && obj.Error != "" {
		return obj.Error
	}
	return clip(string(whole))
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// path builds a collection-scoped path. The collection name has been vetted by
// collectionRE, so it is safe in a URL, but it is escaped anyway: the check
// belongs to construction and the escaping belongs here, and a future
// loosening of one should not silently break the other.
func (l *Loader) path(suffix string) string {
	return "/collections/" + url.PathEscape(l.collection) + suffix
}
