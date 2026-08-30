package qdrant

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// kind is what a point is. It exists because this connector puts every sort of
// record a result contains into one collection, and a store that could not say
// which is which would be a bag of payloads.
//
// One collection rather than four is the decision behind it. Qdrant charges
// nothing for a payload-indexed equality filter, and a buyer who wants "the
// chunks nearest this query, and the entities that came out of them" gets one
// endpoint, one delete, and one set of point IDs that cannot drift apart. Four
// collections would have made "delete this load" four calls that can half-fail
// and "what did we extract from that text" a client-side join across services.
type kind string

const (
	// kindChunk is the only kind that carries a vector, because it is the only
	// kind alchemy.Result has a vector for.
	kindChunk kind = "chunk"
	// kindEntity is a node of the graph, stored as a point with no vector.
	kindEntity kind = "entity"
	// kindRelation is an edge, also a vectorless point. See the package
	// comment for what that costs and why it is still the right answer.
	kindRelation kind = "relation"
	// kindViolation is one of §5's findings, kept beside the graph it is
	// about.
	kindViolation kind = "violation"
	// kindDuplicate is a pair of nodes that may be one node.
	kindDuplicate kind = "duplicate"
	// kindLoad is the marker point: one per load, carrying the counts, the
	// findings that are read whole, and the flag that says whether the load
	// finished. It is a point rather than a collection alias or a name
	// convention because Qdrant has nowhere else to put it — there is no
	// catalog, no table of tables, and a fact that is not in a payload is a
	// fact this store cannot hold.
	kindLoad kind = "load"
)

// pointIDDomain prefixes every derived ID so that a UUID computed here can
// never collide with one computed by some other scheme that happens to hash
// the same bytes, and so that a future change to the derivation can be told
// from this one by bumping the number. pkg/cache's Address and the pgvector
// connector's fingerprint domain do the same thing for the same reason.
const pointIDDomain = "alchemy/connectors/qdrant/point/1"

