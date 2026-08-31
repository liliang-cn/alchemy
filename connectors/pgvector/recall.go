package pgvector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Loader is a recall.Reader.
//
// This connector is one of the two that had invented a retrieval shape before
// there was one to implement, so most of this file is adaptation rather than
// new machinery: the load is already the unit of everything here, the loaded_*
// views already hide a half-written import, and Graph already reads one load
// back whole.
//
// Search and Around are deliberately untouched and stay beside these four.
// They answer a different question with a different input — "which text is
// about this", from an embedding — and folding them into recall.Reader would
// mean every store that holds a graph and no vectors implements a method it can
// only refuse. Both surfaces exist, and the four below are the ones a store
// with no embeddings can also answer.
var _ recall.Reader = (*Loader)(nil)

// Find returns the entities of one load whose name contains name.
//
// position(lower(x) in lower(y)) rather than ILIKE, because ILIKE would make
// the caller's text a pattern: a search for "node_connections" would silently
// treat the underscore as "any character", and one for "50%" would match
// nothing in a way nobody would think to look at. Escaping it would work and
// would put the rule in this package rather than in Postgres.
func (l *Loader) Find(ctx context.Context, load, name string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("pgvector: limit = %d is not a number of anchors", limit)
	}
	// Ordered before the limit, so that asking for ten of a hundred matches
	// twice returns the same ten; without an ORDER BY that is the planner's
	// choice, which is not an order at all.
	//
	// The count comes back with the page, in one statement, because a second
	// query would count a store that had moved. COUNT(*) OVER () is evaluated
	// after the WHERE and before the LIMIT, which is exactly the number
	// wanted: how many matched, not how many were returned.
	const sql = `SELECT entity_id, type, name, count(*) OVER () AS total
	FROM {s}.loaded_entities
	WHERE load_id = $1 AND position(lower($2::text) in lower(name)) > 0
	ORDER BY name, entity_id LIMIT $3`
	rows, err := l.pool.Query(ctx, l.q(sql), load, name, limit)
	if err != nil {
		return recall.Found{}, fmt.Errorf("pgvector: find %q in load %q: %w", name, load, err)
	}
	defer rows.Close()
	found := recall.Found{Nodes: []recall.Node{}}
	for rows.Next() {
		var n recall.Node
		var total int
		if err := rows.Scan(&n.ID, &n.Type, &n.Name, &total); err != nil {
			return recall.Found{}, fmt.Errorf("pgvector: find %q in load %q: %w", name, load, err)
		}
		found.Nodes = append(found.Nodes, n)
		found.Total = total
	}
	return found, rows.Err()
}

