package qdrant

import (
	"fmt"
	"strconv"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// batchOf is one group of points and the name a report or a failure calls it
// by. Points are grouped by kind rather than thrown into one slice so that a
// load that dies halfway can say which kind it died in, which is the first
// thing an operator asks.
type batchOf struct {
	kind   kind
	points []point
}

// The builders below take one batch rather than a whole result, because that
// is what the envelope hands them (pkg/sink): §8.4 pages a large graph over the
// wire precisely because it does not fit in one message, and a store that then
// materialised it to build points would have undone the paging.
//
// Two of them take an index the tx accumulates, and that is this store's own
// cost rather than the interface's. A vector store has no joins, so anything a
// reader needs beside a record is copied onto it at write time: an edge carries
// its endpoints' names and a chunk carries the ids of the entities extracted
// from it. Both require having seen the entities, which is exactly why sink.Tx
// makes "entities before the relations that name them" a contract — the index
// is names and ids, not the graph, and it is the smallest thing that answers
// the question.

// endpoints is what an edge and a chunk need to know about entities that have
// already been written.
type endpoints struct {
	byID    map[string]alchemy.Entity
	byChunk map[int][]alchemy.Entity
}

func newEndpoints() *endpoints {
	return &endpoints{byID: map[string]alchemy.Entity{}, byChunk: map[int][]alchemy.Entity{}}
}

func (e *endpoints) add(batch []alchemy.Entity) {
	for _, en := range batch {
		e.byID[en.ID] = en
		e.byChunk[en.Provenance.Chunk] = append(e.byChunk[en.Provenance.Chunk], en)
	}
}

func entityPoints(loadID, fp string, batch []alchemy.Entity) batchOf {
	out := batchOf{kind: kindEntity, points: make([]point, 0, len(batch))}
	for _, e := range batch {
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
		// Only when there are any, unlike the attributes above: an absent
		// alias list and an empty one are the same claim -- nobody said this
		// goes by another name -- where an absent attribute map and an empty
		// one are not.
		if len(e.Aliases) > 0 {
			p[keyAliases] = e.Aliases
		}
		provenancePayload(e.Provenance, p)
		out.points = append(out.points, point{
			ID: pointID(fp, kindEntity, e.ID), Vector: vectorless(), Payload: p,
		})
	}
	return out
}

func relationPoints(loadID, fp string, at int, batch []alchemy.Relation, ends *endpoints) batchOf {
	out := batchOf{kind: kindRelation, points: make([]point, 0, len(batch))}
	for i, r := range batch {
		p := base(loadID, kindRelation)
		p[keyRelFrom] = r.From
		p[keyRelTo] = r.To
		p[keyType] = r.Type
		p[keyRelKey] = r.Key
		p[keyAttributes] = r.Attributes
		from, okFrom := ends.byID[r.From]
		to, okTo := ends.byID[r.To]
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
		out.points = append(out.points, point{
			ID: pointID(fp, kindRelation, relationKey(at+i, r)), Vector: vectorless(), Payload: p,
		})
	}
	return out
}

func chunkPoints(loadID, fp string, batch []sink.Chunk, ends *endpoints) batchOf {
	out := batchOf{kind: kindChunk, points: make([]point, 0, len(batch))}
	for _, c := range batch {
		p := base(loadID, kindChunk)
		p[keyChunkIndex] = c.Index
		p[keyText] = c.Text
		p[keySource] = c.Source
		p[keyStrategy] = c.Strategy
		p[keyHeading] = c.Heading
		p[keyStart] = c.Start
		p[keyEnd] = c.End
		ids := make([]string, 0, len(ends.byChunk[c.Index]))
		names := make([]string, 0, len(ends.byChunk[c.Index]))
		for _, e := range ends.byChunk[c.Index] {
			ids = append(ids, e.ID)
			names = append(names, e.Name)
		}
		p[keyChunkEntity] = ids
		p[keyChunkEntityNames] = names
		vec := vectorless()
		if c.Vector != nil {
			vec = map[string]any{vectorName: c.Vector}
			// The embedding model is on the chunk and not only on the load,
			// because alchemy.Vector carries it per vector: a result that
			// embedded two chunks with two models is a fact a reader has to be
			// able to see rather than a detail the store averages away.
			p[keyEmbedModel] = c.Model
		}
		out.points = append(out.points, point{
			ID: pointID(fp, kindChunk, strconv.Itoa(c.Index)), Vector: vec, Payload: p,
		})
	}
	return out
}

func violationPoints(loadID, fp string, batch []alchemy.Violation) batchOf {
	out := batchOf{kind: kindViolation, points: make([]point, 0, len(batch))}
	for i, v := range batch {
		p := base(loadID, kindViolation)
		p[keyViolationKind] = string(v.Kind)
		p[keySubject] = v.Subject
		p[keyDetail] = v.Detail
		provenancePayload(v.Provenance, p)
		out.points = append(out.points, point{
			// The index is in the key because two violations can be identical
			// in every field a reader cares about — the same malformed row
			// kind twice — and collapsing them would make §5's violation count
			// disagree with what is in the store.
			ID: pointID(fp, kindViolation, fmt.Sprintf("%d\x00%s\x00%s", i, v.Kind, v.Subject)), Vector: vectorless(), Payload: p,
		})
	}
	return out
}

func duplicatePoints(loadID, fp string, batch []alchemy.Duplicate) batchOf {
	out := batchOf{kind: kindDuplicate, points: make([]point, 0, len(batch))}
	for i, d := range batch {
		p := base(loadID, kindDuplicate)
		p[keySignal] = string(d.Signal)
		p[keySubject] = d.Subject
		p[keyDetail] = d.Detail
		p[keyLeft] = side(d.Left)
		p[keyRight] = side(d.Right)
		out.points = append(out.points, point{
			ID: pointID(fp, kindDuplicate, fmt.Sprintf("%d\x00%s", i, d.Subject)), Vector: vectorless(), Payload: p,
		})
	}
	return out
}

// supersessionPoints builds the claims this result makes about what is over.
//
// They are points beside the graph and nothing else happens. No entity point is
// deleted, no relation point is rewritten, and no payload of the retired record
// changes: alchemy states a retirement and does not perform one, and a store
// that performed it would let one producer remove another producer's fact by
// naming it. What the buyer gets is the claim, filterable on what it retires,
// and the decision about what to do with it.
//
// keyRetires may name a record that is in no load in this collection, which is
// the ordinary case rather than a defect -- the record being retired is usually
// in a load that finished last month. There is nothing to check and nothing to
// refuse: the id is stored as it was given, and a reader who filters on it and
// finds nothing has learned something true.
func supersessionPoints(loadID, fp string, at int, batch []alchemy.Supersession) batchOf {
	out := batchOf{kind: kindSupersession, points: make([]point, 0, len(batch))}
	for i, s := range batch {
		p := base(loadID, kindSupersession)
		p[keyRetires] = s.Retires
		p[keyReason] = s.Reason
		p[keyBy] = ref(s.By)
		// The supersession's own provenance and not the superseding record's: a
		// reviewer may retire a record a model proposed, and those are two
		// claims by two parties.
		provenancePayload(s.Provenance, p)
		out.points = append(out.points, point{
			// The position is in the key for the reason it is in a violation's:
			// one record can be retired twice in one result, by two people for
			// two reasons, and collapsing them onto one point would lose the
			// second person entirely.
			ID: pointID(fp, kindSupersession, fmt.Sprintf("%d\x00%s", at+i, s.Retires)), Vector: vectorless(), Payload: p,
		})
	}
	return out
}

// base is what every point of every kind carries: what it is, and which import
// it belongs to.
func base(loadID string, k kind) map[string]any {
	return map[string]any{keyKind: string(k), keyLoad: loadID}
}

// ref renders an alchemy.Ref, nested for the reason side() is: nobody filters
// on which record replaces a retired one, they read it once they have the
// claim in front of them. Every field goes in, because a Ref naming a relation
// carries the four its identity is a function of and a reader in a store with
// no joins cannot follow an id to go and look.
func ref(r alchemy.Ref) map[string]any {
	return map[string]any{
		keyKind: string(r.Kind), keyEntityID: r.ID, keyType: r.Type,
		keyRelFrom: r.From, keyRelTo: r.To, keyRelKey: r.Key,
	}
}

func readRef(v any) alchemy.Ref {
	m, _ := v.(map[string]any)
	return alchemy.Ref{
		Kind: alchemy.RefKind(str(m[keyKind])), ID: str(m[keyEntityID]), Type: str(m[keyType]),
		From: str(m[keyRelFrom]), To: str(m[keyRelTo]), Key: str(m[keyRelKey]),
	}
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
func lost(dim, chunks, vectors int) []string {
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
	} else if n := chunks - vectors; n > 0 {
		out = append(out, fmt.Sprintf("%d of %d chunks arrived without an embedding and are stored as text only; "+
			"a similarity search cannot reach them", n, chunks))
	}
	return out
}
