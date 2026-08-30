package neo4j

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
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

// The three edge types that reach the run node. FOUND_IN is what a finding
// travels on and IN_RUN what source material does, so that a reader can ask
// for one without being handed the other.
//
// STATED_IN is the third because a supersession is neither. "What did this run
// find wrong" must not come back with a correction in it -- nothing is wrong
// with a correction -- and "what material did this run read" must not either,
// because nobody showed the model a retirement; the run asserts it. A third
// question deserves a third edge, and the alternative was to make one of the
// first two answer something it was not asked.
const (
	linkFinding   = "FOUND_IN"
	linkChunk     = "IN_RUN"
	linkStatement = "STATED_IN"
)

// The edges that reach from a finding or a statement to the records it names.
//
// They were string literals at their call sites, which was survivable while
// nothing read them back. A read path makes it a defect: a traversal from an
// entity has to exclude every edge this connector wrote for its own
// bookkeeping, and a name that lives in a writer and again in a query is a
// name with two homes -- where the query is the copy that fails by silently
// matching one edge too many, and returns a duplicate report to an agent as
// though it were a claim about the world.
const (
	linkAbout      = "ABOUT"
	linkCandidate  = "CANDIDATE"
	linkRetires    = "RETIRES"
	linkReplacedBy = "REPLACED_BY"
)

// bookkeeping is every relationship type this connector writes that is not an
// extracted claim. It is the exclusion list a walk of the graph runs under,
// and it is a function over the constants rather than a literal list in a
// query so that adding an edge type and forgetting the walk is one edit rather
// than two.
func bookkeeping() []any {
	return toAny([]string{linkChunk, linkFinding, linkStatement, linkAbout, linkCandidate, linkRetires, linkReplacedBy})
}

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
			return fmt.Errorf("writing %s nodes: %w", kind, err)
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
func (l *Loader) writeChunks(ctx context.Context, batch []sink.Chunk, rep *Report) error {
	if l.opts.SkipChunks || len(batch) == 0 {
		return nil
	}
	pre := l.opts.ReservedPrefix
	rows := make([]any, 0, len(batch))
	for _, c := range batch {
		props := map[string]any{
			pre + keyIndex: int64(c.Index), pre + keyText: c.Text, pre + keySource: c.Source,
			pre + keyStrategy: c.Strategy, pre + keyStart: int64(c.Start), pre + keyEnd: int64(c.End),
		}
		if c.Heading != "" {
			props[pre+keyHeading] = c.Heading
		}
		rows = append(rows, map[string]any{
			"id":    fmt.Sprintf("chunk-%d", c.Index),
			"props": props,
		})
	}
	rep.Chunks += len(rows)
	return l.writeAux(ctx, "Chunk", linkChunk, rows, "", rep)
}

// writeRuleSets loads the standing policy the records were extracted under.
//
// It closes the one gap this connector shipped with. Every entity, relation and
// finding it writes can carry a `_rule_set` property, which alchemy defines as
// "the set's *name* … and not the set itself; the contents are on the result
// once, in Result.RuleSets, and the name is how a record points at them" — and
// nothing here loaded the sets, so the names pointed at nothing. The graph read
// as if a policy had been in force and gave no way to find out what it said, or
// on whose word, which is the half of §5b that survives a load only if somebody
// loads it.
//
// A set is a node under the run rather than a property of the run, keyed on the
// name a record already carries, so resolving a pointer is a MATCH rather than a
// scan and a string-split. Neo4j properties are flat, so the rules go in as two
// parallel lists — the names and the sentences the model was shown — in the
// order the set states them, which pkg/review already sorts. Two lists rather
// than one list of JSON blobs because a reader looking up "which rule is this
// _ruled_by" wants a list membership test, not a parse.
func (l *Loader) writeRuleSets(ctx context.Context, sets []alchemy.RuleSet, rep *Report) error {
	if len(sets) == 0 {
		return nil
	}
	pre := l.opts.ReservedPrefix
	rows := make([]any, 0, len(sets))
	for _, s := range sets {
		names := make([]any, 0, len(s.Rules))
		told := make([]any, 0, len(s.Rules))
		for _, r := range s.Rules {
			names = append(names, r.Name)
			told = append(told, r.Told)
		}
		rows = append(rows, map[string]any{
			"id": s.Name,
			"props": map[string]any{
				pre + keyName: s.Name, pre + "rule_names": names, pre + "rule_told": told,
			},
		})
	}
	rep.RuleSets = len(rows)
	// IN_RUN rather than FOUND_IN: a policy is not a finding about the graph,
	// it is source material the same way a chunk is — what the model was shown.
	return l.writeAux(ctx, "RuleSet", linkChunk, rows, "", rep)
}

// refKey renders an alchemy.Ref as the one string a content address can be
// taken over. It is the four fields Relation.Identity is a function of plus the
// entity's, in a fixed order, so that two Refs naming one record render alike
// and two naming different records cannot collide through a shared separator.
func refKey(r alchemy.Ref) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		r.Kind, r.ID, r.Type, r.From, r.To, r.Key)
}

