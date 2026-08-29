package cache

import (
	"errors"
	"fmt"
)

// ErrUnsupportedAttribute is what Put returns for an attribute value the cache
// cannot promise to give back unchanged.
//
// The decision behind it, recorded here because it is the kind of choice that
// looks arbitrary later:
//
// Entry.Attributes is map[string]any, which round-trips through Go memory
// losslessly and through a wire format lossily. An int stored by a single-node
// run comes back float64 from a shared store; a time.Time comes back a string.
// A cache exists to make a resumed job identical to a fresh one (§8.2), so a
// store that quietly converted would produce the one difference this package is
// here to prevent — and it would produce it in the type, where nothing
// downstream looks.
//
// There were two ways out: carry a type-tagged encoding rich enough to restore
// any Go value, or declare a narrower domain and hold everything to it. The
// domain wins, for a reason that is about the product and not about the
// encoding. §4 returns the result as JSON, so an attribute that JSON cannot
// carry never reached a caller intact in the first place — a richer cache
// format would faithfully preserve a value that the API on the other side was
// always going to flatten. Preserving it in the cache and losing it at the
// boundary is not better than refusing it at the cache; it is the same loss,
// discovered further away.
//
// So the domain is JSON's: string, bool, float64, nil, []any and
// map[string]any recursively over those. Nothing real is excluded — an
// extraction's attributes come out of json.Unmarshal and are already exactly
// this set — which is what makes the check a guard rather than a limitation.
//
// The check lives in the Cache contract rather than in the shared store alone,
// so that the two implementations agree. A domain enforced only where it is
// technically necessary is a domain a single-node run never tests, and the
// first thing a cluster deployment would discover is that the entries it had
// been caching for a year are not cacheable.
var ErrUnsupportedAttribute = errors.New("cache: attribute value cannot be stored without changing its type")

// validate reports the first attribute the cache will not accept.
//
// It runs before anything is written, so a refused entry is stored nowhere
// rather than half stored: a cache holding the entities of an entry whose
// relations were rejected would serve a graph with edges missing and call it a
// hit.
func validate(e Entry) error {
	for _, ent := range e.Entities {
		if err := checkAttrs(ent.Attributes); err != nil {
			return fmt.Errorf("cache: entity %q: %w", ent.ID, err)
		}
	}
	for _, rel := range e.Relations {
		if err := checkAttrs(rel.Attributes); err != nil {
			return fmt.Errorf("cache: relation %s -[%s]-> %s: %w", rel.From, rel.Type, rel.To, err)
		}
	}
	return nil
}

func checkAttrs(m map[string]any) error {
	for k, v := range m {
		if err := checkValue(v); err != nil {
			return fmt.Errorf("attribute %q: %w", k, err)
		}
	}
	return nil
}

// checkValue walks a value the way an encoder would. The error names the Go
// type, because "unsupported attribute" tells a caller that something is wrong
// and not which line to change.
func checkValue(v any) error {
	switch t := v.(type) {
	case nil, string, bool, float64:
		return nil
	case []any:
		for i, el := range t {
			if err := checkValue(el); err != nil {
				return fmt.Errorf("index %d: %w", i, err)
			}
		}
		return nil
	case map[string]any:
		return checkAttrs(t)
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedAttribute, v)
	}
}
