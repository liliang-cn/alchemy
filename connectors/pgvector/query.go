package pgvector

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Graph is a piece of the store read back as the types alchemy returned.
//
// It holds one load's records and never two. That is the same rule the
// fingerprint enforces on the way in, applied on the way out: Entity.ID is
// stable within one result and says nothing across runs, so entities from two
// loads that share an ID are not the same entity and a Graph that put them in
// one slice would be asserting they were.
type Graph struct {
	Entities  []alchemy.Entity
	Relations []alchemy.Relation
}

// Hit is one chunk a vector search found.
type Hit struct {
	// Load is which load it came from, and it is on the hit rather than
	// implied because a store holding a nightly re-import holds the same
	// source text twice and a reader has to be able to tell them apart.
	Load  string
	Chunk int
	// Distance is in the metric the loader was configured with. It is not
	// converted to a similarity, because the conversion differs per metric and
	// a number that silently means three things is worse than a raw one.
	Distance float64
	Source   string
	Heading  string
	Text     string
	Model    string
}

// SearchOptions narrows a search.
type SearchOptions struct {
	// Loads restricts the search to these loads. Empty means all of them, and
	// that default is the one worth thinking about: a buyer who re-imports a
	// corpus without deleting the old load has two copies of every chunk in
	// the store, and every hit twice. The connector will not choose for them —
	// merging two runs is the thing it refuses to do — so this is how they
	// choose.
	Loads []string
}

const entCols = `entity_id, type, name, attributes::text, ` + provCols
const relCols = `seq, from_id, to_id, type, attributes::text, ` + provCols

