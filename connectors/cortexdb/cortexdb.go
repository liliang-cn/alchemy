// Package cortexdb loads an alchemy.Result into a CortexDB instance.
//
// DESIGN.md §4 decided that alchemy returns and does not store, and named the
// price: "our own projects gain a thin write layer. That is a few hundred lines
// in one place, against a product nobody outside can adopt." This is that
// layer. It lives in the connectors module, so `github.com/liliang-cn/cortexdb`
// is not a dependency of alchemy, and nothing here is reachable from
// pkg/service, pkg/pipeline or cmd/alchemy — the dependency runs one way, and
// the core module's `require` block is the checkable form of the argument.
//
// CortexDB is the only target of the four that already has opinions about the
// same things alchemy has opinions about, so the interesting decisions are all
// about what this connector refuses to let it do:
//
//   - It never calls IngestDocument. That splits text with CortexDB's own
//     chunker, and alchemy has already chunked — under a named strategy that
//     travels in every record's provenance (§7.1). A store whose chunk
//     boundaries are fixed-size while the graph says "heading" is a graph whose
//     provenance is a lie.
//   - It never lets CortexDB embed the chunks. §5c: vectors "describe the text
//     that survived review", computed after it and by the model the caller
//     named. CortexDB's lexicalVectorForText is a token hash — a fine index and
//     not an embedding — and recomputing anything here would be a different
//     claim than the one the result makes.
//   - It never supplies a GraphRAGExtractor. Alchemy extracted under a required
//     ontology (§5); a second, unconstrained extraction pass would put edges in
//     the graph that no vocabulary checked.
//   - It never lets CortexDB resolve an entity by name. Endpoints are handed
//     over as "entity:"-prefixed node ids, which is CortexDB's own way of
//     saying "identity is already decided". §5 defers entity resolution to a
//     second release; letting the store fold two runs' names together would be
//     doing it anyway.
//
// What it does hand over is everything CortexDB is better at: edge identity,
// ontology canonicalisation, mention edges, chunk-id union on a repeated
// assertion, and the foreign key that refuses an edge with no endpoint.
package cortexdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/cortexdb/v2/pkg/core"
	cdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// Loader writes results into one CortexDB instance under one set of options.
type Loader struct {
	cortex *cdb.DB
	opts   Options
	// owned says whether Close should shut the database down. A caller who
	// handed us a handle they also use elsewhere would not thank us for
	// closing it.
	owned bool
}

// Open opens or creates a CortexDB at path and returns a Loader.
//
// No Embedder is configured, and that is the point rather than an omission: a
// DB with an embedder is one that can be asked to embed, and this connector
// must never be able to. The vectors it writes are the ones alchemy computed.
func Open(path string, o Options) (*Loader, error) {
	db, err := cdb.Open(cdb.DefaultConfig(path))
	if err != nil {
		return nil, fmt.Errorf("cortexdb: open %s: %w", path, err)
	}
	l := New(db, o)
	l.owned = true
	return l, nil
}

// New wraps a DB the caller already has.
func New(db *cdb.DB, o Options) *Loader {
	return &Loader{cortex: db, opts: o.withDefaults()}
}

func (l *Loader) Close() error {
	if !l.owned {
		return nil
	}
	return l.cortex.Close()
}

// Report says what a Load did. It is returned rather than logged because
// everything in it is a fact about the store the caller now has.
type Report struct {
	Run    string
	Digest string

	Entities  int
	Relations int
	Chunks    int
	// MentionEdges is how many (chunk)-[mentions]->(entity) edges CortexDB
	// wrote. It is the count that makes §5b's "from which chunk of which file"
	// answerable in CortexDB's own vocabulary rather than only in ours.
	MentionEdges int

	// SkippedRelations names edges not written because an endpoint was not in
	// the result.
	SkippedRelations []string
	// FusedRelations names the groups CortexDB's edge identity collapsed. Only
	// ever non-empty with Options.FuseParallelEdges.
	FusedRelations []string
	// ChunksWithoutVectors is chunks whose text was left out because the result
	// carried no embedding for them. CortexDB stores chunk text in a vector
	// row, and inventing a vector to carry the text is the recomputation this
	// connector refuses — so the text stays out and the number is reported.
	ChunksWithoutVectors int

	// Batches is how many writes it took, which is the number an operator needs
	// when a load dies halfway.
	Batches int
	// Replay is true when the run was already present with the same digest.
	Replay bool
}

