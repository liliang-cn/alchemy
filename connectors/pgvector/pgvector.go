// Package pgvector loads an alchemy.Result into PostgreSQL with the pgvector
// extension, so that a graph alchemy returned can be searched by vector
// similarity and read as the graph around what the search found.
//
// It is a connector, not a storage layer for the service. DESIGN.md §4 decided
// alchemy "returns; it does not store", and the cost that decision names —
// "our own projects gain a thin write layer" — is what this is. Nothing here
// is reachable from pkg/service, pkg/pipeline or cmd/alchemy, and it lives in
// its own module so that a buyer who wants pgvector does not pull Neo4j.
//
// Three properties are the reason to prefer it over an INSERT loop somebody
// writes in an afternoon:
//
//   - Provenance survives. §5b makes "every entity and relation can name the
//     source, the chunk and the producer it came from" a product guarantee, so
//     every field of alchemy.Provenance is a column and none of them is
//     optional. A store that keeps the edge and loses which policy the model
//     was working under has kept the half that is easy.
//
//   - A half-written load is never readable. A large result is many
//     transactions (§8.4), so a failure in the middle is a normal event rather
//     than an emergency; the load row is written first as incomplete and the
//     read views hide it until the last statement commits. The worst outcome
//     available here is a half-loaded store nobody can tell is half-loaded,
//     and it is the one outcome this design makes unreachable.
//
//   - A held job cannot be loaded. §7.3: a graph that contradicts itself is
//     worse than no graph, and a connector that let an unanswered conflict
//     into a store would be the confident wrong answer that whole section
//     exists to prevent.
package pgvector

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config is what a loader needs beyond a database.
type Config struct {
	// Schema is where the tables live. Empty means "public". A schema per
	// corpus is how two unrelated imports share one database without one
	// answering queries with the other's edges.
	Schema string
	// Batch is how many rows one COPY statement carries. §8.4: a large result
	// is not one INSERT and not one transaction, and this is the number that
	// makes that true. Zero means DefaultBatch.
	Batch int
	// Dimension binds the embedding column at Migrate time. Zero is the
	// normal case and means "learn it from the first result that has
	// vectors"; see bindDimension for why a job-time fact is allowed to reach
	// the schema late rather than be guessed early.
	Dimension int
	// Distance is the metric the vector index is built for. Empty means
	// Cosine. It is configuration rather than something read off the result
	// because alchemy.Vector carries the model's name and not the metric that
	// model was trained for, and picking the wrong one is a recall problem
	// that looks like a quality problem.
	Distance Distance
}

// Distance is the pgvector operator class an index and a search agree on.
// They have to agree: an index built for cosine does not serve an L2 query,
// and the failure is a sequential scan that returns the right answer slowly,
// which nobody notices until the table is large.
type Distance string

const (
	// Cosine is the default because embeddings are normally compared by angle,
	// and because a model that returns unnormalised vectors makes L2 answer a
	// question about magnitude that nobody asked.
	Cosine Distance = "cosine"
	// L2 is euclidean distance.
	L2 Distance = "l2"
	// InnerProduct is the negative inner product, for models that document it.
	InnerProduct Distance = "ip"
)

// opClass is the index operator class, and operator the search operator. They
// are returned together so a call site cannot pair one metric's index with
// another's query.
func (d Distance) opClass() (string, string, error) {
	switch d {
	case "", Cosine:
		return "vector_cosine_ops", "<=>", nil
	case L2:
		return "vector_l2_ops", "<->", nil
	case InnerProduct:
		return "vector_ip_ops", "<#>", nil
	}
	return "", "", fmt.Errorf("pgvector: %q is not a distance; use cosine, l2 or ip", d)
}

// DefaultBatch is how many rows one COPY carries when Config.Batch is zero.
// It is a compromise with no magic in it: large enough that the per-statement
// round trip disappears, small enough that a failed batch is a small amount of
// work to have lost and that one statement's memory is bounded.
const DefaultBatch = 5000

// Loader writes results into one schema.
type Loader struct {
	pool *pgxpool.Pool
	// schema qualifies every statement instead of relying on search_path, for
	// the reason pkg/job's store gives: a pool hands out connections a
	// session-level SET does not reliably follow, and "the loader wrote into
	// the wrong schema" is a bug discovered exactly once.
	schema string
	batch  int
	dim    int
	dist   Distance
	// owns the pool: a loader built from a caller's pool must not close it.
	owns bool
	// hooks is the test seam for proving what a failure halfway through
	// leaves behind. It is unexported and nil in every path a caller can
	// reach, so the production code has one shape.
	hooks hooks
}

// hooks lets a test fail a load at a chosen point. §8.4's real question is not
// whether a big load works but what a broken one leaves, and that cannot be
// asserted without being able to break one on purpose.
type hooks struct {
	afterBatch func(table string, n int) error
}

// identRE guards the one string in this package that is interpolated into SQL
// rather than bound as a parameter. An identifier cannot be a placeholder, so
// it is validated at construction instead of trusted at every call site.
var identRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Open dials the DSN and returns a loader. Migrate still has to be called.
func Open(ctx context.Context, dsn string, cfg Config) (*Loader, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgvector: %w", err)
	}
	l, err := New(pool, cfg)
	if err != nil {
		pool.Close()
		return nil, err
	}
	l.owns = true
	return l, nil
}

// New builds a loader on a pool the caller already has, which is how a service
// that already talks to this database avoids a second pool.
func New(pool *pgxpool.Pool, cfg Config) (*Loader, error) {
	if cfg.Schema == "" {
		cfg.Schema = "public"
	}
	if !identRE.MatchString(cfg.Schema) {
		return nil, fmt.Errorf("pgvector: %q is not a usable schema name", cfg.Schema)
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultBatch
	}
	if cfg.Dimension < 0 {
		return nil, fmt.Errorf("pgvector: dimension %d is not a dimension", cfg.Dimension)
	}
	if _, _, err := cfg.Distance.opClass(); err != nil {
		return nil, err
	}
	return &Loader{pool: pool, schema: cfg.Schema, batch: cfg.Batch, dim: cfg.Dimension, dist: cfg.Distance}, nil
}

// Close releases the pool, if this loader opened it.
func (l *Loader) Close() {
	if l.owns {
		l.pool.Close()
	}
}

// Schema is where this loader writes, for a caller that wants to say so in a
// log line.
func (l *Loader) Schema() string { return l.schema }

// q interpolates the schema. The only thing ever interpolated is l.schema,
// which identRE has already vetted.
func (l *Loader) q(sql string) string {
	return strings.ReplaceAll(sql, "{s}", l.schema)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
