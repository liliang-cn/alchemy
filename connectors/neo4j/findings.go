package neo4j

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Findings — violations, duplicates, guesses and unread material — are loaded,
// beside the graph rather than inside it, and this is the argument.
//
// They describe the graph rather than being part of it, so the tempting answer
// is "not loaded; they are in the JSON". That answer breaks §5's obligation the
// moment the JSON and the graph part company, which is immediately: the buyer
// queries Neo4j, and the file with the numbers in it is on the laptop of
// whoever ran the import. "A run with 1180 edges and 400 violations is a
// failure that looks like a success, and without this block nobody would know"
// is a sentence about a *reader*, and after a load the reader is holding a
// graph.
//
// So they are loaded, and kept out of the graph proper by two rules:
//
//   - A finding is its own node, under its own internal label, linked to the
//     run and never to another finding. `MATCH (p:Person)-->(x)` cannot walk
//     into one.
//   - A Duplicate does not become an edge between the two nodes it names. A
//     `MAY_BE_SAME_AS` relationship is traversable, and an agent that
//     traverses it has been handed a claim; the finding says only that a
//     signal fired and nobody has decided. It hangs off the finding node as
//     two CANDIDATE edges instead, which is the same information stated as a
//     question rather than as an assertion.
//
// Two things are deliberately not loaded. Vectors: this is the connector for
// the graph store, an embedding belongs in the store bought for embeddings,
// and Report.SkippedVectors says how many were left so the omission is not
// silent. ModelCalls: what a job spent is a fact about the job, and the job is
// the service's — a cost record in the buyer's graph is a row nobody will ever
// join to.

// The two edge types that reach the run node. FOUND_IN is what a finding
// travels on and IN_RUN what source material does, so that a reader can ask
// for one without being handed the other.
const (
	linkFinding = "FOUND_IN"
	linkChunk   = "IN_RUN"
)

// findingID gives a finding a content-addressed identity, so that loading the
// same result twice merges each finding onto itself instead of accumulating a
// second copy. An index would have been simpler and wrong: a re-run whose
// findings came back in another order would rewrite every one of them.
func findingID(kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, p := range parts {
		fmt.Fprintf(h, "\x00%d:%s", len(p), p)
	}
	return kind + "-" + hex.EncodeToString(h.Sum(nil))[:16]
}

