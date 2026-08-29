package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/cache"
)

// The decision this file tests, stated once:
//
// A cache exists to make a resumed job identical to a fresh one. An attribute
// map round-trips through Go memory losslessly and through any wire format
// lossily — an int comes back float64, a time.Time comes back a string — so a
// shared store that simply serialised whatever it was handed would produce a
// resumed job whose attributes differ in *type* from a fresh one's. That is the
// exact class of difference this package exists to prevent.
//
// So the domain of an attribute value is declared, and it is JSON's: string,
// bool, float64, nil, and arrays and objects of those. A value outside it is
// refused at Put rather than converted, because converting is the silent
// version of the bug. Refusing is not a restriction on anything real: an
// extraction's attributes come out of json.Unmarshal, so they are already
// exactly this set, and §4 returns the result as JSON anyway — a value that
// cannot survive the domain could never have reached a caller intact.
func TestTheAttributeDomainIsJSONsAndItIsAccepted(t *testing.T) {
	ctx := context.Background()
	c := cache.NewMemory(8)
	e := cache.Entry{
		Entities: []alchemy.Entity{{
			ID: "e1", Type: "System", Name: "SuperAI",
			Attributes: map[string]any{
				"text":   "go",
				"yes":    true,
				"count":  float64(3),
				"ratio":  0.5,
				"absent": nil,
				"list":   []any{"a", float64(1), false, nil},
				"nested": map[string]any{"k": "v", "n": float64(2)},
			},
		}},
	}
	if err := c.Put(ctx, base(), e); err != nil {
		t.Fatalf("Put refused a value JSON can carry: %v", err)
	}
}

func TestAValueThatCannotSurviveTheWireIsRefusedNotConverted(t *testing.T) {
	ctx := context.Background()
	for name, value := range map[string]any{
		"int":       42,
		"time":      time.Now(),
		"struct":    alchemy.Provenance{},
		"byteslice": []byte("x"),
		"intslice":  []any{1, 2},
		"nestedint": map[string]any{"n": 7},
	} {
		t.Run(name, func(t *testing.T) {
			c := cache.NewMemory(8)
			e := cache.Entry{Entities: []alchemy.Entity{{
				ID: "e1", Attributes: map[string]any{"a": value},
			}}}
			err := c.Put(ctx, base(), e)
			if !errors.Is(err, cache.ErrUnsupportedAttribute) {
				t.Fatalf("Put(%s) = %v, want ErrUnsupportedAttribute", name, err)
			}
			// And nothing was stored: a half-accepted entry would be worse than
			// a refused one.
			if _, ok, _ := c.Get(ctx, base()); ok {
				t.Fatal("a refused entry was stored anyway")
			}
		})
	}
}

// A relation's attributes are checked too. They reach a reader by the same
// route and break in the same way, and a check that covered only entities
// would be a guarantee with a hole in it exactly where an edge is.
func TestARelationsAttributesAreCheckedToo(t *testing.T) {
	c := cache.NewMemory(8)
	err := c.Put(context.Background(), base(), cache.Entry{
		Relations: []alchemy.Relation{{From: "a", To: "b", Type: "USES", Attributes: map[string]any{"n": 1}}},
	})
	if !errors.Is(err, cache.ErrUnsupportedAttribute) {
		t.Fatalf("Put = %v, want ErrUnsupportedAttribute", err)
	}
}

// The refusal must say what to change. "cache: unsupported attribute" names
// nothing; the entity, the key and the Go type are what a person needs.
func TestTheRefusalNamesWhatWasWrong(t *testing.T) {
	c := cache.NewMemory(8)
	err := c.Put(context.Background(), base(), cache.Entry{
		Entities: []alchemy.Entity{{ID: "svc-7", Attributes: map[string]any{"started": time.Now()}}},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"svc-7", "started", "time.Time"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Fetch's contract is unchanged by the refusal: a Put that fails is not a job
// that fails. The chunk is simply bought again next time, which is the price
// §7.2 says is acceptable and the corrupted graph is not.
func TestARefusedPutDoesNotFailTheJob(t *testing.T) {
	produced := cache.Entry{Entities: []alchemy.Entity{{ID: "e1", Attributes: map[string]any{"n": 1}}}}
	got, hit, err := cache.Fetch(context.Background(), cache.NewMemory(8), base(),
		func(context.Context) (cache.Entry, error) { return produced, nil })
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if hit {
		t.Fatal("Fetch reported a hit on an empty cache")
	}
	if len(got.Entities) != 1 || got.Entities[0].Attributes["n"] != 1 {
		t.Fatalf("Fetch = %+v, want the produced entry unchanged", got)
	}
}