// Search returns the k nearest chunks to a query embedding.
//
// The query vector is bound as a text literal with an explicit ::vector cast
// for the same reason the bulk path writes CSV: `vector` is an extension type
// whose OID is per-database, and a text literal the server parses needs no
// codec registered on a pool this package may not own.
func (l *Loader) Search(ctx context.Context, query []float32, k int, opts SearchOptions) ([]Hit, error) {
	if len(query) == 0 {
		return nil, fmt.Errorf("pgvector: an empty query vector matches nothing")
	}
	if k <= 0 {
		return nil, fmt.Errorf("pgvector: k = %d is not a number of results", k)
	}
	bound, err := l.boundDimension(ctx)
	if err != nil {
		return nil, err
	}
	if bound == 0 {
		return nil, fmt.Errorf("pgvector: this schema holds no embeddings yet, so there is nothing to search; " +
			"load a result that carries vectors first")
	}
	if bound != len(query) {
		// Postgres would refuse this too, with a message about types. Saying
		// it here says it in the buyer's terms: the query came from a
		// different embedding model than the corpus, which is a wrong answer
		// waiting to happen rather than a type error.
		return nil, &DimensionError{Bound: bound, Found: len(query), Model: "the query"}
	}
	_, op, err := l.dist.opClass()
	if err != nil {
		return nil, err
	}
	sql := l.q(fmt.Sprintf(`SELECT load_id, idx, source, heading, body, embed_model,
	embedding %s $1::vector AS distance
FROM {s}.loaded_chunks
WHERE embedding IS NOT NULL AND ($2::text[] IS NULL OR load_id = ANY($2::text[]))
ORDER BY embedding %s $1::vector
LIMIT $3`, op, op))
	var loads any
	if len(opts.Loads) > 0 {
		loads = opts.Loads
	}
	rows, err := l.pool.Query(ctx, sql, vectorLiteral(query), loads, k)
	if err != nil {
		return nil, fmt.Errorf("pgvector: search: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Load, &h.Chunk, &h.Source, &h.Heading, &h.Text, &h.Model, &h.Distance); err != nil {
			return nil, fmt.Errorf("pgvector: search: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Around returns the graph that surrounds what a search found: the entities
// extracted from the hit chunks, and the edges out to their neighbours.
//
// This is the reason to put a graph and its vectors in one store rather than
// two. A similarity search answers "which text is about this", and the thing an
// agent actually needs next is "what did we extract from that text, and what
// does it connect to" — which is a join in this schema and a distributed
// transaction if the graph lives somewhere else.
//
// It is keyed by load. Two loads are two graphs (see Graph), and a caller whose
// search crossed both is being told so rather than handed one merged answer.
func (l *Loader) Around(ctx context.Context, hits []Hit, depth int) (map[string]Graph, error) {
	if depth < 0 {
		return nil, fmt.Errorf("pgvector: depth %d is not a depth", depth)
	}
	byLoad := map[string][]int{}
	for _, h := range hits {
		byLoad[h.Load] = append(byLoad[h.Load], h.Chunk)
	}
	out := map[string]Graph{}
	for load, chunks := range byLoad {
		g, err := l.around(ctx, load, chunks, depth)
		if err != nil {
			return nil, err
		}
		out[load] = g
	}
	return out, nil
}

func (l *Loader) around(ctx context.Context, load string, chunks []int, depth int) (Graph, error) {
	const seedSQL = `SELECT ` + entCols + ` FROM {s}.loaded_entities
	WHERE load_id = $1 AND prov_chunk = ANY($2::int[])`
	ents, err := l.scanEntities(ctx, l.q(seedSQL), load, chunks)
	if err != nil {
		return Graph{}, err
	}
	seen := map[string]bool{}
	var frontier []string
	graph := Graph{}
	add := func(list []alchemy.Entity) {
		for _, e := range list {
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			graph.Entities = append(graph.Entities, e)
			frontier = append(frontier, e.ID)
		}
	}
	add(ents)

	edges := map[int]bool{}
	for hop := 0; hop < depth && len(frontier) > 0; hop++ {
		// Both directions in one statement. An agent asking what surrounds a
		// node does not care which way the extractor happened to write the
		// edge, and two queries would make "depth 1" mean something different
		// for a node that is only ever a target.
		const edgeSQL = `SELECT ` + relCols + ` FROM {s}.loaded_relations
	WHERE load_id = $1 AND (from_id = ANY($2::text[]) OR to_id = ANY($2::text[]))`
		rels, seqs, err := l.scanRelations(ctx, l.q(edgeSQL), load, frontier)
		if err != nil {
			return Graph{}, err
		}
		var next []string
		for i, r := range rels {
			if edges[seqs[i]] {
				continue
			}
			edges[seqs[i]] = true
			graph.Relations = append(graph.Relations, r)
			for _, id := range []string{r.From, r.To} {
				if !seen[id] {
					next = append(next, id)
				}
			}
		}
		frontier = nil
		if len(next) == 0 {
			break
		}
		// The endpoints are fetched rather than invented. A relation may name
		// an entity the result never contained — that is
		// ViolationDanglingRelation, which §7.3 delivers rather than refuses —
		// so an endpoint with no row simply does not join, and the edge stays
		// in the graph saying what it says.
		const endSQL = `SELECT ` + entCols + ` FROM {s}.loaded_entities
	WHERE load_id = $1 AND entity_id = ANY($2::text[])`
		ends, err := l.scanEntities(ctx, l.q(endSQL), load, next)
		if err != nil {
			return Graph{}, err
		}
		add(ends)
	}
	return graph, nil
}

// Graph reads one load back whole. It is the round trip that proves the store
// kept what it was given, and the export path for a buyer moving a corpus.
func (l *Loader) Graph(ctx context.Context, load string) (Graph, error) {
	ents, err := l.scanEntities(ctx, l.q(`SELECT `+entCols+` FROM {s}.loaded_entities WHERE load_id = $1 ORDER BY entity_id`), load, nil)
	if err != nil {
		return Graph{}, err
	}
	rels, _, err := l.scanRelations(ctx, l.q(`SELECT `+relCols+` FROM {s}.loaded_relations WHERE load_id = $1 ORDER BY seq`), load, nil)
	if err != nil {
		return Graph{}, err
	}
	return Graph{Entities: ents, Relations: rels}, nil
}

// scanEntities runs a query whose projection is entCols. The second argument
// is optional so one scanner serves the three shapes of entity read.
func (l *Loader) scanEntities(ctx context.Context, sql string, load string, arg any) ([]alchemy.Entity, error) {
	args := []any{load}
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := l.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("pgvector: %w", err)
	}
	defer rows.Close()
	var out []alchemy.Entity
	for rows.Next() {
		var e alchemy.Entity
		var raw *string
		dest := []any{&e.ID, &e.Type, &e.Name, &raw}
		dest = append(dest, provDest(&e.Provenance)...)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("pgvector: %w", err)
		}
		if e.Attributes, err = decodeAttrs(raw); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (l *Loader) scanRelations(ctx context.Context, sql string, load string, arg any) ([]alchemy.Relation, []int, error) {
	args := []any{load}
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := l.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("pgvector: %w", err)
	}
	defer rows.Close()
	var out []alchemy.Relation
	var seqs []int
	for rows.Next() {
		var r alchemy.Relation
		var seq int
		var raw *string
		dest := []any{&seq, &r.From, &r.To, &r.Type, &raw}
		dest = append(dest, provDest(&r.Provenance)...)
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, fmt.Errorf("pgvector: %w", err)
		}
		if r.Attributes, err = decodeAttrs(raw); err != nil {
			return nil, nil, err
		}
		out = append(out, r)
		seqs = append(seqs, seq)
	}
	return out, seqs, rows.Err()
}

// provDest is the scan target list for provCols, in the same order. It is next
// to provRow and provNames on purpose: three lists that must agree, written so
// that a field added to alchemy.Provenance breaks the build in all three.
func provDest(p *alchemy.Provenance) []any {
	return []any{
		&p.Source, &p.Chunk, &p.Producer, new(bool), &p.Model,
		&p.Ontology, &p.Chunking, &p.Confidence, &p.ReviewedBy, &p.RuleSet, &p.RuledBy,
	}
}

// decodeAttrs restores the NULL/'{}' distinction the writer preserved.
func decodeAttrs(raw *string) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(*raw), &m); err != nil {
		return nil, fmt.Errorf("pgvector: reading attributes: %w", err)
	}
	return m, nil
}