// pointID is the whole of this connector's answer to idempotency.
//
// Qdrant accepts an unsigned integer or a UUID and nothing else, so a natural
// key — "entity SuperAI of load ld_9f21" — cannot be the ID; it has to be
// hashed into one of those two shapes. That constraint turns out to be
// convenient rather than annoying, because an upsert keyed on a derived ID is
// idempotent by construction: loading the same result twice writes the same
// point IDs with the same payloads, which is one write and not two rows. There
// is no read-modify-write and no unique index to race on.
//
// The load's fingerprint is in the derivation, and that is the load-bearing
// part. alchemy.Entity.ID "is stable within one result and says nothing across
// runs", so two runs that both call something "e1" are two different things;
// an ID derived from the record alone would upsert the second over the first
// and leave a store quietly holding one graph under two runs' provenance. With
// the fingerprint in, two genuinely different results cannot touch each
// other's points, and the identical result cannot help but land on its own.
//
// The result is formatted as a version 8 UUID. RFC 9562 reserves version 8 for
// exactly this — an application-specific, deterministically derived UUID — so
// stamping 4 or 5 on a SHA-256 digest would be claiming a derivation this is
// not, to no benefit.
func pointID(fingerprint string, k kind, key string) string {
	h := sha256.New()
	// Length-prefixed, so that a key ending where the next field begins cannot
	// be rearranged into another record's bytes.
	for _, part := range []string{pointIDDomain, fingerprint, string(k), key} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write([]byte(part))
	}
	sum := h.Sum(nil)[:16]
	sum[6] = (sum[6] & 0x0f) | 0x80 // version 8: custom, per RFC 9562
	sum[8] = (sum[8] & 0x3f) | 0x80 // variant 10: RFC 4122/9562
	s := hex.EncodeToString(sum)
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// The payload keys. They are constants in one block because a payload index, a
// filter and a writer that spell the same field three ways is a bug that
// presents as "the query returns nothing" with no error anywhere.
//
// Everything alchemy knows about a record is a top-level key, and everything
// the source said about it is nested under keyAttributes. That is the one rule
// that makes a collision unreachable: a model that invents an attribute called
// "type" or "prov_source" cannot overwrite this connector's fields, because
// its attributes are never at this level. The Neo4j connector cannot do that —
// Neo4j properties are flat, so it needs a reserved prefix and a validator to
// reject attributes that stray into it — and it is the one place where Qdrant's
// document-shaped payload is simply a better fit for alchemy.Result than a
// property graph is.
const (
	keyKind = "kind"
	// keyLoad scopes every point to one import. It is on every point of every
	// kind because a collection holds many loads and no query in this package
	// is allowed to answer from two of them without being asked to.
	keyLoad = "load"

	keyEntityID = "entity_id"
	keyType     = "type"
	keyName     = "name"
	// keyAttributes is the source's own words, nested, verbatim.
	keyAttributes = "attributes"

	keyRelFrom = "rel_from"
	keyRelTo   = "rel_to"
	// keyRelKey is alchemy.Relation.Key — the producer's own name for the
	// edge, which is what makes two parallel edges two edges rather than one
	// edge described twice. It is stored even though this connector's point
	// identity does not need it, because a buyer looking at five edges between
	// one pair of nodes has no other way to tell which foreign key each came
	// from.
	keyRelKey = "rel_key"
	// The endpoints' names and types, denormalised onto the edge. A store with
	// no joins that held only ids would make every edge unreadable without a
	// second round trip per endpoint; this is the compensation, and its cost
	// is that a name is copied onto every edge that touches the node.
	keyRelFromName = "rel_from_name"
	keyRelFromType = "rel_from_type"
	keyRelToName   = "rel_to_name"
	keyRelToType   = "rel_to_type"
	// keyRelDangling marks an edge whose endpoint is not in this result. That
	// is ViolationDanglingRelation, which §7.3 delivers rather than refuses, so
	// the edge is stored — and marked, because a reader who joins on it will
	// otherwise think the store lost a node.
	keyRelDangling = "rel_dangling"

	keyChunkIndex  = "chunk_index"
	keyText        = "text"
	keySource      = "source"
	keyStrategy    = "strategy"
	keyHeading     = "heading"
	keyStart       = "start"
	keyEnd         = "end"
	keyEmbedModel  = "embed_model"
	keyChunkEntity = "entity_ids"
	// keyChunkEntityNames is what makes a search result legible on its own: a
	// hit that says "and these are the things we extracted from it" without a
	// second request is the difference between a search endpoint and a search
	// product.
	keyChunkEntityNames = "entity_names"

	keyViolationKind = "violation_kind"
	keySubject       = "subject"
	keyDetail        = "detail"
	keySignal        = "signal"
	keyLeft          = "left"
	keyRight         = "right"

	keyProvSource        = "prov_source"
	keyProvChunk         = "prov_chunk"
	keyProvProducer      = "prov_producer"
	keyProvDeterministic = "prov_deterministic"
	keyProvModel         = "prov_model"
	keyProvOntology      = "prov_ontology"
	keyProvChunking      = "prov_chunking"
	keyProvConfidence    = "prov_confidence"
	keyProvReviewedBy    = "prov_reviewed_by"
	keyProvRuleSet       = "prov_rule_set"
	keyProvRuledBy       = "prov_ruled_by"

	// The load marker's own keys. They are on exactly one point per load, and
	// they are the whole of what this store has instead of a catalog: which
	// graph this is, whether it finished, and §5's numbers needed to distrust
	// it.
	keyFingerprint = "fingerprint"
	keyComplete    = "complete"
	keyStartedAt   = "started_at"
	keyFinishedAt  = "finished_at"
	keyDimension   = "dimension"
	keyPoints      = "points"
	keyCounts      = "counts"
	keyConflicts   = "conflicts"
	keyGuesses     = "guesses"
	keyUnread      = "unread"
	keyRuleSets    = "rule_sets"
	keyModelCalls  = "model_calls"
	// keyLost is what this store could not keep about the graph. It is written
	// into the collection and not only returned from Load, so that the buyer
	// who finds the collection a year later — and never saw the return value —
	// is told the same thing.
	keyLost = "lost"
)

