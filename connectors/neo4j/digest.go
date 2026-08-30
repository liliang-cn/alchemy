package neo4j

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The content address of a load used to be here, and it is pkg/sink's now.
//
// It was the identity of everything a Load writes: the same digest under one
// name is a replay and must converge, a different one is the caller telling the
// store two different things about one import. Four connectors each wrote one,
// and §4.1 puts result identity above the interface for exactly that reason —
// so this file keeps only the part that is this store's, which is what makes
// two records one edge in a property graph.
//
// The two properties this one had and the shared one keeps: it is
// order-independent, because a result reassembled from §8.4's pages is the same
// load; and it covers provenance as well as content, because the same edges
// re-extracted by a different model are not the same import and a run that
// replayed as unchanged would leave the graph claiming the first model's word
// for the second model's work.

// relationKey is the identity of one edge, and it is the identity of the
// *assertion* rather than of the pair it connects.
//
// Two chunks that both say "SuperAI USES CortexDB" are two pieces of evidence
// with two provenances. Merging them onto one edge keyed by (from, type, to)
// would leave the second one unable to name its producer, its chunk or its
// model — which is exactly the guarantee §5b sells. So the key covers the
// attributes and the provenance too: replaying the same result is a no-op, and
// two genuinely separate assertions remain two edges a buyer can count.
//
// alchemy.Relation.Key is in it as well, and its absence was a bug rather than
// a position. The other fields happened to separate the two
// NODE_CONNECTIONS foreign keys only because those two edges also disagree
// about their columns; two parallel edges that agree about everything the
// producer said and differ only in which of them they are — which is what the
// field means — collapsed onto one, silently, in the exact case the field was
// added for.
//
// It stays the identity of the assertion rather than becoming
// alchemy.Relation.Identity, and the difference is Neo4j's rather than a
// preference. Identity says two records asserting one edge are one edge; a
// Neo4j relationship holds one set of properties, so merging them would leave
// the second assertion unable to name its producer, its chunk or its model.
// Where the store can hold both claims it holds both, and Identity is what a
// reader groups them by afterwards.
func relationKey(r alchemy.Relation) string {
	h := sha256.Sum256([]byte(r.From + "\x00" + r.To + "\x00" + r.Type + "\x00" + r.Key + "\x00" + canonical(r.Attributes) + "\x00" + canonical(r.Provenance)))
	return hex.EncodeToString(h[:])
}

// canonical renders a value for hashing. json.Marshal sorts map keys, which is
// what makes an Attributes map hash the same twice; a value that cannot be
// marshalled is rendered with its Go syntax rather than dropped, so that two
// records differing only in an unmarshallable field still differ here.
func canonical(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}
