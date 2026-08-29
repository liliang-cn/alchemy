package cache_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/cache"
	"github.com/liliang-cn/alchemy/pkg/extract"
)

// base is the key every test in this file varies one field of. Using a real
// PromptVersion rather than a literal is deliberate: if extract's constant were
// ever removed or renamed, this file should stop compiling rather than quietly
// start hashing a stale string.
func base() cache.Key {
	return cache.Key{
		Chunk:    "SuperAI uses CortexDB for storage.",
		Model:    "gemini-3.6-flash-high",
		Ontology: "sds@3",
		Prompt:   extract.PromptVersion,
	}
}

// TestACacheThatSurvivesAPromptChangeReturnsTheOldPromptsOpinion is named after
// the sentence in DESIGN.md §8.2 that it enforces. The cache is keyed on
// everything that would change the answer; each case below changes exactly one
// such thing and asserts the address moved with it.
func TestACacheThatSurvivesAPromptChangeReturnsTheOldPromptsOpinion(t *testing.T) {
	cases := []struct {
		name string
		vary func(cache.Key) cache.Key
		why  string
	}{
		{
			name: "chunk text",
			vary: func(k cache.Key) cache.Key { k.Chunk = k.Chunk + " It also uses Qdrant."; return k },
			why:  "different text is a different question to the model",
		},
		{
			name: "model",
			vary: func(k cache.Key) cache.Key { k.Model = "gemini-3.6-pro"; return k },
			why:  "another model answers differently, and provenance records which",
		},
		{
			name: "ontology version",
			vary: func(k cache.Key) cache.Key { k.Ontology = "sds@4"; return k },
			why:  "the vocabulary constrains the extraction, so it constrains the answer",
		},
		{
			name: "prompt version",
			// The version before the current one, so this case keeps saying
			// what it says whichever version is current: a literal that
			// happens to equal extract.PromptVersion asserts nothing.
			vary: func(k cache.Key) cache.Key { k.Prompt = "extract/1"; return k },
			why:  "a cache that survives a prompt change returns the old prompt's opinion",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := base().Address()
			after := c.vary(base()).Address()
			if before == after {
				t.Fatalf("varying %s did not change the address (%s): %s", c.name, c.why, before)
			}
		})
	}
}

// TestAddressIsStableForTheSameKey is the other half: a resumed job rebuilds
// the key from the same inputs and must land on the same address, or the cache
// never hits and §8.2's whole point is lost.
func TestAddressIsStableForTheSameKey(t *testing.T) {
	if a, b := base().Address(), base().Address(); a != b {
		t.Fatalf("same key produced two addresses: %s != %s", a, b)
	}
}

// TestAddressIsNotForgeableByConcatenation pins the encoding, not just the
// hash. hash(chunk+model+ontology+prompt) cannot tell {"ab","c"} from
// {"a","bc"}: the bytes fed to the hash are identical, so two different
// questions collide on one address and a resumed job serves an answer produced
// for a different model. Any framing that makes field boundaries recoverable
// fixes it; a hash alone does not.
func TestAddressIsNotForgeableByConcatenation(t *testing.T) {
	cases := []struct {
		name string
		a, b cache.Key
	}{
		{
			name: "chunk/model boundary",
			a:    cache.Key{Chunk: "ab", Model: "c"},
			b:    cache.Key{Chunk: "a", Model: "bc"},
		},
		{
			name: "model/ontology boundary",
			a:    cache.Key{Model: "sds@", Ontology: "3"},
			b:    cache.Key{Model: "sds", Ontology: "@3"},
		},
		{
			name: "ontology/prompt boundary",
			a:    cache.Key{Ontology: "sds@3extract", Prompt: "/1"},
			b:    cache.Key{Ontology: "sds@3", Prompt: "extract/1"},
		},
		{
			name: "empty field absorbed by its neighbour",
			a:    cache.Key{Chunk: "text", Model: ""},
			b:    cache.Key{Chunk: "", Model: "text"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.a.Address() == c.b.Address() {
				t.Fatalf("collision across %s: %+v and %+v share address %s", c.name, c.a, c.b, c.a.Address())
			}
		})
	}
}

// TestAddressIsStableAcrossProcessesAndArchitectures pins one address as a
// literal. §8.3 shares the cache between nodes, so an address is a wire value:
// the node that stored an entry and the node that looks it up are different
// processes, often on different machines, and if the two disagree the cache
// misses everything while appearing to work — the expensive failure, because
// the bill goes up and nothing breaks.
//
// The constant below was computed outside Go, from the encoding documented on
// Address: SHA-256 over, for each of the domain tag and the five fields, an
// eight-byte big-endian length followed by the bytes. The pinned key leaves
// Question empty, so it contributes a zero length and no bytes — pinning that
// too, since "an absent field contributes nothing" is the part of a framing
// most easily broken by accident. That it matches is the
// evidence the address is a property of the specification rather than of this
// process — a Go map hash or maphash (seeded per process), a pointer, or the
// iteration order of a map would all pass a same-process test and fail this
// one. A change to this value is a cache-wide invalidation and must be made by
// bumping addressDomain, not by editing the constant.
func TestAddressIsStableAcrossProcessesAndArchitectures(t *testing.T) {
	// Changed once, with addressDomain 1 -> 2, when Question was added: the
	// old addresses were computed without a field that changes the answer.
	//
	// Changed again with extract.PromptVersion "extract/1" -> "extract/2",
	// which the base key above takes from extract on purpose. That bump is
	// itself a cache-wide invalidation and the intended one — the extraction
	// prompt gained the standing-answers section — so this constant moves with
	// it. It is still computed outside Go, from the encoding documented on
	// Address, which is what the paragraph above is asserting.
	const golden = "48989d7023553b65eab200ee50ce0f25882d910964efe291649e3a882333d667"
	if got := base().Address(); got != golden {
		t.Fatalf("address of the pinned key = %s, want %s (an encoding change invalidates every shared entry; bump addressDomain instead)", got, golden)
	}
}

// TestAddressIsHexOfASHA256: the address is used as a map key here and will be
// a column in a shared store (§8.3), so its shape is part of the contract —
// fixed width, and safe in a URL, a filename or an unquoted SQL literal.
func TestAddressIsHexOfASHA256(t *testing.T) {
	a := base().Address()
	if len(a) != 64 {
		t.Fatalf("address is %d chars, want 64", len(a))
	}
	for _, r := range a {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("address contains %q, want lowercase hex only: %s", r, a)
		}
	}
}
