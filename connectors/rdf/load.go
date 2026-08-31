package rdf

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// The load marker, and the one thing about it that is RDF's rather than
// anybody's preference.
//
// In a property graph a marker is a node and finishing a load is SET
// complete = true. There is no SET here. A graph is a set of triples and INSERT
// only adds, so writing al:complete true beside an al:complete false leaves the
// load asserting both — finished and unfinished at once, with a reader entitled
// to believe either. Every value on the marker that changes over the life of a
// load therefore has to be deleted before it is written, which is what
// replaceValues does and why it exists at all.

// replaceValues sets predicates that must hold exactly one value.
//
// Each predicate is cleared and re-asserted in one SPARQL Update request, so
// there is no window in which the marker says nothing — a reader arriving
// between two requests would see a load with no completion flag, which every
// query in recall.go reads as "not finished" and would answer nothing for.
func (l *Loader) replaceValues(ctx context.Context, graph string, subj term, pairs ...pair) error {
	if len(pairs) == 0 {
		return nil
	}
	g := iri(graph)
	if err := firstErr(append([]term{g, subj}, termsOf(pairs)...)...); err != nil {
		return err
	}
	var b strings.Builder
	for i, p := range pairs {
		fmt.Fprintf(&b, "DELETE WHERE { GRAPH %s { %s %s ?v%d } };\n", g.text, subj.text, p.p.text, i)
	}
	fmt.Fprintf(&b, "INSERT DATA { GRAPH %s {", g.text)
	for _, p := range pairs {
		fmt.Fprintf(&b, " %s %s %s .", subj.text, p.p.text, p.o.text)
	}
	b.WriteString(" } }")
	l.count()
	return l.update(ctx, b.String())
}

func termsOf(pairs []pair) []term {
	out := make([]term, 0, 2*len(pairs))
	for _, p := range pairs {
		out = append(out, p.p, p.o)
	}
	return out
}

// count records one round trip. Every request this package makes goes through
// it, so Report.Requests is the answer to "how much work does a failure lose?"
// rather than an estimate.
func (l *Loader) count() {
	if l.report != nil {
		l.report.Requests++
	}
}

// claimLoad decides what a second load of the same name means, and is the only
// place that decides it.
//
//   - Same name, same digest: a replay. RDF makes this the cheapest case there
//     is — every statement is already in the set, so re-writing them changes
//     nothing — which is what makes a crashed load finishable by running the
//     same command again.
//   - Same name, different digest: refused. The caller is telling the store two
//     different things about one import and nothing in the data decides which
//     is current. Replace is how a caller says so on purpose.
//   - A different name: a different graph, in the literal sense. Nothing is
//     merged across loads, ever.
func (l *Loader) claimLoad(ctx context.Context, digest string, replace bool) (replay, done bool, err error) {
	graph := l.loadIRI(l.opts.RunID)
	prev, complete, found, err := l.marker(ctx, graph)
	if err != nil {
		return false, false, err
	}
	if found {
		switch {
		case prev == digest:
			replay, done = true, complete
		case replace || l.opts.Overwrite:
			if err := l.dropLoad(ctx, graph); err != nil {
				return false, false, err
			}
		default:
			// Both sentinels: sink.ErrExists is what a caller asks when it does
			// not care which store answered, ErrRunExists is what a caller of
			// this package matches on.
			return false, false, fmt.Errorf("%w: %w: load %q holds a graph with digest %s, this result is %s; "+
				"use a new RunID, or Options.Overwrite to replace it",
				sink.ErrExists, ErrRunExists, l.opts.RunID, short(prev), short(digest))
		}
	}
	// A load that is already there and finished is left exactly as it is.
	// Rewriting the marker would set al:complete false on a graph that is
	// complete, which is the one moment a reader could see a whole load
	// claiming to be partial.
	if replay && done {
		return replay, done, nil
	}
	// Written incomplete and only later completed, so the window in which the
	// graph is partial is a window in which the graph says so.
	return replay, done, l.replaceValues(ctx, graph, iri(graph),
		pair{iri(rdfType), iri(clLoad)},
		pair{iri(pLoad), lit(l.opts.RunID)},
		pair{iri(pDigest), lit(digest)},
		pair{iri(pComplete), boolLit(false)},
		pair{iri(pStartedAt), lit(time.Now().UTC().Format(time.RFC3339))},
	)
}

