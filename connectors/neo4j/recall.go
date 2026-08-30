package neo4j

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Loader is a recall.Reader, and this file is the half of this connector that
// was missing rather than the half that was written four times.
//
// pkg/sink was extracted because four connectors had each invented the write
// side. The read side was measured the same way and this store scored nothing:
// an agent over a graph loaded here had to be handed every Cypher query by
// hand, outside the repository, because the package that decides what a node
// is called and which edges are its own bookkeeping had no way to be asked.
// Every one of those hand-written queries got something subtly wrong, and each
// of the four below says which.
//
// What stays this store's own is what only a property graph has an opinion
// about: that a label cannot be a parameter, that the bookkeeping edges have
// to be named to be excluded, and that "is this load finished" is a property on
// a marker node rather than a view the database maintains.
var _ recall.Reader = (*Loader)(nil)

// scope is the clause every read below begins with, and it is the thing this
// connector had no equivalent of.
//
// pgvector reads through loaded_* views that hide a load until its last
// statement commits, so a half-written import cannot answer a query there. Here
// the same fact is a property on the run node — written false before the first
// batch and true after the last (see claimRun) — and nothing consulted it,
// because nothing read. A query that skipped it would serve a load that is
// still arriving as though it were whole: the entities that happen to have
// landed, the edges between the ones that have, and no way for the reader to
// tell. The single MATCH costs one index lookup and makes the two connectors
// answer the same question.
func (l *Loader) scope() (string, error) {
	base, err := quoteIdent(l.opts.BaseLabel)
	if err != nil {
		return "", fmt.Errorf("neo4j: base label: %w", err)
	}
	runLabel, err := quoteIdent(l.internalLabel("Run"))
	if err != nil {
		return "", err
	}
	// The reserved prefix is checked once here rather than at each of the
	// twenty places below that concatenate it onto a key. Every one of those is
	// prefix + a fixed ASCII name, so a prefix that survives this survives all
	// of them — and one that does not would otherwise reach the server as a
	// statement with a hole in it, which is a syntax error attributed to this
	// package rather than to the option that caused it.
	if _, err := quoteIdent(l.opts.ReservedPrefix + keyID); err != nil {
		return "", fmt.Errorf("neo4j: reserved prefix: %w", err)
	}
	// It reduces to a count rather than carrying the run node forward, and that
	// is not a stylistic choice: an entity is joined to its run by a property
	// and not by an edge, so `WITH run MATCH (n)` is two disconnected patterns
	// and the planner says so, on every read, forever. Aggregating first leaves
	// no node in scope to be disconnected from. An empty match still produces
	// one row here — count over nothing is 0 — which the WHERE then drops, so a
	// load that is absent and a load that is unfinished both answer nothing.
	return fmt.Sprintf("MATCH (run:%s:%s {%s: $run}) WHERE run.%s = true WITH count(run) AS found WHERE found > 0 ",
		base, runLabel, l.prop(keyID), l.prop(keyComplete)), nil
}

// prop renders one reserved property as a quoted identifier, so that a
// ReservedPrefix a buyer chose cannot become a syntax error in a query.
func (l *Loader) prop(name string) string {
	q, _ := quoteIdent(l.opts.ReservedPrefix + name)
	return q
}

// Find returns the entities of one load whose name contains name.
//
// Two exclusions rather than one. `_run` keeps the answer inside the load the
// caller asked for, which is this package's oldest rule — Entity.ID says
// nothing across runs — and the label test keeps this connector's own
// bookkeeping nodes out. The second looks redundant today and is not a
// belt-and-braces: a chunk, a finding and a run marker all carry the base label
// and are all excluded here only because internalLabels() is complete, which is
// a fact TestEveryInternalLabelIsRefusedAsAnOntologyType maintains. The
// hand-written version of this query filtered on `name IS NOT NULL` instead and
// worked for exactly the reason that no bookkeeping node happens to have a
// plain `name` property — which is true, and is a property of the writer that
// nothing was holding still.
func (l *Loader) Find(ctx context.Context, load, name string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("neo4j: limit = %d is not a number of anchors", limit)
	}
	stmt, err := l.findCypher()
	if err != nil {
		return recall.Found{}, err
	}
	recs, err := l.read(ctx, stmt, map[string]any{
		"run": load, "name": name, "limit": int64(limit),
		"internal": toAny(l.opts.internalLabels()),
	})
	if err != nil {
		return recall.Found{}, fmt.Errorf("neo4j: find %q in load %q: %w", name, load, err)
	}
	found := recall.Found{Nodes: []recall.Node{}}
	for _, r := range recs {
		found.Total = num(r["total"])
		page, _ := r["page"].([]any)
		for _, item := range page {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			found.Nodes = append(found.Nodes, recall.Node{
				ID: str(m["id"]), Type: str(m["type"]), Name: str(m["name"]),
			})
		}
	}
	return found, nil
}