// Load writes a whole result into the store.
//
// The sequence is the same shape as the Neo4j connector's and for the same
// reason (§8.4): a large result is many writes, so a load can fail with part of
// it written. A half-loaded store is survivable; one nobody can tell is
// half-loaded is not.
//
//  1. Everything checkable without the store is checked first, and the load is
//     refused before a single write if anything is wrong.
//  2. The target itself is checked, because a store can refuse what a result
//     cannot know about (checkStore).
//  3. A run marker is written. From that instant until the completion lands,
//     the store truthfully says it is mid-import, and Incomplete says which
//     runs are in that state.
//  4. Documents, then chunks, then entities, then relations — in that order
//     because an embedding needs its document, a mention edge needs its chunk,
//     and a relation needs its endpoints.
//  5. The completion is written, carrying the digest, §5's counts and the
//     findings.
func (l *Loader) Load(ctx context.Context, res alchemy.Result) (Report, error) {
	p, err := preflight(res, l.opts)
	if err != nil {
		return Report{}, err
	}
	rep := Report{
		Run: l.opts.RunID, Digest: p.digest,
		SkippedRelations: p.skipped, FusedRelations: p.fused,
	}

	if err := l.checkStore(ctx); err != nil {
		return rep, err
	}
	replay, err := l.claimRun(ctx, p, &rep)
	if err != nil {
		return rep, err
	}
	rep.Replay = replay

	if err := l.writeDocuments(ctx, p, &rep); err != nil {
		return rep, err
	}
	written, err := l.writeChunks(ctx, p, &rep)
	if err != nil {
		return rep, err
	}
	if err := l.writeEntities(ctx, p, written, &rep); err != nil {
		return rep, err
	}
	if err := l.writeRelations(ctx, p, written, &rep); err != nil {
		return rep, err
	}
	return rep, l.completeRun(ctx, p, &rep)
}

// checkStore refuses a target the load cannot honestly land in, before the
// first write rather than in the middle of the third batch.
//
// A CortexDB schema with strict enforcement declares the properties an object
// may carry and rejects the rest. Alchemy's provenance is a dozen properties
// that no buyer's schema declares — the store answers "object type \"System\"
// has no property \"_chunk\"" — so under a strict schema §5b's guarantee cannot
// be written at all. The two honest ways out are for the buyer to declare those
// properties or to relax the schema to `vocabulary`, and neither is a decision
// a connector gets to make on their behalf.
//
// It is refused rather than degraded because the degradation is the thing this
// whole package exists to prevent: a graph loaded with the provenance quietly
// dropped is a graph whose edges cannot say who made them, which is the product
// (§5b) rather than a nicety.
func (l *Loader) checkStore(ctx context.Context) error {
	active, err := l.cortex.ListOntologySchemas(ctx, cdb.OntologyListRequest{ActiveOnly: true})
	if err != nil {
		return fmt.Errorf("cortexdb: read active ontology: %w", err)
	}
	for _, s := range active.Schemas {
		if s.Enforcement == cdb.OntologyEnforcementStrict {
			return fmt.Errorf("%w: schema %q; declare them, or set its enforcement to %q",
				ErrStrictOntology, s.SchemaID, cdb.OntologyEnforcementVocabulary)
		}
	}
	return nil
}