// marker reads a load's own record out of the graph it describes.
//
// One query answers both halves — which graph this is and whether it finished —
// because the marker's subject is the graph IRI. See loadIRI.
func (l *Loader) marker(ctx context.Context, graph string) (digest string, complete, found bool, err error) {
	g := iri(graph)
	if g.err != nil {
		return "", false, false, g.err
	}
	q := fmt.Sprintf("SELECT ?digest ?complete WHERE { GRAPH %[1]s { %[1]s <%[2]s> ?digest ; <%[3]s> ?complete } }",
		g.text, pDigest, pComplete)
	l.count()
	rows, err := l.query(ctx, q)
	if err != nil {
		return "", false, false, fmt.Errorf("rdf: reading the marker of <%s>: %w", graph, err)
	}
	if len(rows) == 0 {
		return "", false, false, nil
	}
	return rows[0]["digest"].Value, rows[0]["complete"].Value == "true", true, nil
}

// dropLoad removes everything one load wrote, in one statement.
//
// This is the one place where a triple store is plainly better at something
// than a property graph: neo4j deletes a run in bounded bites because a single
// DETACH DELETE over four hundred thousand nodes is a transaction the server
// has to hold in memory, and here it is DROP GRAPH — the store's own bulk
// operation over a partition it maintains. Named graphs earn their place on
// this line.
//
// SILENT because a load that is not there is the answer the caller wanted.
func (l *Loader) dropLoad(ctx context.Context, graph string) error {
	g := iri(graph)
	if g.err != nil {
		return g.err
	}
	l.count()
	return l.update(ctx, "DROP SILENT GRAPH "+g.text)
}

// completeLoad flips the marker and writes the numbers §5 obliges a graph to
// carry: "every returned graph is accompanied by the numbers needed to
// distrust it". They are on the load marker rather than left in the JSON,
// because a graph in a store whose quality numbers are in a file on somebody's
// laptop is a graph you merely have.
//
// The counts go through replaceValues like the flag, although they do not
// change within one load. A re-load under Replace writes a second set into a
// graph that was dropped, so they would in fact be single-valued either way —
// but a count that could ever be two values is a count no query can read, and
// the cost of being sure is one request that was already being made.
func (l *Loader) completeLoad(ctx context.Context, digest string, c alchemy.Counts) error {
	graph := l.loadIRI(l.opts.RunID)
	pairs := []pair{
		{iri(pComplete), boolLit(true)},
		{iri(pFinishedAt), lit(time.Now().UTC().Format(time.RFC3339))},
		{iri(pDigest), lit(digest)},
	}
	// Every field is written, including the zeros: a missing predicate reads as
	// "this loader did not know about that number", which is a different claim
	// from "that number was nought".
	for _, cp := range []struct {
		pred string
		n    int
	}{
		{countPreds.Entities, c.Entities}, {countPreds.Relations, c.Relations},
		{countPreds.Chunks, c.Chunks}, {countPreds.Vectors, c.Vectors},
		{countPreds.Deterministic, c.Deterministic}, {countPreds.Inferred, c.Inferred},
		{countPreds.Violations, c.Violations}, {countPreds.Conflicts, c.Conflicts},
		{countPreds.Guesses, c.Guesses}, {countPreds.Duplicates, c.Duplicates},
		{countPreds.ChunksEmpty, c.ChunksEmpty}, {countPreds.ChunksUnread, c.ChunksUnread},
		{countPreds.Dropped, c.Dropped},
	} {
		pairs = append(pairs, pair{iri(cp.pred), intLit(cp.n)})
	}
	return l.replaceValues(ctx, graph, iri(graph), pairs...)
}

// finished reports whether one load is present and complete. It is the read
// side of the invariant claimLoad and completeLoad maintain between them, and
// it is what tells recall.ErrNoLoad from recall.ErrNoCitation.
func (l *Loader) finished(ctx context.Context, load string) (bool, error) {
	_, complete, found, err := l.marker(ctx, l.loadIRI(load))
	if err != nil {
		return false, err
	}
	return found && complete, nil
}

func short(digest string) string {
	if digest == "" {
		return "(none)"
	}
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}