// findCypher is the anchor query. The four builders in this file are separate
// from the calls that run them so that the invariant every one of them shares —
// that a read is scoped to one load, and to a finished one — is assertable
// without a database. A query is the kind of code that is only ever tested
// where a server is, which is to say on the machines that have one.
func (l *Loader) findCypher() (string, error) {
	scope, err := l.scope()
	if err != nil {
		return "", err
	}
	base, _ := quoteIdent(l.opts.BaseLabel)
	return scope + fmt.Sprintf(
		"MATCH (n:%[1]s {%[2]s: $run}) "+
			"WHERE n.%[3]s IS NOT NULL AND NOT any(lbl IN labels(n) WHERE lbl IN $internal) "+
			"AND toLower(n.%[3]s) CONTAINS toLower($name) "+
			"WITH DISTINCT n.%[4]s AS id, n.%[5]s AS type, n.%[3]s AS name "+
			// The count travels with the page, collected in one statement,
			// because a second query would count a store that had moved. The
			// collect-then-slice shape is Cypher's way of saying "after the
			// match, before the limit", which is the number wanted: how many
			// matched, not how many came back.
			"ORDER BY name, id "+
			// `all` cannot be the variable: it is Cypher's own predicate
			// function, and size(all) parses as a call to it with no
			// arguments rather than as the size of a list.
			"WITH collect({id: id, type: type, name: name}) AS matches "+
			"RETURN size(matches) AS total, matches[0..$limit] AS page",
		base, l.prop(keyRun), keyName, l.prop(keyID), l.prop(keyType)), nil
}

// Claims returns every extracted edge touching one entity, in either
// direction, each carrying the provenance of the edge.
//
// Of the edge, and this is the correction that matters most. The hand-written
// walk read `startNode(r)._source`, `startNode(r)._producer` and
// `startNode(r)._deterministic` — the subject node's provenance, not the
// assertion's. Both carry a full one, because provenanceProps deliberately
// flattens the same shape onto nodes and relationships, so the query returned
// plausible values for every row and attributed every claim about an entity to
// whatever sentence first named that entity. §5b's promise is that "each of
// them can name its own producer", and it is why writeRelations keeps two
// chunks that said the same thing as two edges rather than merging them: a
// merged edge can name only one producer. A walk that reads the node's throws
// away exactly what that cost was paid for.
//
// The bookkeeping edges are excluded by name from bookkeeping(), and the
// bookkeeping *nodes* by label as well. The second is the guard the first
// needs: a new edge type added to findings.go and forgotten here would
// otherwise walk an agent straight from an entity into a duplicate report and
// hand it back as a claim about the world.
func (l *Loader) Claims(ctx context.Context, load, id string) ([]recall.Claim, error) {
	stmt, err := l.claimsCypher()
	if err != nil {
		return nil, err
	}
	recs, err := l.read(ctx, stmt, map[string]any{
		"run": load, "id": id,
		"bookkeeping": bookkeeping(), "internal": toAny(l.opts.internalLabels()),
	})
	if err != nil {
		return nil, fmt.Errorf("neo4j: claims about %q in load %q: %w", id, load, err)
	}
	out := make([]recall.Claim, 0, len(recs))
	for _, r := range recs {
		// Through recall.NewClaim, so that stated-or-inferred is
		// alchemy.Producer.Deterministic and not the `_deterministic` property
		// sitting right beside the producer on the same edge. That property is
		// written for the buyer's own Cypher, and it is the rule as it stood on
		// the day of the import; a reader deciding how far to trust a sentence
		// today should be told today's answer.
		out = append(out, recall.NewClaim(str(r["subject"]), str(r["rel"]), str(r["object"]),
			alchemy.Provenance{
				Source:   str(r["source"]),
				Chunk:    num(r["chunk"]),
				Producer: alchemy.Producer(str(r["producer"])),
			}))
	}
	return out, nil
}