// writeSupersessions loads what this result says is no longer true.
//
// It is loaded, and it is not acted on. alchemy states the rule and this is the
// step at which breaking it would cost a buyer something real: a producer able
// to delete another producer's fact by naming it is an unreviewed writer with
// write access to a graph somebody is already querying. So nothing is deleted,
// nothing is detached, and no property of the retired record changes. What is
// written is the claim -- what it retires, what replaces it, why, and on whose
// word -- which is the whole of what alchemy promises survives the pipeline.
//
// The two edges reach the records from the supersession node and never run
// between them, which is the rule a Duplicate is under above and for a sharper
// version of the same reason. A `SUPERSEDED_BY` relationship from the old node
// to the new one is traversable, and an agent that walked it would have been
// handed one producer's decision as though the store had made it. Hanging both
// off the claim states the same information as a claim.
//
// Both are OPTIONAL MATCH, and here that is the ordinary case rather than the
// careful one. Supersession.Retires "deliberately need not be present in this
// result": the record being retired is usually in a store from a run that
// finished last month, and it is never in this graph at all when it names a
// relation, because a relation here is an edge and an edge is not a node an id
// resolves to. It matches nothing then, the claim is still written with the id
// it named, and a reader can go and look. A connector that refused would make
// the field useless for the only case it exists for.
//
// Options.SkipFindings does not reach here. That option is a buyer saying they
// want the graph without the quality report; a retirement is not part of the
// quality report, and dropping it under a flag about findings would lose the
// statement to a setting nobody set for that reason.
func (l *Loader) writeSupersessions(ctx context.Context, batch []alchemy.Supersession, rep *Report) error {
	if len(batch) == 0 {
		return nil
	}
	pre := l.opts.ReservedPrefix
	base, err := quoteIdent(l.opts.BaseLabel)
	if err != nil {
		return err
	}
	link := func(field, relType, alias string) string {
		return fmt.Sprintf(" WITH f, row OPTIONAL MATCH (%[4]s:%[1]s {`%[2]s%[3]s`: $run, `%[2]s%[5]s`: row.%[6]s}) "+
			"FOREACH (x IN CASE WHEN %[4]s IS NULL THEN [] ELSE [%[4]s] END | MERGE (f)-[:%[7]s]->(x))",
			base, pre, keyRun, alias, keyID, field, relType)
	}

	rows := make([]any, 0, len(batch))
	for _, s := range batch {
		props := map[string]any{
			pre + "retires": s.Retires, pre + "reason": s.Reason,
			// The whole Ref and not only its id: which kind of record replaces
			// this one, and for an edge the four fields its identity is a
			// function of, so a reader holding the node can work out what it
			// names without going back to the result.
			pre + "by_kind": string(s.By.Kind), pre + "by_id": s.By.ID, pre + "by_type": s.By.Type,
			pre + "by_from": s.By.From, pre + "by_to": s.By.To, pre + "by_key": s.By.Key,
		}
		// The supersession's own provenance and not the superseding record's:
		// a reviewer may retire a record a model proposed, and those are two
		// claims by two parties.
		for k, val := range provenanceProps(s.Provenance, pre) {
			props[k] = val
		}
		rows = append(rows, map[string]any{
			"id": findingID("supersession", s.Retires, refKey(s.By), s.Reason), "props": props,
			"retires": s.Retires, "by": s.By.ID,
		})
	}
	rep.Supersessions += len(rows)
	return l.writeAux(ctx, "Supersession", linkStatement, rows,
		link("retires", linkRetires, "old")+link("by", linkReplacedBy, "new"), rep)
}

// writeFindings loads everything that describes the graph.
func (l *Loader) writeFindings(ctx context.Context, f sink.Findings, rep *Report) error {
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

	rows := make([]any, 0, len(f.Violations))
	for _, v := range f.Violations {
		props := map[string]any{pre + keyKind: string(v.Kind), pre + keyDetail: v.Detail, pre + keySubject: v.Subject}
		for k, val := range provenanceProps(v.Provenance, pre) {
			props[k] = val
		}
		rows = append(rows, map[string]any{
			"id": findingID("violation", string(v.Kind), v.Subject, v.Detail), "props": props, "subject": v.Subject,
		})
	}
	rep.Violations = len(rows)
	if err := l.writeAux(ctx, "Violation", linkFinding, rows, link("subject", linkAbout, "e"), rep); err != nil {
		return err
	}

	rows = rows[:0]
	for _, d := range f.Duplicates {
		props := map[string]any{
			pre + keySignal: string(d.Signal), pre + keySubject: d.Subject, pre + keyDetail: d.Detail,
			pre + keyLeftName: d.Left.Name, pre + keyRightName: d.Right.Name,
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
	if err := l.writeAux(ctx, "Duplicate", linkFinding, rows, link("left", linkCandidate, "a")+link("right", linkCandidate, "b"), rep); err != nil {
		return err
	}

	rows = rows[:0]
	for _, g := range f.Guesses {
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
	for _, u := range f.Unread {
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
