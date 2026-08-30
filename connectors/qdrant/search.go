package qdrant

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Hit is one chunk a search found.
type Hit struct {
	// Load is which import it came from, and it is on the hit rather than
	// implied because a collection holding a nightly re-import holds the same
	// source text twice and a reader has to be able to tell them apart.
	Load  string
	Chunk int
	// Score is Qdrant's, in the metric the collection was built for. It is not
	// converted into a similarity, because the conversion differs per metric
	// and a number that silently means three things is worse than a raw one
	// the caller can look up.
	Score   float64
	Source  string
	Heading string
	Text    string
	// Model is the embedding model that produced this chunk's vector.
	Model string
	// EntityIDs and EntityNames are what was extracted from this chunk. They
	// are on the hit because this store cannot join: a search that answered
	// with text alone would make "and what did we learn from it" a second
	// request for every result, which at k=20 is twenty round trips to answer
	// one question.
	EntityIDs   []string
	EntityNames []string
}

// Search returns the k chunks nearest a query embedding, among the records the
// filter admits.
//
// It is the first of the three questions this connector exists to answer, and
// the only one that uses the vector at all. The filter is applied by the
// server rather than after the fact, which is the difference between "the 10
// nearest chunks, of which 2 are from the file you asked about" and "the 10
// nearest chunks from that file".
func (l *Loader) Search(ctx context.Context, query []float32, k int, f Filter) ([]Hit, error) {
	if len(query) == 0 {
		return nil, fmt.Errorf("qdrant: an empty query vector matches nothing")
	}
	if k <= 0 {
		return nil, fmt.Errorf("qdrant: k = %d is not a number of results", k)
	}
	have, err := l.CollectionDimension(ctx)
	if err != nil {
		return nil, err
	}
	if have == 0 {
		return nil, fmt.Errorf("qdrant: collection %q holds no embeddings, so there is nothing to search; "+
			"it was created from a result that carried no vectors, and Qdrant cannot add a vector to a collection "+
			"that exists", l.collection)
	}
	if have != len(query) {
		// Qdrant would refuse this too, with a message about dimensions.
		// Saying it here says it in the buyer's terms: the query came from a
		// different embedding model than the corpus, which is a wrong answer
		// waiting to happen rather than a type error.
		return nil, &DimensionError{Collection: l.collection, Have: have, Want: len(query), Model: "the query"}
	}
	// Only chunks have the vector, so this narrowing is not strictly needed
	// today — a vectorless point cannot be a nearest neighbour. It is here
	// because that is a property of the current model rather than of Qdrant,
	// and a search that started returning entities the day something else
	// gained a vector would be a very quiet regression.
	f.Kinds = []string{string(kindChunk)}
	flt, err := l.resolve(ctx, f)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"query": query, "using": vectorName, "limit": k,
		"with_payload": true, "filter": flt,
	}
	var res struct {
		Points []struct {
			Score   float64        `json:"score"`
			Payload map[string]any `json:"payload"`
		} `json:"points"`
	}
	if err := l.call(ctx, http.MethodPost, l.path("/points/query"), body, &res); err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(res.Points))
	for _, p := range res.Points {
		h := Hit{
			Load: str(p.Payload[keyLoad]), Chunk: num(p.Payload[keyChunkIndex]), Score: p.Score,
			Source: str(p.Payload[keySource]), Heading: str(p.Payload[keyHeading]),
			Text: str(p.Payload[keyText]), Model: str(p.Payload[keyEmbedModel]),
		}
		for _, v := range asSlice(p.Payload[keyChunkEntity]) {
			h.EntityIDs = append(h.EntityIDs, str(v))
		}
		for _, v := range asSlice(p.Payload[keyChunkEntityNames]) {
			h.EntityNames = append(h.EntityNames, str(v))
		}
		out = append(out, h)
	}
	return out, nil
}

// Graph is a piece of the store read back as the types alchemy returned.
//
// It holds one load's records and never two. That is the rule the fingerprint
// enforces on the way in, applied on the way out: Entity.ID is stable within
// one result and says nothing across runs, so entities from two loads that
// share an ID are not the same entity and a Graph that put them in one slice
// would be asserting that they were.
type Graph struct {
	Entities  []alchemy.Entity
	Relations []alchemy.Relation
}

