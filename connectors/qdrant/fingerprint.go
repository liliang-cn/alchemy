package qdrant

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// fingerprintDomain prefixes every fingerprint so that a digest computed here
// cannot be mistaken for one computed over some other structure that happens
// to hash the same bytes, and so that a change to the encoding can be told
// from this one by bumping the number rather than silently returning different
// fingerprints under the same name.
const fingerprintDomain = "alchemy/connectors/qdrant/fingerprint/1"

// Fingerprint is the content address of a whole result, and in this connector
// it is not merely how a re-load is recognised — it is part of every point ID.
//
// The reason it has to be is Qdrant's own constraint. A point ID is a UUID or
// an unsigned integer, so an ID is always derived from something, and the
// thing it is derived from decides what an upsert does. Derive it from the
// record alone and two different results that both call something "e1" land on
// one point, the second silently overwriting the first — worse than the
// equivalent mistake in a graph store, where at least the mess is visible.
// alchemy.Entity.ID "is stable within one result and says nothing across
// runs", which is exactly the licence to do that. So the load's identity is in
// the ID, and the load's identity is this.
//
// Two consequences, both wanted:
//
//   - Loading the identical result twice is a no-op. Every point ID is the
//     same and every payload is the same, so the upsert is a rewrite of what
//     is already there rather than a doubling, and the marker check short-
//     circuits it before even that.
//
//   - Loading a genuinely different result is a second load, side by side.
//     Nothing merges, because nothing in the data says the two are the same
//     corpus and a store that guessed would answer one query with an edge from
//     each.
//
// It hashes the JSON encoding of the whole result — §4's "the JSON is the
// contract" taken literally — which covers fields added to alchemy.Result
// later. That is the safe direction to be wrong in: a new field makes a
// re-load look like a new result, where ignoring it would make two different
// results look like one.
func Fingerprint(res alchemy.Result) (string, error) {
	body, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("qdrant: fingerprint: %w", err)
	}
	h := sha256.New()
	// Length-prefixed, so the domain tag cannot be absorbed into the payload by
	// a result whose encoding happens to begin with the same bytes.
	for _, part := range [][]byte{[]byte(fingerprintDomain), body} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dimensionOf is the check a result has to pass before any of it is written:
// one dimension across the whole result, no empty vectors, and every vector
// naming a chunk that exists.
//
// All three are things alchemy.Result does not promise. Vector.Values is a
// slice per vector with nothing tying two of them together, and Vector.Chunk
// is an index into a slice the type does not require to be present — so the
// connector computes the result's dimension rather than reading it, and says
// so when it cannot. A collection is created at one width and cannot be
// changed, which makes this the last moment the question can be asked.
func dimensionOf(res alchemy.Result) (int, string, error) {
	chunks := make(map[int]bool, len(res.Chunks))
	for _, c := range res.Chunks {
		chunks[c.Index] = true
	}
	dim, model := 0, ""
	for _, v := range res.Vectors {
		if len(v.Values) == 0 {
			return 0, "", fmt.Errorf("qdrant: the vector for chunk %d is empty; "+
				"an embedding nobody can search is not one worth storing", v.Chunk)
		}
		if !chunks[v.Chunk] {
			return 0, "", fmt.Errorf("qdrant: a vector names chunk %d and the result has no such chunk; "+
				"storing it would leave an embedding with no text behind it", v.Chunk)
		}
		if dim == 0 {
			dim, model = len(v.Values), v.Model
			continue
		}
		if len(v.Values) != dim {
			return 0, "", &DimensionError{
				Have: dim, Want: len(v.Values), Model: v.Model,
				Where: fmt.Sprintf("chunk %d", v.Chunk),
			}
		}
	}
	return dim, model, nil
}
