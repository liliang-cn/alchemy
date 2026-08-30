package qdrant

import (
	"fmt"
	"strconv"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// batchOf is one group of points and the name a report or a failure calls it
// by. Points are grouped by kind rather than thrown into one slice so that a
// load that dies halfway can say which kind it died in, which is the first
// thing an operator asks.
type batchOf struct {
	kind   kind
	points []point
}

// build turns a result into every point it becomes.
//
// It is one pass with no I/O in it, which is deliberate: everything that can
// be wrong with the shape of a result is discoverable before the first
// request, and a load that is going to be refused should be refused while the
// store still looks exactly as the caller left it.
func build(res alchemy.Result, fp, loadID string) []batchOf {
	// The two lookups that pay for this store's lack of joins. A vector store
	// cannot follow an id at read time, so anything a reader needs beside a
	// record has to be copied onto it at write time.
	names := make(map[string]alchemy.Entity, len(res.Entities))
	for _, e := range res.Entities {
		names[e.ID] = e
	}
	byChunk := map[int][]alchemy.Entity{}
	for _, e := range res.Entities {
		byChunk[e.Provenance.Chunk] = append(byChunk[e.Provenance.Chunk], e)
	}
	vectors := make(map[int]alchemy.Vector, len(res.Vectors))
	for _, v := range res.Vectors {
		vectors[v.Chunk] = v
	}

	out := []batchOf{
		{kind: kindEntity, points: make([]point, 0, len(res.Entities))},
		{kind: kindRelation, points: make([]point, 0, len(res.Relations))},
		{kind: kindChunk, points: make([]point, 0, len(res.Chunks))},
		{kind: kindViolation, points: make([]point, 0, len(res.Violations))},
		{kind: kindDuplicate, points: make([]point, 0, len(res.Duplicates))},
	}

	for _, e := range res.Entities {
		p := base(loadID, kindEntity)
		p[keyEntityID] = e.ID
		p[keyType] = e.Type
		p[keyName] = e.Name
		// Attributes are nested rather than merged, so a source that calls a
		// column "type" or "prov_source" cannot overwrite what this connector
		// knows about the record. Written even when empty is nil, because
		// Qdrant's is_empty can then tell "the source said nothing" from "the
		// source said {}" — a distinction the JSON contract makes and a store
		// that flattened both to nothing would erase.
		p[keyAttributes] = e.Attributes
		provenancePayload(e.Provenance, p)
		out[0].points = append(out[0].points, point{
			ID: pointID(fp, kindEntity, e.ID), Vector: vectorless(), Payload: p,
		})
	}

	for i, r := range res.Relations {
		p := base(loadID, kindRelation)
		p[keyRelFrom] = r.From
		p[keyRelTo] = r.To
		p[keyType] = r.Type
		p[keyRelKey] = r.Key
		p[keyAttributes] = r.Attributes
		from, okFrom := names[r.From]
		to, okTo := names[r.To]
		p[keyRelFromName], p[keyRelFromType] = from.Name, from.Type
		p[keyRelToName], p[keyRelToType] = to.Name, to.Type
		// A relation naming an entity the result does not contain is
		// ViolationDanglingRelation, and §7.3 keeps the graph: "attributable,
		// excludable, and the rest of the graph is usable without it". So the
		// edge is written, and it is written marked — a reader who found it
		// with an empty endpoint name would otherwise conclude the store had
		// dropped a node.
		p[keyRelDangling] = !okFrom || !okTo
		provenancePayload(r.Provenance, p)
		out[1].points = append(out[1].points, point{
			ID: pointID(fp, kindRelation, relationKey(i, r)), Vector: vectorless(), Payload: p,
		})
	}

	for _, c := range res.Chunks {
		p := base(loadID, kindChunk)
		p[keyChunkIndex] = c.Index
		p[keyText] = c.Text
		p[keySource] = c.Source
		p[keyStrategy] = c.Strategy
		p[keyHeading] = c.Heading
		p[keyStart] = c.Start
		p[keyEnd] = c.End
		ids := make([]string, 0, len(byChunk[c.Index]))
		names := make([]string, 0, len(byChunk[c.Index]))
		for _, e := range byChunk[c.Index] {
			ids = append(ids, e.ID)
			names = append(names, e.Name)
		}
		p[keyChunkEntity] = ids
		p[keyChunkEntityNames] = names
		vec := vectorless()
		if v, ok := vectors[c.Index]; ok {
			vec = map[string]any{vectorName: v.Values}
			// The embedding model is on the chunk and not only on the load,
			// because alchemy.Vector carries it per vector: a result that
			// embedded two chunks with two models is a fact a reader has to be
			// able to see rather than a detail the store averages away.
			p[keyEmbedModel] = v.Model
		}
		out[2].points = append(out[2].points, point{
			ID: pointID(fp, kindChunk, strconv.Itoa(c.Index)), Vector: vec, Payload: p,
		})
	}

	for i, v := range res.Violations {
		p := base(loadID, kindViolation)
		p[keyViolationKind] = string(v.Kind)
		p[keySubject] = v.Subject
		p[keyDetail] = v.Detail
		provenancePayload(v.Provenance, p)
		out[3].points = append(out[3].points, point{
			// The index is in the key because two violations can be identical
			// in every field a reader cares about — the same malformed row
			// kind twice — and collapsing them would make §5's violation count
			// disagree with what is in the store.
			ID: pointID(fp, kindViolation, fmt.Sprintf("%d\x00%s\x00%s", i, v.Kind, v.Subject)), Vector: vectorless(), Payload: p,
		})
	}

	for i, d := range res.Duplicates {
		p := base(loadID, kindDuplicate)
		p[keySignal] = string(d.Signal)
		p[keySubject] = d.Subject
		p[keyDetail] = d.Detail
		p[keyLeft] = side(d.Left)
		p[keyRight] = side(d.Right)
		out[4].points = append(out[4].points, point{
			ID: pointID(fp, kindDuplicate, fmt.Sprintf("%d\x00%s", i, d.Subject)), Vector: vectorless(), Payload: p,
		})
	}
	return out
}

// base is what every point of every kind carries: what it is, and which import
// it belongs to.
func base(loadID string, k kind) map[string]any {
	return map[string]any{keyKind: string(k), keyLoad: loadID}
}

// side renders one half of a Duplicate, provenance and all. It is nested
// rather than flattened because nobody filters on it: a duplicate pair is read
// whole by a person deciding whether two nodes are one thing.
func side(s alchemy.DuplicateSide) map[string]any {
	return map[string]any{
		keyEntityID: s.ID, keyType: s.Type, keyName: s.Name,
		"provenance": provenancePayload(s.Provenance, map[string]any{}),
	}
}

// relationKey is the identity of one edge within one result, and it is the
// one place this connector had to invent something alchemy.Result does not
// carry.
//
// An Entity has an ID. A Relation has no ID — it has From, To, Type and an
// optional Key, and the Key is documented as optional precisely because a
// model reading prose "cannot say whether the edge it just proposed is the one
// it proposed two chunks ago". So identity has to fall back to position in
// Result.Relations, and the Key is folded in ahead of it so that a producer
// which does know its edges apart — a DDL reader with a constraint name — has
// that name in the identity rather than only in the payload.
//
// Position is safe here for a reason particular to this connector: the point
// ID also carries the result's fingerprint, which is a digest of the whole
// encoded result and therefore already changes if the relations arrive in
// another order. A reordered result is a different load either way, so nothing
// is made worse by keying on the order within it.
func relationKey(i int, r alchemy.Relation) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", r.From, r.To, r.Type, r.Key, i)
}

// lost is what this store could not keep about the graph it was just given.
//
// It is returned from Load and written into the load marker rather than left
// in a doc comment, because the failure mode this connector has and the other
// two do not is a buyer believing they loaded a graph. Every line here is a
// question the store will answer wrongly or not at all, said before they ask
// it.
func lost(res alchemy.Result, dim int) []string {
	out := []string{
		"traversal is not stored as traversal: a relation is a point with two ids in its payload, not an edge, so one hop " +
			"is an indexed lookup and n hops are n round trips, " +
			"and there is no path, variable-length or shortest-path query at all — a graph loaded here is its records, indexed, not a graph",
		"entities and relations carry no vector, because alchemy.Result carries vectors only for chunks: " +
			"they are filterable and retrievable, and no similarity search will ever return one",
	}
	if dim == 0 {
		out = append(out, "this result carried no embeddings, so the collection was created with no embedding vector; "+
			"Qdrant cannot add one to a collection that exists, so a later result with vectors will need a new collection")
	} else if n := len(res.Chunks) - len(res.Vectors); n > 0 {
		out = append(out, fmt.Sprintf("%d of %d chunks arrived without an embedding and are stored as text only; "+
			"a similarity search cannot reach them", n, len(res.Chunks)))
	}
	return out
}
