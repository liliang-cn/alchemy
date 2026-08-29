// Package cache is the content-addressed store DESIGN.md §8.2 requires.
//
// §7.2 decided cost is not optimised for, and §8.2 draws the line that decision
// does not cross: "Declining to use a cheaper model is a product position;
// paying twice for the identical call after a crash is a bug." So an extraction
// result is addressed by everything that would change it — the chunk text, the
// model, the ontology version and the prompt version — and a job resumed after
// a lease expiry (§8.3) re-buys only the chunks that had not finished.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Key is everything that would change the answer. Anything absent from this
// struct is something the cache is asserting the model does not read.
type Key struct {
	// Chunk is the chunk text itself, not its index. An index is a position in
	// one job's chunking; the text is what the model was actually shown, and
	// two jobs that chunk differently (§7.1) must not share an answer just
	// because both called something "chunk 14".
	Chunk string
	// Model is the model that would be asked.
	Model string
	// Ontology is the vocabulary version the extraction is constrained by.
	Ontology string
	// Prompt is the prompt version, e.g. extract.PromptVersion.
	Prompt string
}

// addressDomain prefixes every address so that a digest computed here can
// never be mistaken for one computed over some other structure that happens to
// hash the same bytes, and so that a future encoding change can be told from
// this one by bumping the number rather than by silently returning different
// addresses under the same name.
const addressDomain = "alchemy/cache/key/1"

// Address is the content address of the key: SHA-256 over a length-prefixed
// encoding of the fields, hex-encoded.
//
// Two choices, both load-bearing:
//
// The framing is length-prefixed rather than concatenated or separated by a
// delimiter. hash(chunk+model+…) cannot tell {Chunk:"ab", Model:"c"} from
// {Chunk:"a", Model:"bc"} — the same bytes reach the hash — so two different
// calls would share an address and a resumed job would serve an answer that
// was produced for a different model. A delimiter only moves the problem to
// whatever byte was chosen, and chunk text is arbitrary document content that
// contains every byte sooner or later. A length prefix is unforgeable because
// the boundaries are stated, not inferred.
//
// The digest is SHA-256, from crypto/sha256, and the lengths are written
// big-endian through encoding/binary. Both are byte-exact specifications, so
// the address a node computes on arm64 today equals the one another node
// computes on amd64 tomorrow — which is the entire point when the cache is
// shared (§8.3). Go's built-in map hash and maphash are explicitly
// process-seeded, and a pointer or a fmt of a struct is neither stable nor
// meaningful across processes; any of them would produce a cache that misses
// everything after a restart while looking like it works.
func (k Key) Address() string {
	h := sha256.New()
	// The domain tag is written framed too, so it cannot be absorbed into the
	// first field by a Chunk that begins with the same text.
	for _, field := range []string{addressDomain, k.Chunk, k.Model, k.Ontology, k.Prompt} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(field)))
		h.Write(n[:])
		h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Entry is one chunk's extraction result: what the model call bought.
//
// Tokens travels with it because §7.2 obliges the job to report what it spent
// by model and stage. A cached chunk spent nothing this time, and a job that
// reported the cached figure again would be inventing a bill; a job that
// reported nothing at all would lose the record of what the graph cost to
// produce. Keeping the number here lets the caller decide which it is saying.
type Entry struct {
	Entities  []alchemy.Entity
	Relations []alchemy.Relation
	Tokens    int
}

// Cache is the content-addressed store.
//
// Get returns (Entry{}, false, nil) for a key that was never stored: a miss is
// not an error. An error means the cache itself failed — a shared store that
// is unreachable, a row that will not decode — and a caller must be able to
// carry on without it, because a cache is an optimisation and an optimisation
// that can fail a job is worse than no cache. The contract is therefore:
// treat any error as a miss, log it, and buy the call.
type Cache interface {
	Get(ctx context.Context, k Key) (Entry, bool, error)
	Put(ctx context.Context, k Key, e Entry) error
}
