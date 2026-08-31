package rdf

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Describe returns one entity whole.
//
// Three reads in one query, and they are three because a triple store answers
// this shape in three unrelated patterns rather than in one row: the entity's
// own statements, its aliases, and the attributes a source put on it. The
// attributes are the interesting half. This connector mints a predicate per
// attribute key -- <base>attr/<key> -- so there is no fixed list to project,
// and the pattern has to be ?p ?o over that namespace with the key read back
// out of the predicate IRI.
//
// That is why the attribute predicates were minted under a namespace of their
// own rather than dropped into the alchemy one. A design that had written them
// as al:city, al:from and so on would be indistinguishable from this
// connector's own vocabulary at read time, and separating them again would mean
// a hard-coded list of everything alchemy itself uses -- which goes stale the
// first time this package adds a predicate.
func (l *Loader) Describe(ctx context.Context, load, id string) (recall.Description, error) {
	q, err := l.describeSPARQL(load, id)
	if err != nil {
		return recall.Description{}, err
	}
	rows, err := l.query(ctx, q)
	if err != nil {
		return recall.Description{}, fmt.Errorf("rdf: describe %q in load %q: %w", id, load, err)
	}
	if len(rows) == 0 {
		ok, err := l.finished(ctx, load)
		if err != nil {
			return recall.Description{}, err
		}
		if !ok {
			return recall.Description{}, fmt.Errorf("%w: %q is not a finished load in this store; "+
				"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
				recall.ErrNoLoad, load)
		}
		return recall.Description{}, nil
	}

	d := recall.Description{ID: id}
	seenAlias := map[string]bool{}
	jsonKeys := map[string]bool{}
	attrs := map[string]binding{}
	for _, r := range rows {
		d.Type, d.Name = r["type"].Value, r["name"].Value
		d.Provenance = provDecode(r)
		if a, ok := r["alias"]; ok && a.Value != "" && !seenAlias[a.Value] {
			seenAlias[a.Value] = true
			d.Aliases = append(d.Aliases, a.Value)
		}
		if k, ok := r["jsonKey"]; ok && k.Value != "" {
			jsonKeys[k.Value] = true
		}
		if p, ok := r["ap"]; ok && p.Value != "" {
			if key := l.attrKeyFromIRI(p.Value); key != "" {
				attrs[key] = r["av"]
			}
		}
	}
	if len(attrs) > 0 {
		d.Attributes = make(map[string]any, len(attrs))
		for k, v := range attrs {
			// Only the keys the writer recorded as JSON are decoded that way.
			// Trying every value would turn a literal "123" or "true" that a
			// source meant as text into a number or a boolean, which is a change
			// to the record made on the way out of the store.
			if jsonKeys[k] {
				var decoded any
				if err := json.Unmarshal([]byte(v.Value), &decoded); err == nil {
					d.Attributes[k] = decoded
					continue
				}
			}
			d.Attributes[k] = typedValue(v)
		}
	}
	return d, nil
}

func (l *Loader) describeSPARQL(load, id string) (string, error) {
	scope, err := l.scope(load)
	if err != nil {
		return "", err
	}
	ent := iri(l.entityIRI(load, id))
	if ent.err != nil {
		return "", ent.err
	}
	// The entity's own statements are required and everything else is OPTIONAL,
	// so a node with no aliases and no attributes still comes back. An
	// unmatched OPTIONAL leaves its variables unbound rather than empty, which
	// is what lets the loop above tell "no alias" from "an alias that is the
	// empty string".
	// Every verb is indexed. Mixing %s with %[3]s makes the ones after the
	// index continue from it rather than from where they were, which produced a
	// query the store rejected with a parse error pointing at a column number
	// -- the least useful place to be told that an argument list is shuffled.
	return fmt.Sprintf(
		"SELECT ?type ?name ?alias ?ap ?av ?jsonKey%[1]s WHERE {\n%[2]s"+
			"%[3]s <%[4]s> <%[5]s> ; <%[6]s> ?type ; <%[7]s> ?name .\n"+
			"OPTIONAL { %[3]s <%[8]s> ?alias }\n"+
			"OPTIONAL { %[3]s ?ap ?av . FILTER(STRSTARTS(STR(?ap), %[9]s)) }\n"+
			"OPTIONAL { %[3]s <%[10]s> ?jsonKey }\n"+
			"%[11]s}\n}",
		provVars(), scope, ent.text, rdfType, clEntity, pType, rdfsLabel,
		skosAltLabel, lit(l.opts.Base+"attr/").text, pJSONAttribute,
		provPattern(fmt.Sprintf("<< %s <%s> ?class >>", ent.text, rdfType))), nil
}

// attrKeyFromIRI is attrIRI read backwards. An IRI outside the attribute
// namespace is not an attribute and returns empty rather than a guess, the same
// rule entityIDFromIRI follows.
func (l *Loader) attrKeyFromIRI(s string) string {
	prefix := l.opts.Base + "attr/"
	if !strings.HasPrefix(s, prefix) {
		return ""
	}
	key, err := url.PathUnescape(strings.TrimPrefix(s, prefix))
	if err != nil {
		return ""
	}
	return key
}

// typedValue is the inverse of attrValue: a literal's datatype says what Go
// value the source put there.
//
// Without it a bool comes back as the string "false", which is worse than it
// looks -- "false" is a non-empty string, so a caller writing the obvious
// truthiness check gets the opposite of what the source said. The other two
// stores do not need this because they hold a typed value natively; a triple
// store holds a lexical form and a datatype beside it, and dropping the second
// half is dropping the type.
//
// An unrecognised datatype stays text. This connector writes exactly three, and
// guessing at a fourth would be inventing a conversion the writer never made.
func typedValue(b binding) any {
	switch b.Datatype {
	case xsdBoolean:
		return b.Value == "true"
	case xsdInteger, xsdDouble:
		if f, err := strconv.ParseFloat(b.Value, 64); err == nil {
			return f
		}
	}
	return b.Value
}
