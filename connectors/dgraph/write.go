package dgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// facets renders one provenance as the parenthesised list that hangs off an
// edge.
//
// This is the whole reason this connector exists as a sixth one. A triple
// cannot carry a property and an RDF store has to name the triple to say
// anything about it; here the eleven fields go on the edge, and the numbers
// stay numbers — chunk comes back as an integer and confidence as a float,
// where RDF costs a datatype IRI on every literal and a decoder that does not
// drop it. connectors/rdf shipped a bug that dropped exactly that.
//
// Empty optional fields are omitted rather than written as "": "this record has
// no model" and "this record's model is the empty string" are not the same
// claim. Chunk is kept even at -1, because DESIGN.md defines -1 as "the
// producer did not work in chunks", which is a fact about the record rather
// than a missing value.
//
// Sorted, so that loading one record twice produces the same bytes and a replay
// is a no-op rather than a rewrite.
func facets(p alchemy.Provenance) string {
	pairs := []string{
		keySource + "=" + literal(p.Source),
		keyChunk + "=" + strconv.Itoa(p.Chunk),
		keyProducer + "=" + literal(string(p.Producer)),
		// Computed here rather than left to the buyer. §5b's promise is that a
		// person "can filter to the half that was guessed", and making them
		// enumerate the producer names hands them a rule the core module owns.
		"deterministic=" + strconv.FormatBool(p.Producer.Deterministic()),
	}
	for k, v := range map[string]string{
		keyModel: p.Model, keyOntology: p.Ontology, keyChunking: p.Chunking,
		keyReviewedBy: p.ReviewedBy, keyRuleSet: p.RuleSet, keyRuledBy: p.RuledBy,
		keyBy: p.By, keyAt: p.At,
	} {
		if v != "" {
			pairs = append(pairs, k+"="+literal(v))
		}
	}
	if p.Confidence != 0 {
		pairs = append(pairs, keyConfidence+"="+strconv.FormatFloat(p.Confidence, 'g', -1, 64))
	}
	sort.Strings(pairs)
	return " (" + strings.Join(pairs, ", ") + ")"
}

// provenanceQuads writes one provenance onto a NODE, where there are no facets
// to hang it from.
//
// A facet attaches to an edge and an entity's provenance is about the entity,
// so it lands as ordinary predicates. That asymmetry is Dgraph's, not
// alchemy's, and it is worth stating because it is the one place the two halves
// of §5b are stored differently in this store: an edge's provenance is read out
// of facets and a node's out of predicates, and recall answers with the same
// shape from both.
func (l *Loader) provenanceQuads(subject string, p alchemy.Provenance) string {
	var b strings.Builder
	b.WriteString(nquad(subject, l.pred(keySource), literal(p.Source)))
	b.WriteString(nquad(subject, l.pred(keyChunk), intLit(p.Chunk)))
	b.WriteString(nquad(subject, l.pred(keyProducer), literal(string(p.Producer))))
	for k, v := range map[string]string{
		keyModel: p.Model, keyOntology: p.Ontology, keyChunking: p.Chunking,
		keyReviewedBy: p.ReviewedBy, keyRuleSet: p.RuleSet, keyRuledBy: p.RuledBy,
		keyBy: p.By, keyAt: p.At,
	} {
		if v != "" {
			b.WriteString(nquad(subject, l.pred(k), literal(v)))
		}
	}
	if p.Confidence != 0 {
		b.WriteString(nquad(subject, l.pred(keyConfidence), floatLit(p.Confidence)))
	}
	return sortedQuads(b.String())
}

