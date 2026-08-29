package cache_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/cache"
)

// storedEntry is an extraction result with provenance filled in on every edge,
// which is what a real extractor returns (§5b makes it a guarantee, not a
// debugging aid).
func storedEntry() cache.Entry {
	prov := alchemy.Provenance{
		Source:     "architecture.pdf",
		Chunk:      14,
		Producer:   alchemy.ProducerLLMExtract,
		Model:      "gemini-3.6-flash-high",
		Ontology:   "sds@3",
		Chunking:   "heading",
		Confidence: 0.82,
	}
	return cache.Entry{
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "System", Name: "SuperAI", Attributes: map[string]any{"lang": "go"}, Provenance: prov},
			{ID: "e2", Type: "System", Name: "CortexDB", Provenance: prov},
		},
		Relations: []alchemy.Relation{
			{From: "e1", To: "e2", Type: "USES", Attributes: map[string]any{"since": "2025"}, Provenance: prov},
		},
		Tokens: 1731,
	}
}

// TestHitReturnsExactlyWhatWasStored is the provenance test. A cache that drops
// or blanks provenance turns an attributable edge into an anonymous one, and
// attributability is the product's whole claim (§5b) — so a resumed job must
// not produce a graph that is less explainable than a fresh one.
func TestHitReturnsExactlyWhatWasStored(t *testing.T) {
	ctx := context.Background()
	c := cache.NewMemory(8)
	k := base()
	want := storedEntry()

	if err := c.Put(ctx, k, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := c.Get(ctx, k)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("Get: miss on a key that was just stored")
	}
	assertEntryEqual(t, got, want)
}

// assertEntryEqual compares field by field rather than with reflect.DeepEqual
// so that a failure names the field that was lost — "provenance.Model empty" is
// actionable in a way that a struct dump is not.
func assertEntryEqual(t *testing.T, got, want cache.Entry) {
	t.Helper()
	if got.Tokens != want.Tokens {
		t.Errorf("Tokens = %d, want %d", got.Tokens, want.Tokens)
	}
	if len(got.Entities) != len(want.Entities) {
		t.Fatalf("got %d entities, want %d", len(got.Entities), len(want.Entities))
	}
	for i := range want.Entities {
		w, g := want.Entities[i], got.Entities[i]
		if g.ID != w.ID || g.Type != w.Type || g.Name != w.Name {
			t.Errorf("entity %d = %+v, want %+v", i, g, w)
		}
		if g.Provenance != w.Provenance {
			t.Errorf("entity %d provenance = %+v, want %+v", i, g.Provenance, w.Provenance)
		}
		if len(g.Attributes) != len(w.Attributes) {
			t.Errorf("entity %d attributes = %v, want %v", i, g.Attributes, w.Attributes)
		}
		for key, wv := range w.Attributes {
			// DeepEqual rather than !=: an attribute value may be a list or a
			// nested object (see the domain on ErrUnsupportedAttribute), and
			// comparing those with == panics rather than failing.
			if !reflect.DeepEqual(g.Attributes[key], wv) {
				t.Errorf("entity %d attribute %q = %v, want %v", i, key, g.Attributes[key], wv)
			}
		}
	}
	if len(got.Relations) != len(want.Relations) {
		t.Fatalf("got %d relations, want %d", len(got.Relations), len(want.Relations))
	}
	for i := range want.Relations {
		w, g := want.Relations[i], got.Relations[i]
		if g.From != w.From || g.To != w.To || g.Type != w.Type {
			t.Errorf("relation %d = %+v, want %+v", i, g, w)
		}
		if g.Provenance != w.Provenance {
			t.Errorf("relation %d provenance = %+v, want %+v", i, g.Provenance, w.Provenance)
		}
		for key, wv := range w.Attributes {
			if !reflect.DeepEqual(g.Attributes[key], wv) {
				t.Errorf("relation %d attribute %q = %v, want %v", i, key, g.Attributes[key], wv)
			}
		}
	}
}

// TestMissIsNotAnError: a key that was never stored is (Entry{}, false, nil).
// An error from this interface means the cache itself failed; not having an
// answer yet is the normal case, and a caller that treats it as a failure would
// abort a job for the ordinary reason that the work has not been done.
func TestMissIsNotAnError(t *testing.T) {
	got, ok, err := cache.NewMemory(8).Get(context.Background(), base())
	if err != nil {
		t.Fatalf("miss returned an error: %v", err)
	}
	if ok {
		t.Fatalf("miss reported a hit")
	}
	if len(got.Entities) != 0 || len(got.Relations) != 0 || got.Tokens != 0 {
		t.Fatalf("miss returned a non-zero entry: %+v", got)
	}
}
