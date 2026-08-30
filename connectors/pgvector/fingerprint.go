package pgvector

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// fingerprintDomain prefixes every fingerprint so a digest computed here can
// never be mistaken for one computed over some other structure that happens to
// hash the same bytes, and so a future encoding change can be told from this
// one by bumping the number rather than silently returning different
// fingerprints under the same name. pkg/cache's Address does the same thing
// for the same reasons.
const fingerprintDomain = "alchemy/connectors/pgvector/fingerprint/1"

// Fingerprint is the content address of a whole result, and it is the answer
// to the idempotency question DESIGN.md §5 leaves open by putting "incremental
// re-import and change detection" in the second release.
//
// The crux is that alchemy.Entity.ID "is stable within one result and says
// nothing across runs". So there is no key on which two runs could be merged
// without inventing one, and inventing one is how a store silently joins two
// graphs that disagree — the same corpus re-extracted under a different
// chunking strategy (§7.1) produces the same entity IDs for a genuinely
// different graph. The unit of identity therefore has to be the whole result,
// and this is its name.
//
// Two consequences, and both are the behaviour that was wanted:
//
//   - Loading the identical result twice is a no-op. The second call finds the
//     fingerprint already complete and writes nothing, so a retried nightly
//     job does not double the graph.
//
//   - Loading a genuinely different result is a second load, side by side.
//     Nothing is merged, because nothing in the data says they are the same
//     corpus, and a store that guessed would answer a query with an edge from
//     one run and a contradicting edge from the other.
//
// It hashes the JSON encoding of the whole result — §4's "the JSON is the
// contract" taken literally. That covers every field, including ones added to
// alchemy.Result later, which is the safe direction: a new field makes a
// re-load look like a different result rather than making two different
// results look the same.
func Fingerprint(res alchemy.Result) (string, error) {
	body, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("pgvector: fingerprint: %w", err)
	}
	h := sha256.New()
	// Length-prefixed, so the domain tag cannot be absorbed into the payload
	// by a result whose encoding happens to begin with the same bytes.
	for _, part := range [][]byte{[]byte(fingerprintDomain), body} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