// Claims returns every relation touching one entity, in either direction, with
// the provenance of the relation.
//
// Of the relation, which in this schema is not a thing that can go wrong by
// accident: prov_* is a column on the relations table, so a claim carries the
// assertion's source, chunk and producer because that is where they are. It is
// worth saying anyway, because it is what makes the answer comparable with the
// graph store's — where node and edge provenance are the same flat shape on two
// kinds of thing, and reading the wrong one returns plausible values for every
// row.
//
// The endpoints are joined outward and fall back to the ID. There is no foreign
// key from a relation to an entity here, deliberately: a relation naming an
// entity the result did not contain is ViolationDanglingRelation, which §7.3
// delivers rather than refuses. So an endpoint may genuinely have no row, and
// the honest rendering of that is the identifier the edge named — a claim about
// something this load does not describe, which is different from no claim.
func (l *Loader) Claims(ctx context.Context, load, id string) ([]recall.Claim, error) {
	// DISTINCT rather than one row per relation: two edges of the same type
	// between one pair, extracted from two chunks, are two rows here on purpose
	// (see relations.seq), and they are two claims — but two that agree on
	// every field below render as one sentence, and a pack that printed it
	// twice would be telling a reader the corpus said it twice.
	const sql = `SELECT DISTINCT coalesce(f.name, r.from_id) AS subject, r.type AS rel,
	coalesce(t.name, r.to_id) AS object, r.from_id, r.to_id,
	r.prov_source, r.prov_chunk, r.prov_producer
FROM {s}.loaded_relations r
LEFT JOIN {s}.loaded_entities f ON f.load_id = r.load_id AND f.entity_id = r.from_id
LEFT JOIN {s}.loaded_entities t ON t.load_id = r.load_id AND t.entity_id = r.to_id
WHERE r.load_id = $1 AND (r.from_id = $2::text OR r.to_id = $2::text)
ORDER BY rel, object, subject, r.prov_source, r.prov_chunk`
	rows, err := l.pool.Query(ctx, l.q(sql), load, id)
	if err != nil {
		return nil, fmt.Errorf("pgvector: claims about %q in load %q: %w", id, load, err)
	}
	defer rows.Close()
	out := []recall.Claim{}
	for rows.Next() {
		var subject, rel, object string
		// The IDs are the columns the edge is actually keyed on, taken straight
		// rather than through the join that produced the names. coalesce above
		// falls back to the ID for an endpoint this load does not describe --
		// ViolationDanglingRelation, which §7.3 delivers rather than refuses --
		// so a caller reading only the names cannot tell that case from a node
		// whose name happens to be an identifier. These two always mean the ID.
		var from, to recall.Endpoint
		var p alchemy.Provenance
		if err := rows.Scan(&subject, &rel, &object, &from.ID, &to.ID,
			&p.Source, &p.Chunk, &p.Producer); err != nil {
			return nil, fmt.Errorf("pgvector: claims about %q in load %q: %w", id, load, err)
		}
		// Through recall.NewClaim, so that stated-or-inferred is
		// alchemy.Producer.Deterministic and not the prov_deterministic column
		// sitting in the same row. That column exists so a buyer can write "the
		// half that was guessed" as their own WHERE clause without owning the
		// rule; it is also the answer the rule gave on the day of the import,
		// and a reader deciding how far to trust a sentence today should be
		// told today's answer.
		from.Name, to.Name = subject, object
		out = append(out, recall.NewClaim(from, to, rel, p))
	}
	return out, rows.Err()
}

// Cite resolves one [source#index] marker against one load.
//
// Both halves have to match. Matching the index alone would work — a job's
// chunk indexes are unique across the whole job — and is the wrong shape: a
// caller who passed the right number with the wrong file would be handed the
// other file's text, with nothing about the answer looking wrong.
//
// Three outcomes, not two, and the third is the common one. ErrNoChunk when
// the marker carries no chunk number, which is an ordinary answer: the producer
// did not work in chunks and there was never any text under this claim.
// ErrNoCitation when the load holds no such chunk, which IS a failure — a claim
// pointing at material that was not loaded. ErrNoLoad when there is no finished
// load of that name, which is a caller naming the wrong import, the bug the
// load parameter exists for arriving as a typo instead of as a wrong answer.
// Never a zero Citation for any of the three.
//
// The first two were one error until a measurement separated them: across
// thirty runs of an agent over a graph loaded here, seven of thirteen citation
// attempts were against a graph-import source whose chunk is -1, and every one
// was refused with the sentence reserved for evidence that does not check out.
// All seven were false alarms — §5b ranks a machine reading something that
// already asserted a fact ABOVE a model reading prose — and the agents cited
// the claims regardless, which is a tool teaching its caller to ignore it.
func (l *Loader) Cite(ctx context.Context, load, source string, index int) (recall.Citation, error) {
	// A negative index is not a lookup that failed, it is a marker with no chunk
	// number in it, and there is nothing to ask the store for. It goes through
	// whyNoCitation anyway, because the load is checked before anything else:
	// answering "this claim has no text, and that is fine" for an import that is
	// not here would be an ordinary answer handed back for a caller's mistake,
	// which is the one thing the load parameter exists to prevent.
	if index < 0 {
		return recall.Citation{}, l.whyNoCitation(ctx, load, source, index)
	}
	const sql = `SELECT source, idx, start_byte, end_byte, body FROM {s}.loaded_chunks
	WHERE load_id = $1 AND source = $2::text AND idx = $3`
	var c recall.Citation
	err := l.pool.QueryRow(ctx, l.q(sql), load, source, index).
		Scan(&c.Source, &c.Index, &c.Start, &c.End, &c.Text)
	if errors.Is(err, pgx.ErrNoRows) {
		return recall.Citation{}, l.whyNoCitation(ctx, load, source, index)
	}
	if err != nil {
		return recall.Citation{}, fmt.Errorf("pgvector: cite %s#%d in load %q: %w", source, index, load, err)
	}
	return c, nil
}