// runMarker is what a run says about itself.
//
// It is a CortexDB *document* rather than a graph node because a graph node
// requires a vector, and the only vector this connector could put on a run
// marker is one it made up. Fabricating an embedding to hold bookkeeping is
// exactly what the rest of this file refuses to do for real text.
//
// A run is two documents, not one that gets updated: the marker, written before
// the first batch, and the completion, written after the last. Two because the
// Store a consumer holds offers Create, Get and Delete and no update — so
// "flip a flag" would be a delete and a create, and a crash between them would
// leave a half-loaded store with no marker at all, which is the one state worse
// than a half-loaded store that says so. Appending never has that window.
type runMarker struct {
	Digest   string          `json:"digest"`
	Started  time.Time       `json:"started_at"`
	Counts   alchemy.Counts  `json:"counts,omitempty"`
	Findings json.RawMessage `json:"findings,omitempty"`
}

func markerID(run string) string     { return runNodeID(run) }
func completionID(run string) string { return runNodeID(run) + ":complete" }

// claimRun answers "what does loading the same result twice do?".
//
//   - Same run, same digest: a replay. Every write below is an upsert keyed on
//     an id derived from the run, so it converges on what is already there —
//     which is what makes a crashed load finishable by running it again.
//   - Same run, different digest: refused. The caller is telling the store two
//     different things about one import and there is nothing in the data to
//     decide which is current.
//   - A different run: a different graph. Nothing is merged across runs.
func (l *Loader) claimRun(ctx context.Context, p *plan, rep *Report) (bool, error) {
	store := l.cortex.Vector()
	if doc, err := store.GetDocument(ctx, markerID(l.opts.RunID)); err == nil && doc != nil {
		var prev runMarker
		_ = json.Unmarshal([]byte(doc.Content), &prev)
		if prev.Digest != p.digest {
			return false, fmt.Errorf("%w: run %q holds a graph with digest %s, this result is %s; use a new RunID",
				ErrRunExists, l.opts.RunID, short(prev.Digest), short(p.digest))
		}
		return true, nil
	}
	body, err := json.Marshal(runMarker{Digest: p.digest, Started: time.Now().UTC()})
	if err != nil {
		return false, fmt.Errorf("cortexdb: render run marker: %w", err)
	}
	rep.Batches++
	return false, store.CreateDocument(ctx, &core.Document{
		ID: markerID(l.opts.RunID), Title: "alchemy run " + l.opts.RunID,
		Content: string(body), Author: author, Version: 1,
	})
}

// completeRun writes the numbers §5 obliges a graph to carry: "every returned
// graph is accompanied by the numbers needed to distrust it". They are in the
// store rather than left in the JSON, because a graph in CortexDB whose quality
// numbers are in a file on somebody's laptop is a graph you merely have.
//
// Its existence is also the answer to "is this run finished?": a marker with no
// completion beside it is a load that died halfway, which is one Get to find
// and one re-Load to fix.
func (l *Loader) completeRun(ctx context.Context, p *plan, rep *Report) error {
	store := l.cortex.Vector()
	if doc, err := store.GetDocument(ctx, completionID(l.opts.RunID)); err == nil && doc != nil {
		return nil
	}
	findings, err := json.Marshal(map[string]any{
		"violations": p.res.Violations, "duplicates": p.res.Duplicates,
		"guesses": p.res.Guesses, "unread": p.res.Unread,
		// Provenance.RuleSet is a name into Result.RuleSets, so the sets have to
		// travel with the graph or every record points at nothing. §5c: "a rule
		// is recorded with the decision that produced it, so a later reader can
		// see why the rule exists" — and the later reader is holding a store,
		// not the JSON.
		"rule_sets":         p.res.RuleSets,
		"skipped_relations": rep.SkippedRelations, "fused_relations": rep.FusedRelations,
		"chunks_without_vectors": rep.ChunksWithoutVectors,
	})
	if err != nil {
		return fmt.Errorf("cortexdb: render findings: %w", err)
	}
	body, err := json.Marshal(runMarker{
		Digest: p.digest, Started: time.Now().UTC(), Counts: p.res.Counts, Findings: findings,
	})
	if err != nil {
		return fmt.Errorf("cortexdb: render run marker: %w", err)
	}
	rep.Batches++
	return store.CreateDocument(ctx, &core.Document{
		ID: completionID(l.opts.RunID), Title: "alchemy run " + l.opts.RunID + " complete",
		Content: string(body), Author: author, Version: 1,
	})
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