// sortedQuads puts a mutation's statements in a fixed order.
//
// Go map iteration is random, so a record written twice would produce two
// different mutation bodies for the same graph. Nothing downstream is wrong
// when that happens — Dgraph converges either way — but a load that cannot be
// byte-compared with itself is a load whose replay nobody can check, and this
// package's idempotency test does exactly that comparison.
func sortedQuads(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// writeEntities upserts one batch of nodes.
//
// One upsert block per entity rather than one for the batch, and that is
// Dgraph's shape rather than a choice: an upsert block has ONE query, so a
// block covering fifty entities would need fifty query variables and fifty
// eq() lookups written out, which is the same number of requests' worth of work
// in one body plus a transaction holding all of them. The batch is still a
// batch — the blocks are sent back to back on one connection.
func (l *Loader) writeEntities(ctx context.Context, batch []alchemy.Entity, rep *Report) error {
	for _, e := range batch {
		xid := entityXID(l.opts.RunID, e.ID)
		var b strings.Builder
		b.WriteString(nquad("uid(v)", l.pred(keyXID), literal(xid)))
		b.WriteString(nquad("uid(v)", l.pred(keyRun), literal(l.opts.RunID)))
		b.WriteString(nquad("uid(v)", l.pred(keyKind), literal(kindEntity)))
		b.WriteString(nquad("uid(v)", l.pred(keyName), literal(e.Name)))
		b.WriteString(nquad("uid(v)", l.pred(keyType), literal(e.Type)))
		for _, a := range e.Aliases {
			b.WriteString(nquad("uid(v)", l.pred(keyAliases), literal(a)))
		}
		if len(e.Attributes) > 0 {
			// The whole map as one JSON string, and not one predicate per key.
			//
			// A predicate is global in Dgraph: its type and its index belong to
			// the cluster, not to this load. Writing a source's `owner` field
			// as a predicate would put a customer's column name into a
			// namespace shared with every other writer, and the first two
			// sources that disagreed about whether `owner` is a string or a uid
			// would collide in a way that surfaces as somebody else's data
			// disappearing. The cost is that attributes are not queryable here,
			// which is what recall.Describe is for — it returns the record, and
			// nothing in the interface promises a filter on a source's fields.
			blob, err := json.Marshal(e.Attributes)
			if err != nil {
				return fmt.Errorf("dgraph: entity %s: attributes: %w", e.ID, err)
			}
			b.WriteString(nquad("uid(v)", l.pred(keyAttrs), literal(string(blob))))
		}
		b.WriteString(l.provenanceQuads("uid(v)", e.Provenance))
		if err := l.mutate(ctx, l.upsert(xid, sortedQuads(b.String()))); err != nil {
			return fmt.Errorf("dgraph: write entity %s: %w", e.ID, err)
		}
		rep.Entities++
	}
	return nil
}

// group is the set of relations that Dgraph's edge identity makes one edge.
type group struct {
	from, to, typ string
	members       []alchemy.Relation
}

// groupRelations folds a batch onto (from, predicate, to).
//
// It exists because FACETS OVERWRITE, SILENTLY. Measured: writing three facets
// onto an edge that already carried six leaves the three and drops the other
// three, and the server answers Success. So one edge holds one provenance here,
// and the choice is between keeping the first and reporting the rest, or
// keeping the last and telling nobody.
//
// The first is kept, which matches pkg/sink's fold for entities and preflight's
// `prev`, and the rest are counted into Report.MergedRelations — the same
// answer connectors/rdf gives for a quoted triple, arrived at from the other
// direction.
func groupRelations(batch []alchemy.Relation) []group {
	order := []string{}
	byKey := map[string]*group{}
	for _, r := range batch {
		k := r.From + "\x00" + r.Type + "\x00" + r.To
		g, seen := byKey[k]
		if !seen {
			g = &group{from: r.From, to: r.To, typ: r.Type}
			byKey[k] = g
			order = append(order, k)
		}
		g.members = append(g.members, r)
	}
	out := make([]group, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

// writeRelations upserts one batch of edges.
//
// Two upserts per edge would be the obvious shape — one to find each end — and
// Dgraph's upsert block allows several query variables, so both ends are bound
// in one. What it cannot do is create a missing end: an edge whose endpoints
// are not in this load is not this connector's to invent, and pkg/sink
// guarantees entities arrive before the relations that name them.
func (l *Loader) writeRelations(ctx context.Context, batch []alchemy.Relation, rep *Report) error {
	for _, g := range groupRelations(batch) {
		if err := l.ensureRelPred(ctx, g.typ); err != nil {
			return err
		}
		fromXID, toXID := entityXID(l.opts.RunID, g.from), entityXID(l.opts.RunID, g.to)
		stmt := "uid(a) <" + l.pred(relPred(g.typ)) + "> uid(b)" + facets(g.members[0].Provenance) + " .\n"
		body := "upsert {\n query {\n" +
			"  a as var(func: eq(" + l.pred(keyXID) + ", " + literal(fromXID) + "))\n" +
			"  b as var(func: eq(" + l.pred(keyXID) + ", " + literal(toXID) + "))\n" +
			" }\n mutation { set {\n" + stmt + " } }\n}\n"
		if err := l.mutate(ctx, body); err != nil {
			return fmt.Errorf("dgraph: write relation %s -[%s]-> %s: %w", g.from, g.typ, g.to, err)
		}
		rep.Relations++
		if extra := len(g.members) - 1; extra > 0 {
			rep.MergedRelations += extra
		}
	}
	return nil
}

// relPred is the predicate one relation type is written as.
//
// Prefixed like everything else, and sanitised: a Dgraph predicate name in
// N-Quads is delimited by angle brackets, so a type containing '>' would close
// the name early and the rest of the statement would be parsed as syntax. A
// type is declared by an ontology and is unlikely to contain one; "unlikely" is
// not a reason to let a corpus decide where a predicate name ends.
func relPred(typ string) string {
	r := strings.NewReplacer("<", "_", ">", "_", " ", "_", "\n", "_", "\t", "_")
	return "rel_" + r.Replace(typ)
}

// ensureRelPred declares one relation type's predicate, with a reverse edge.
//
// @reverse is not an optimisation. recall.Claims promises both directions in
// one answer — an agent asking what is known about a thing does not care which
// way the extractor happened to write the edge — and Dgraph cannot traverse an
// edge backwards unless the predicate was declared reversible BEFORE the edge
// was written. A connector that declared it afterwards would have a graph whose
// older half is walkable one way only, and nothing about the answer would look
// wrong.
//
// Declared lazily rather than in ensureSchema, because the predicate name comes
// from the corpus: a relation type is whatever the ontology declared, and this
// connector learns the list by being handed the edges.
//
// The per-process memo is a cache and not a source of truth. Altering a
// predicate to the type and index it already has is a no-op in Dgraph, so a
// second process that never saw the memo re-declares and nothing happens; the
// memo only saves the round trip.
func (l *Loader) ensureRelPred(ctx context.Context, typ string) error {
	name := l.pred(relPred(typ))
	if l.rels.has(name) {
		return nil
	}
	if err := l.alter(ctx, name+": [uid] @reverse .\n"); err != nil {
		return fmt.Errorf("dgraph: declaring relation predicate %s: %w", name, err)
	}
	l.rels.add(name)
	return nil
}

// relTypes asks the store which relation predicates exist under this prefix.
//
// The store rather than a list this connector keeps, because the reader is
// usually a different process from the writer — an agent answering questions
// months after the import — and a list held in memory would be empty there. The
// schema is the one place the answer survives.
func (l *Loader) relTypes(ctx context.Context) ([]string, error) {
	data, err := l.query(ctx, "schema {}")
	if err != nil {
		return nil, err
	}
	var out struct {
		Schema []struct {
			Predicate string `json:"predicate"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("dgraph: reading the schema: %w", err)
	}
	want := l.pred("rel_")
	var names []string
	for _, p := range out.Schema {
		if strings.HasPrefix(p.Predicate, want) {
			names = append(names, p.Predicate)
		}
	}
	sort.Strings(names)
	return names, nil
}

// relTypeOf recovers the alchemy relation type from a predicate name.
func (l *Loader) relTypeOf(pred string) string {
	return strings.TrimPrefix(pred, l.pred("rel_"))
}