// writeAux writes one kind of non-graph node and links it to the run. The
// `extra` clause is where a kind adds its own edges; it is appended rather
// than folded in so that the shared part — the label, the merge key, the link
// to the run — has exactly one form.
//
// linkType is separate for chunks and for findings, and the separation was a
// bug before it was a decision: with one type, "what did this run find" —
// which is §5's question, the one a reader asks before trusting the graph —
// came back with the corpus mixed into the answer. A chunk is material the run
// read; a finding is something it wants to tell you.
func (l *Loader) writeAux(ctx context.Context, kind, linkType string, rows []any, extra string, rep *Report) error {
	if len(rows) == 0 {
		return nil
	}
	pre := l.opts.ReservedPrefix
	base, err := quoteIdent(l.opts.BaseLabel)
	if err != nil {
		return err
	}
	label, err := quoteIdent(l.internalLabel(kind))
	if err != nil {
		return err
	}
	runLabel, err := quoteIdent(l.internalLabel("Run"))
	if err != nil {
		return err
	}
	stmt := fmt.Sprintf(
		"UNWIND $rows AS row "+
			"MATCH (run:%[1]s:%[2]s {`%[3]s%[4]s`: $run}) "+
			"MERGE (f:%[1]s:%[5]s {`%[3]s%[6]s`: $run, `%[3]s%[4]s`: row.id}) "+
			"SET f += row.props "+
			"MERGE (f)-[:%[8]s]->(run) %[7]s",
		base, runLabel, pre, keyID, label, keyRun, extra, linkType)
	for _, b := range batches(seq(len(rows)), l.opts.BatchSize) {
		page := make([]any, 0, len(b))
		for _, i := range b {
			page = append(page, rows[i])
		}
		if err := l.runBatch(ctx, stmt, page); err != nil {
			return fmt.Errorf("%s findings: %w", kind, err)
		}
		rep.Batches++
	}
	return nil
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// writeChunks loads the source text a chunk index refers to.
//
// A record whose provenance says "chunk 14" and a store that cannot show you
// chunk 14 delivers half of §5b: the edge names its chunk and the name resolves
// to nothing. The chunks are nodes under the run, joined by index, and
// deliberately *not* joined to entities by an edge — because a relationship
// cannot have a relationship, so a FROM_CHUNK edge could exist for nodes and
// never for edges, and an affordance that works on half the graph reads as a
// bug in the other half.
func (l *Loader) writeChunks(ctx context.Context, p *plan, rep *Report) error {
	if l.opts.SkipChunks || len(p.res.Chunks) == 0 {
		return nil
	}
	pre := l.opts.ReservedPrefix
	rows := make([]any, 0, len(p.res.Chunks))
	for _, c := range p.res.Chunks {
		props := map[string]any{
			pre + "index": int64(c.Index), pre + "text": c.Text, pre + keySource: c.Source,
			pre + "strategy": c.Strategy, pre + "start": int64(c.Start), pre + "end": int64(c.End),
		}
		if c.Heading != "" {
			props[pre+"heading"] = c.Heading
		}
		rows = append(rows, map[string]any{
			"id":    fmt.Sprintf("chunk-%d", c.Index),
			"props": props,
		})
	}
	rep.Chunks = len(rows)
	return l.writeAux(ctx, "Chunk", linkChunk, rows, "", rep)
}

// writeFindings loads everything that describes the graph.
func (l *Loader) writeFindings(ctx context.Context, p *plan, rep *Report) error {
	if l.opts.SkipFindings {
		return nil
	}
	pre := l.opts.ReservedPrefix
	base, err := quoteIdent(l.opts.BaseLabel)
	if err != nil {
		return err
	}
	// link is the clause that attaches a finding to a node it names, when the
	// name resolves. OPTIONAL MATCH rather than MATCH: a violation may name a
	// subject that is precisely the thing the result does not contain, and a
	// finding that refused to load because its subject was missing would drop
	// the report of the very failure it was reporting.
	link := func(field, relType, alias string) string {
		return fmt.Sprintf(" WITH f, row OPTIONAL MATCH (%[4]s:%[1]s {`%[2]s%[3]s`: $run, `%[2]s%[5]s`: row.%[6]s}) "+
			"FOREACH (x IN CASE WHEN %[4]s IS NULL THEN [] ELSE [%[4]s] END | MERGE (f)-[:%[7]s]->(x))",
			base, pre, keyRun, alias, keyID, field, relType)
	}

	rows := make([]any, 0, len(p.res.Violations))
	for _, v := range p.res.Violations {
		props := map[string]any{pre + "kind": string(v.Kind), pre + "detail": v.Detail, pre + "subject": v.Subject}
		for k, val := range provenanceProps(v.Provenance, pre) {
			props[k] = val
		}
		rows = append(rows, map[string]any{
			"id": findingID("violation", string(v.Kind), v.Subject, v.Detail), "props": props, "subject": v.Subject,
		})
	}
	rep.Violations = len(rows)
	if err := l.writeAux(ctx, "Violation", linkFinding, rows, link("subject", "ABOUT", "e"), rep); err != nil {
		return err
	}

	rows = rows[:0]
	for _, d := range p.res.Duplicates {
		props := map[string]any{
			pre + "signal": string(d.Signal), pre + "subject": d.Subject, pre + "detail": d.Detail,
			pre + "left_name": d.Left.Name, pre + "right_name": d.Right.Name,
			pre + "left_type": d.Left.Type, pre + "right_type": d.Right.Type,
		}
		// Neither side's provenance is copied onto the finding: the CANDIDATE
		// edges reach the two nodes, and each of those carries its own. A copy
		// would be a second place for the same fact to be right.
		rows = append(rows, map[string]any{
			"id": findingID("duplicate", string(d.Signal), d.Left.ID, d.Right.ID), "props": props,
			"left": d.Left.ID, "right": d.Right.ID,
		})
	}
	rep.Duplicates = len(rows)
	if err := l.writeAux(ctx, "Duplicate", linkFinding, rows, link("left", "CANDIDATE", "a")+link("right", "CANDIDATE", "b"), rep); err != nil {
		return err
	}

	rows = rows[:0]
	for _, g := range p.res.Guesses {
		props := map[string]any{
			pre + "field": g.Field, pre + "chosen_as": g.ChosenAs, pre + "reason": g.Reason,
			pre + "alternatives": toAny(g.Alternatives),
		}
		for k, val := range provenanceProps(g.Provenance, pre) {
			props[k] = val
		}
		rows = append(rows, map[string]any{"id": findingID("guess", g.Field, g.ChosenAs), "props": props})
	}
	rep.Guesses = len(rows)
	if err := l.writeAux(ctx, "Guess", linkFinding, rows, "", rep); err != nil {
		return err
	}

	rows = rows[:0]
	for _, u := range p.res.Unread {
		rows = append(rows, map[string]any{
			"id": findingID("unread", u.Source, u.Locator),
			"props": map[string]any{
				pre + keySource: u.Source, pre + "locator": u.Locator, pre + "reason": u.Reason,
			},
		})
	}
	rep.Unread = len(rows)
	return l.writeAux(ctx, "Unread", linkFinding, rows, "", rep)
}