// claimsCypher is the one-hop walk. See findCypher for why it is a builder.
//
// DISTINCT because the walk is undirected and two parallel edges that agree on
// every field it returns render as the same sentence; a pack that printed one
// twice would be telling a reader the corpus said it twice, which is a claim
// about the corpus this query cannot support.
func (l *Loader) claimsCypher() (string, error) {
	scope, err := l.scope()
	if err != nil {
		return "", err
	}
	base, _ := quoteIdent(l.opts.BaseLabel)
	return scope + fmt.Sprintf(
		"MATCH (x:%[1]s {%[2]s: $run, %[3]s: $id})-[r]-(y:%[1]s {%[2]s: $run}) "+
			"WHERE NOT type(r) IN $bookkeeping AND NOT any(lbl IN labels(y) WHERE lbl IN $internal) "+
			"RETURN DISTINCT startNode(r).%[4]s AS subject, type(r) AS rel, endNode(r).%[4]s AS object, "+
			"r.%[5]s AS source, r.%[6]s AS chunk, r.%[7]s AS producer "+
			"ORDER BY rel, object, subject, source, chunk",
		base, l.prop(keyRun), l.prop(keyID), keyName,
		l.prop(keySource), l.prop(keyChunk), l.prop(keyProducer)), nil
}

// Cite resolves one [source#index] marker against one load.
//
// Both halves have to match, and matching only the index would have worked:
// a job's chunk indexes are unique across the whole job, so within a run the
// number alone identifies the chunk. It is the wrong shape anyway. The marker a
// reader is holding says a file and a number, and a caller who passed the right
// number with the wrong file would be handed the other file's text with nothing
// about the answer looking wrong — which is the same failure as the missing
// load filter, one field over.
//
// Nothing found is an error and never a zero Citation, and which error it is
// says which mistake was made: ErrNoLoad for a load that is not here or not
// finished, ErrNoCitation for a chunk this load does not hold. The second query
// is on the failure path only, and it is worth a round trip because the two
// have different fixes — one is a claim pointing at material that was not
// loaded, the other is an agent citing the wrong import, which is the bug the
// load parameter exists for arriving as a typo instead of as a wrong answer.
func (l *Loader) Cite(ctx context.Context, load, source string, index int) (recall.Citation, error) {
	stmt, err := l.citeCypher()
	if err != nil {
		return recall.Citation{}, err
	}
	recs, err := l.read(ctx, stmt, map[string]any{"run": load, "source": source, "index": int64(index)})
	if err != nil {
		return recall.Citation{}, fmt.Errorf("neo4j: cite %s#%d in load %q: %w", source, index, load, err)
	}
	if len(recs) == 0 {
		return recall.Citation{}, l.whyNoCitation(ctx, load, source, index)
	}
	r := recs[0]
	return recall.Citation{
		Source: str(r["source"]), Index: num(r["idx"]),
		Start: num(r["startByte"]), End: num(r["endByte"]), Text: str(r["text"]),
	}, nil
}

// citeCypher resolves a chunk. See findCypher for why it is a builder.
func (l *Loader) citeCypher() (string, error) {
	scope, err := l.scope()
	if err != nil {
		return "", err
	}
	base, _ := quoteIdent(l.opts.BaseLabel)
	chunkLabel, err := quoteIdent(l.internalLabel("Chunk"))
	if err != nil {
		return "", err
	}
	// `_index` is compared as the integer it was written as. The hand-written
	// query wrapped it in toString() because the agent was passing text, which
	// turns every chunk in the store into a string conversion and makes chunk
	// 10 compare as though it were between 1 and 2.
	return scope + fmt.Sprintf(
		"MATCH (c:%[1]s:%[2]s {%[3]s: $run}) WHERE c.%[4]s = $source AND c.%[5]s = $index "+
			"RETURN c.%[4]s AS source, c.%[5]s AS idx, c.%[6]s AS startByte, c.%[7]s AS endByte, c.%[8]s AS text",
		base, chunkLabel, l.prop(keyRun), l.prop(keySource), l.prop(keyIndex),
		l.prop(keyStart), l.prop(keyEnd), l.prop(keyText)), nil
}

// whyNoCitation tells the two absences apart. It runs only when a citation
// failed, so the ordinary path pays nothing for it.
func (l *Loader) whyNoCitation(ctx context.Context, load, source string, index int) error {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %q is not a finished load in this graph; "+
			"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
			recall.ErrNoLoad, load)
	}
	return fmt.Errorf("%w: load %q holds no chunk %d of %q — the claim that cited it cannot be checked "+
		"against this import, and must not be offered as evidence from it",
		recall.ErrNoCitation, load, index, source)
}

