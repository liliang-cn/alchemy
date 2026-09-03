package dgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"

	"github.com/liliang-cn/alchemy/connectors/internal/contributions"
)

// Reading an edge and its facets back, and the one thing about the shape that
// is easy to get wrong.
//
// Dgraph returns a facet as a sibling key of the child object, named
// "<predicate>|<facet>":
//
//	{"rel_USES": [{"uid": "0x1", "name": "CortexDB",
//	               "rel_USES|source": "architecture.pdf",
//	               "rel_USES|chunk": 0,
//	               "rel_USES|producer": "llm-extract"}]}
//
// So the facets of an edge are NOT in a nested object and cannot be decoded
// into a struct with tags — the key depends on the predicate, which depends on
// the relation type, which depends on the corpus. Every row here is decoded
// generically and the prefix is stripped, which is also what keeps
// Options.Prefix configurable.
//
// The numbers survive as numbers. chunk comes back 0 and confidence 0.81,
// where an RDF store hands back a lexical form and a datatype IRI beside it —
// and connectors/rdf shipped a bug that dropped the datatype and returned the
// string "false" for a boolean, which is non-empty and therefore true.

// edgeRow is one neighbour, with its facets left as raw JSON.
type edgeRow map[string]json.RawMessage

// facetsOf pulls the facets of one predicate out of a row.
func facetsOf(row edgeRow, pred string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	want := pred + "|"
	for k, v := range row {
		if strings.HasPrefix(k, want) {
			out[strings.TrimPrefix(k, want)] = v
		}
	}
	return out
}