// Around returns the graph that surrounds what a search found: the entities
// extracted from the hit chunks, and the edges out to their neighbours.
//
// This is the second question, and it is the reason to put a graph and its
// vectors in one store rather than two. A similarity search answers "which
// text is about this"; what an agent needs next is "what did we extract from
// that text, and what does it connect to" — which is two requests here and a
// distributed join if the graph lives somewhere else.
//
// It is also where this store's honest limit lives. Each hop is a round trip:
// depth 3 is six requests, depth 10 is twenty, and there is no query that asks
// about a path rather than a neighbourhood. A graph database walks this in its
// storage engine and can answer "is there any route from A to B"; this cannot,
// at any depth, and a buyer who needs that question answered needs the other
// connector. Load says as much in Loaded.Lost, and this is the method the
// sentence is about.
//
// It is keyed by load for the same reason Graph is: a search that crossed two
// imports is being told so rather than handed one merged answer.
func (l *Loader) Around(ctx context.Context, hits []Hit, depth int) (map[string]Graph, error) {
	if depth < 0 {
		return nil, fmt.Errorf("qdrant: depth %d is not a depth", depth)
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
	seed, err := l.Records(ctx, Filter{Loads: []string{load}, Kinds: []string{string(kindEntity)}, Chunks: chunks}, 0)
	if err != nil {
		return Graph{}, err
	}
	var g Graph
	seen := map[string]bool{}
	var frontier []string
	add := func(list []alchemy.Entity) {
		for _, e := range list {
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			g.Entities = append(g.Entities, e)
			frontier = append(frontier, e.ID)
		}
	}
	add(seed.Entities)

	edges := map[string]bool{}
	for hop := 0; hop < depth && len(frontier) > 0; hop++ {
		rels, err := l.edgesTouching(ctx, load, frontier)
		if err != nil {
			return Graph{}, err
		}
		var next []string
		for _, r := range rels {
			key := relationKey(0, r)
			if edges[key] {
				continue
			}
			edges[key] = true
			g.Relations = append(g.Relations, r)
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
		// The endpoints are fetched rather than invented from the names
		// denormalised on the edge. A relation may name an entity the result
		// never contained — ViolationDanglingRelation, which §7.3 delivers
		// rather than refuses — so an endpoint with no point of its own simply
		// does not appear, and the edge stays in the graph saying what it says.
		ends, err := l.Records(ctx, Filter{
			Loads: []string{load}, Kinds: []string{string(kindEntity)}, Entities: next,
		}, 0)
		if err != nil {
			return Graph{}, err
		}
		add(ends.Entities)
	}
	sort.Slice(g.Entities, func(i, j int) bool { return g.Entities[i].ID < g.Entities[j].ID })
	sort.Slice(g.Relations, func(i, j int) bool { return relLess(g.Relations[i], g.Relations[j]) })
	return g, nil
}

// edgesTouching is one hop: every edge with one of these ids at either end.
//
// Both directions in one request, deliberately. An agent asking what surrounds
// a node does not care which way the extractor happened to write the edge, and
// two requests would make "depth 1" mean something different for a node that
// is only ever a target. Qdrant expresses that as a nested should inside the
// must, which is the one place this package builds a filter by hand rather
// than through Filter — Filter is a list of ands, and this is an or.
func (l *Loader) edgesTouching(ctx context.Context, load string, ids []string) ([]alchemy.Relation, error) {
	flt := map[string]any{"must": []any{
		match(keyKind, string(kindRelation)),
		match(keyLoad, load),
		map[string]any{"should": []map[string]any{
			matchAny(keyRelFrom, ids),
			matchAny(keyRelTo, ids),
		}},
	}}
	pts, err := l.scroll(ctx, flt, 0)
	if err != nil {
		return nil, err
	}
	out := make([]alchemy.Relation, 0, len(pts))
	for _, p := range pts {
		out = append(out, readRelation(p.Payload))
	}
	return out, nil
}