// finished reports whether one load is present and complete. It is the read
// side of the invariant claimRun and completeRun maintain between them.
func (l *Loader) finished(ctx context.Context, load string) (bool, error) {
	base, err := quoteIdent(l.opts.BaseLabel)
	if err != nil {
		return false, fmt.Errorf("neo4j: base label: %w", err)
	}
	runLabel, err := quoteIdent(l.internalLabel("Run"))
	if err != nil {
		return false, err
	}
	recs, err := l.read(ctx, fmt.Sprintf("MATCH (r:%s:%s {%s: $run}) RETURN r.%s AS complete",
		base, runLabel, l.prop(keyID), l.prop(keyComplete)), map[string]any{"run": load})
	if err != nil {
		return false, fmt.Errorf("neo4j: reading load %q: %w", load, err)
	}
	if len(recs) == 0 {
		return false, nil
	}
	done, _ := recs[0]["complete"].(bool)
	return done, nil
}

// Unanswered returns the identity questions this load carries.
//
// They are the Duplicate nodes, which findings.go deliberately does not turn
// into an edge between the two entities: "a MAY_BE_SAME_AS relationship is
// traversable, and an agent that traverses it has been handed a claim". This is
// the other half of that decision. Keeping the question off the graph only pays
// if there is a way to ask it, and until now there was not — so the honest
// reading of a graph loaded here was that nothing was in doubt.
//
// An empty about returns all of them, rather than the literal "all" the
// hand-written query used as its sentinel: "all" is a plausible name for a
// table, a column or a flag, and a filter that stops filtering for one legal
// input is worse than no filter.
//
// A load written with Options.SkipFindings holds no Duplicate nodes and answers
// nothing here. That is the buyer's own decision and this cannot second-guess
// it, but a caller who has to tell "nothing is in doubt" from "the doubts were
// not imported" can: the run node carries `_count_duplicates`, which is what
// the job found rather than what was loaded.
func (l *Loader) Unanswered(ctx context.Context, load, about string) ([]recall.Question, error) {
	stmt, err := l.unansweredCypher()
	if err != nil {
		return nil, err
	}
	recs, err := l.read(ctx, stmt, map[string]any{"run": load, "about": strings.ToLower(about)})
	if err != nil {
		return nil, fmt.Errorf("neo4j: unanswered questions about %q in load %q: %w", about, load, err)
	}
	out := make([]recall.Question, 0, len(recs))
	for _, r := range recs {
		out = append(out, recall.Question{
			Signal: alchemy.DuplicateSignal(str(r["signal"])), Subject: str(r["subject"]), Detail: str(r["detail"]),
			Left: str(r["lname"]), Right: str(r["rname"]),
		})
	}
	return out, nil
}

// unansweredCypher reads the identity questions. See findCypher for why it is
// a builder.
//
// It matches every field a person would recognise the pair by rather than the
// detail alone: alchemy renders the pair into Subject, states the case in
// Detail, and keeps each side's name separately, so "touching a subject" is
// four properties and the hand-written query searched one of them.
func (l *Loader) unansweredCypher() (string, error) {
	scope, err := l.scope()
	if err != nil {
		return "", err
	}
	base, _ := quoteIdent(l.opts.BaseLabel)
	dupLabel, err := quoteIdent(l.internalLabel("Duplicate"))
	if err != nil {
		return "", err
	}
	return scope + fmt.Sprintf(
		"MATCH (d:%[1]s:%[2]s {%[3]s: $run}) "+
			"WHERE $about = '' OR toLower(d.%[4]s) CONTAINS $about OR toLower(d.%[5]s) CONTAINS $about "+
			"OR toLower(d.%[6]s) CONTAINS $about OR toLower(d.%[7]s) CONTAINS $about "+
			"RETURN d.%[8]s AS signal, d.%[4]s AS subject, d.%[5]s AS detail, "+
			"d.%[6]s AS lname, d.%[7]s AS rname ORDER BY subject, detail",
		base, dupLabel, l.prop(keyRun), l.prop(keySubject), l.prop(keyDetail),
		l.prop(keyLeftName), l.prop(keyRightName), l.prop(keySignal)), nil
}

// str and num read one column out of a driver record.
//
// Both are total rather than checked, and the reason is what a failure would
// look like either way. Every property read here is written by this package on
// every record that has it, so a wrong type is a bug in the writer and not in a
// buyer's data; a read that returned an error for it would make every call site
// handle a case that cannot happen, and a read that panicked would take an
// agent down over one malformed node. An absent property reads as the zero
// value, which for `_chunk` is 0 — legible, and never -1, so a citation built
// from one resolves or says it does not.
func str(v any) string { s, _ := v.(string); return s }

// num narrows Bolt's only integer. Every count and offset this package writes
// goes out as int64 (see normalizeInt), so this is the one place it comes back.
func num(v any) int { n, _ := v.(int64); return int(n) }