func fstr(f map[string]json.RawMessage, key string) string {
	var s string
	if raw, ok := f[key]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func fint(f map[string]json.RawMessage, key string, def int) int {
	n := def
	if raw, ok := f[key]; ok {
		if err := json.Unmarshal(raw, &n); err != nil {
			return def
		}
	}
	return n
}

func ffloat(f map[string]json.RawMessage, key string) float64 {
	var v float64
	if raw, ok := f[key]; ok {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

// provenanceFromFacets is the inverse of facets() in write.go.
//
// Chunk defaults to -1 and not to 0: alchemy defines -1 as "the producer did
// not work in chunks", and 0 is a legal chunk index — a record whose chunk
// failed to decode would otherwise cite the first chunk of its file, with
// nothing about the answer looking wrong.
func provenanceFromFacets(f map[string]json.RawMessage) alchemy.Provenance {
	return alchemy.Provenance{
		Source:     fstr(f, keySource),
		Chunk:      fint(f, keyChunk, -1),
		Producer:   alchemy.Producer(fstr(f, keyProducer)),
		Model:      fstr(f, keyModel),
		Ontology:   fstr(f, keyOntology),
		Chunking:   fstr(f, keyChunking),
		Confidence: ffloat(f, keyConfidence),
		ReviewedBy: fstr(f, keyReviewedBy),
		RuleSet:    fstr(f, keyRuleSet),
		RuledBy:    fstr(f, keyRuledBy),
		By:         fstr(f, keyBy),
		At:         fstr(f, keyAt),
	}
}

// neighbours reads every edge touching one node, in both directions.
//
// The predicate list comes from the store's schema rather than from a list this
// connector holds, because the reader is usually a different process from the
// writer — an agent answering questions months after the import — and an
// in-memory list would be empty there.
//
// Both directions in one query, `pred` and `~pred`, which is what @reverse buys
// and why ensureRelPred declares it before the first edge is written.
func (l *Loader) neighbours(ctx context.Context, load, entityID string) ([]recall.Claim, error) {
	preds, err := l.relTypes(ctx)
	if err != nil {
		return nil, err
	}
	if len(preds) == 0 {
		return nil, nil
	}
	var b strings.Builder
	b.WriteString("{ q(func: eq(" + l.pred(keyXID) + ", " + literal(entityXID(load, entityID)) + ")) {\n")
	b.WriteString("  xid: " + l.pred(keyXID) + "\n  name: " + l.pred(keyName) + "\n")
	for i, p := range preds {
		// Aliased so the response key is stable and short; the facet keys are
		// still named after the alias, which is what facetsOf is given.
		b.WriteString(fmt.Sprintf("  o%d: %s @facets { xid: %s name: %s }\n", i, p, l.pred(keyXID), l.pred(keyName)))
		b.WriteString(fmt.Sprintf("  i%d: ~%s @facets { xid: %s name: %s }\n", i, p, l.pred(keyXID), l.pred(keyName)))
	}
	b.WriteString("} }\n")

	data, err := l.query(ctx, b.String())
	if err != nil {
		return nil, err
	}
	var out struct {
		Q []map[string]json.RawMessage `json:"q"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("dgraph: decoding the neighbours of %q: %w", entityID, err)
	}
	if len(out.Q) == 0 {
		return nil, nil
	}
	row := out.Q[0]
	self := recall.Endpoint{ID: entityID, Name: rawString(row["name"])}

	var claims []recall.Claim
	for i, p := range preds {
		typ := l.relTypeOf(p)
		for _, dir := range []struct {
			key string
			out bool
		}{{fmt.Sprintf("o%d", i), true}, {fmt.Sprintf("i%d", i), false}} {
			raw, ok := row[dir.key]
			if !ok {
				continue
			}
			var others []edgeRow
			if err := json.Unmarshal(raw, &others); err != nil {
				continue
			}
			for _, o := range others {
				other := recall.Endpoint{ID: id(rawString(o["xid"]), load), Name: rawString(o["name"])}
				prov := provenanceFromFacets(facetsOf(o, dir.key))
				if dir.out {
					claims = append(claims, recall.NewClaim(self, other, typ, prov))
					continue
				}
				// An incoming edge is the same assertion read from the other
				// end, so the endpoints go back the way the extractor wrote
				// them. A walk that normalised the direction would report a
				// claim the corpus never made.
				claims = append(claims, recall.NewClaim(other, self, typ, prov))
			}
		}
	}
	return claims, nil
}

func rawString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// Claims returns every claim adjacent to one entity, each carrying its own
// provenance rather than its subject's.
func (l *Loader) Claims(ctx context.Context, load, entityID string) ([]recall.Claim, error) {
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return nil, err
	}
	out, err := l.neighbours(ctx, load, entityID)
	if err != nil {
		return nil, fmt.Errorf("dgraph: claims about %q in load %q: %w", entityID, load, err)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Type != b.Type:
			return a.Type < b.Type
		case a.To != b.To:
			return a.To < b.To
		case a.From != b.From:
			return a.From < b.From
		case a.Source != b.Source:
			return a.Source < b.Source
		default:
			return a.Chunk < b.Chunk
		}
	})
	return out, nil
}

// Contributions reports every source that had a hand in one node.
//
// The node's own record is one contribution and every other one comes off an
// edge, which is the same shape all six stores report and for the same reason:
// pkg/sink folds the records sharing an ID before any connector sees them, so
// one node keeps one provenance.
//
// Name is filled for the node's own record and empty for every contribution
// recovered from an edge. That emptiness is the measurement — copying the
// node's name onto every contributor would report that all of them agreed on
// it, turning the one signal that distinguishes "joined on a full name" from
// "joined on a first name" into a constant.
func (l *Loader) Contributions(ctx context.Context, load, entityID string) (recall.Contributions, error) {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return recall.Contributions{}, err
	}
	if !ok {
		return recall.Contributions{}, noLoad(load)
	}
	d, err := l.Describe(ctx, load, entityID)
	if err != nil {
		return recall.Contributions{}, err
	}
	if d.ID == "" {
		// An id the load does not hold is an ordinary answer — nothing
		// contributed to a node that is not there.
		return recall.Contributions{}, nil
	}
	mentions := []recall.Contributor{{
		Source: d.Provenance.Source, Chunk: d.Provenance.Chunk,
		Producer: d.Provenance.Producer, Name: d.Name,
	}}
	claims, err := l.neighbours(ctx, load, entityID)
	if err != nil {
		return recall.Contributions{}, fmt.Errorf("dgraph: contributions to %q in load %q: %w", entityID, load, err)
	}
	for _, c := range claims {
		mentions = append(mentions, recall.Contributor{
			Source: c.Source, Chunk: c.Chunk, Producer: c.Producer,
		})
	}
	return contributions.Assemble(entityID, d.Type, mentions), nil
}