// whyNoCitation tells the two absences apart. loaded_chunks hides a load that
// has not committed its last statement, so an unfinished load and an absent one
// are the same answer here and are reported as the same thing: there is no
// finished load of that name to cite against.
func (l *Loader) whyNoCitation(ctx context.Context, load, source string, index int) error {
	var n int
	err := l.pool.QueryRow(ctx, l.q(`SELECT count(*) FROM {s}.loads WHERE id = $1 AND state = '`+stateComplete+`'`), load).Scan(&n)
	if err != nil {
		return fmt.Errorf("pgvector: reading load %q: %w", load, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %q is not a finished load in this schema; "+
			"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
			recall.ErrNoLoad, load)
	}
	// The claim never had a chunk, which Mark already says by rendering its
	// marker as [source] with no #n. It is an ordinary answer and deliberately
	// not ErrNoCitation: the two say opposite things about how far to trust the
	// claim, and conflating them refused every graph-import claim in the store
	// with the sentence reserved for evidence that does not check out.
	if index < 0 {
		return fmt.Errorf("%w: the claim citing %q carries no chunk number, so load %q holds no text "+
			"to quote for it — the claim is not weakened by that, and must not be reported as uncited",
			recall.ErrNoChunk, source, load)
	}

	return fmt.Errorf("%w: load %q holds no chunk %d of %q — the claim that cited it cannot be checked "+
		"against this import, and must not be offered as evidence from it",
		recall.ErrNoCitation, load, index, source)
}

// Unanswered returns the identity questions this load carries.
//
// They are the duplicates, which are a table here rather than a blob on the
// load precisely so that they can be asked about beside the records: "this node
// may be the same as that one" is a fact a reader needs at query time. This is
// the reader that needed it. Nothing in the answer is phrased as an assertion,
// because the finding says only that a signal fired and nobody has ruled.
//
// An empty about returns all of them, rather than a word like "all": a sentinel
// that is also a legal search term is a filter that silently stops filtering
// for one input, and "all" is a plausible name for a table or a column.
func (l *Loader) Unanswered(ctx context.Context, load, about string) ([]recall.Question, error) {
	// Every field a person would recognise the pair by, not the detail alone:
	// alchemy renders the pair into subject, states the case in detail, and
	// keeps each side's name separately.
	const sql = `SELECT signal, subject, detail, left_name, right_name FROM {s}.loaded_duplicates
	WHERE load_id = $1 AND ($2::text = ''
		OR position($2::text in lower(subject))    > 0
		OR position($2::text in lower(detail))     > 0
		OR position($2::text in lower(left_name))  > 0
		OR position($2::text in lower(right_name)) > 0)
	ORDER BY subject, detail`
	rows, err := l.pool.Query(ctx, l.q(sql), load, strings.ToLower(about))
	if err != nil {
		return nil, fmt.Errorf("pgvector: unanswered questions about %q in load %q: %w", about, load, err)
	}
	defer rows.Close()
	out := []recall.Question{}
	for rows.Next() {
		var q recall.Question
		if err := rows.Scan(&q.Signal, &q.Subject, &q.Detail, &q.Left, &q.Right); err != nil {
			return nil, fmt.Errorf("pgvector: unanswered questions about %q in load %q: %w", about, load, err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
