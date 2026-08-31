package neo4j

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Describe returns one entity whole.
//
// It is the only read here that hands back the record rather than a rendering
// of it, and the reason it exists is that everything a source said about a node
// beyond its name was write-only: writeEntities puts the attributes on as
// properties and the aliases on as a list, and no reader could get either back.
//
// The decoding is the exact inverse of the write, which is why it reads the
// whole property bag rather than naming fields. A node here carries three kinds
// of property in one namespace -- this connector's, under the reserved prefix;
// alchemy's one unprefixed field, `name`; and the source's own, at the top
// level where a buyer writes `n.city` rather than a name they would have to be
// told. Anything that is not one of the first two is the source's, and that is
// the rule rather than a list, because a list would go stale the first time an
// extractor produced a field nobody had thought of -- which is every import.
func (l *Loader) Describe(ctx context.Context, load, id string) (recall.Description, error) {
	stmt, err := l.describeCypher()
	if err != nil {
		return recall.Description{}, err
	}
	recs, err := l.read(ctx, stmt, map[string]any{
		"run": load, "id": id, "internal": toAny(l.opts.internalLabels()),
	})
	if err != nil {
		return recall.Description{}, fmt.Errorf("neo4j: describe %q in load %q: %w", id, load, err)
	}
	if len(recs) == 0 {
		ok, err := l.finished(ctx, load)
		if err != nil {
			return recall.Description{}, err
		}
		if !ok {
			return recall.Description{}, fmt.Errorf("%w: %q is not a finished load in this graph; "+
				"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
				recall.ErrNoLoad, load)
		}
		return recall.Description{}, nil
	}
	props, _ := recs[0]["props"].(map[string]any)
	return recall.Description{
		ID:         id,
		Type:       str(props[l.raw(keyType)]),
		Name:       str(props[keyName]),
		Aliases:    stringList(props[l.raw(keyAliases)]),
		Attributes: l.attributesOf(props),
		Provenance: l.provenanceOf(props),
	}, nil
}

func (l *Loader) describeCypher() (string, error) {
	scope, err := l.scope()
	if err != nil {
		return "", err
	}
	base, _ := quoteIdent(l.opts.BaseLabel)
	// properties(n) rather than a projection, because the source's fields are
	// not known to this package. A query naming them could only return the ones
	// somebody remembered.
	return scope + fmt.Sprintf(
		"MATCH (n:%s {%s: $run, %s: $id}) "+
			"WHERE NOT any(lbl IN labels(n) WHERE lbl IN $internal) "+
			"RETURN properties(n) AS props",
		base, l.prop(keyRun), l.prop(keyID)), nil
}

// raw is prop without the quoting, for looking a property up in a map that came
// back from the driver rather than naming one in a statement.
func (l *Loader) raw(name string) string { return l.opts.ReservedPrefix + name }

// attributesOf is the inverse of attributeProps: everything the source put on
// the node, with the values that had to travel as JSON text decoded again.
//
// A value that will not decode is returned as the text it was stored as rather
// than dropped or errored on. It is a fact about the record either way, and a
// reader who can see `{"city": "Wien"` knows more than one handed nothing.
func (l *Loader) attributesOf(props map[string]any) map[string]any {
	encoded := map[string]bool{}
	for _, k := range stringList(props[l.raw(keyJSONAttrs)]) {
		encoded[k] = true
	}
	out := map[string]any{}
	for k, v := range props {
		if k == keyName || strings.HasPrefix(k, l.opts.ReservedPrefix) {
			continue
		}
		if encoded[k] {
			var decoded any
			if err := json.Unmarshal([]byte(str(v)), &decoded); err == nil {
				out[k] = decoded
				continue
			}
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// provenanceOf rebuilds the record's provenance from the flattened properties.
//
// Deterministic is deliberately not read back. It is written for the buyer's
// own Cypher and it is the rule as it stood on the day of the import;
// alchemy.Producer.Deterministic is the rule today, and recall.NewClaim gives
// the argument at length.
func (l *Loader) provenanceOf(props map[string]any) alchemy.Provenance {
	return alchemy.Provenance{
		Source:     str(props[l.raw(keySource)]),
		Chunk:      num(props[l.raw(keyChunk)]),
		Producer:   alchemy.Producer(str(props[l.raw(keyProducer)])),
		Model:      str(props[l.raw(keyModel)]),
		Ontology:   str(props[l.raw(keyOntology)]),
		Chunking:   str(props[l.raw(keyChunking)]),
		ReviewedBy: str(props[l.raw(keyReviewedBy)]),
		RuleSet:    str(props[l.raw(keyRuleSet)]),
		RuledBy:    str(props[l.raw(keyRuledBy)]),
		By:         str(props[l.raw(keyBy)]),
		At:         str(props[l.raw(keyAt)]),
		Confidence: f64(props[l.raw(keyConfidence)]),
	}
}

func stringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, str(i))
	}
	return out
}

func f64(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	}
	return 0
}