// provenancePayload flattens a Provenance onto whatever it describes.
//
// It is flat rather than nested under a "provenance" object for one reason: a
// Qdrant payload index is created on a path, and a filter on a nested path
// costs the same to write but makes every index name a compound. Flat keys
// under one prefix keep §5b's "filter to the half that was guessed" a
// one-field filter, which is the query this shape exists to make cheap.
//
// Empty optional fields are omitted rather than written as "". "This record
// has no model" and "this record's model is the empty string" are different
// claims, and Qdrant's is_empty condition can ask about the first — so
// omitting loses nothing and writing "" would make every filter carry an
// exclusion for a value that never meant anything.
func provenancePayload(p alchemy.Provenance, into map[string]any) map[string]any {
	into[keyProvSource] = p.Source
	// Kept even at -1: pkg/alchemy defines -1 as "the producer did not work in
	// chunks", which is a fact about the record rather than a missing value.
	into[keyProvChunk] = p.Chunk
	into[keyProvProducer] = string(p.Producer)
	// Computed here rather than left to the buyer. §5b promises a person "can
	// filter to the half that was guessed"; making them enumerate the producer
	// names to do it hands them a rule the core module owns and may extend.
	into[keyProvDeterministic] = p.Producer.Deterministic()
	for k, v := range map[string]string{
		keyProvModel:      p.Model,
		keyProvOntology:   p.Ontology,
		keyProvChunking:   p.Chunking,
		keyProvReviewedBy: p.ReviewedBy,
		keyProvRuleSet:    p.RuleSet,
		keyProvRuledBy:    p.RuledBy,
	} {
		if v != "" {
			into[k] = v
		}
	}
	if p.Confidence != 0 {
		into[keyProvConfidence] = p.Confidence
	}
	return into
}

// readProvenance is provenancePayload backwards, and it exists so that a
// record read out of the store is an alchemy.Provenance again rather than a
// map a caller has to know the key names of.
func readProvenance(p map[string]any) alchemy.Provenance {
	return alchemy.Provenance{
		Source:     str(p[keyProvSource]),
		Chunk:      num(p[keyProvChunk]),
		Producer:   alchemy.Producer(str(p[keyProvProducer])),
		Model:      str(p[keyProvModel]),
		Ontology:   str(p[keyProvOntology]),
		Chunking:   str(p[keyProvChunking]),
		Confidence: float(p[keyProvConfidence]),
		ReviewedBy: str(p[keyProvReviewedBy]),
		RuleSet:    str(p[keyProvRuleSet]),
		RuledBy:    str(p[keyProvRuledBy]),
	}
}

// point is one row on the wire.
type point struct {
	ID      string         `json:"id"`
	Vector  map[string]any `json:"vector"`
	Payload map[string]any `json:"payload"`
}

// vectorless is the empty vector map every non-chunk point carries.
//
// It is the shape of the central decision: alchemy.Result has vectors for
// chunks and for nothing else, so an entity point is a point with no
// embedding. Qdrant accepts that, and what it costs is stated where a buyer
// will read it — such a point is retrievable and filterable, and no similarity
// search will ever return it.
func vectorless() map[string]any { return map[string]any{} }

func str(v any) string {
	s, _ := v.(string)
	return s
}

// num reads an integer out of a payload. JSON has one number type, so
// everything arrives as a float64 and an int that went in as 42 comes back as
// 42.0; this is the one place that is dealt with.
func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func float(v any) float64 {
	f, _ := v.(float64)
	return f
}
