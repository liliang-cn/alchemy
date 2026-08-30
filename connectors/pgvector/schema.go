package pgvector

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// provDDL is alchemy.Provenance as columns, and it is spelled once so that
// entities, relations and violations cannot drift into three different answers
// about what provenance is.
//
// Every field is here, including the two that are easiest to lose. RuleSet
// names the standing policy the model had already been told about when this
// record was proposed and RuledBy names the one rule that acted on it; a store
// that drops them keeps the edge and loses which policy the model was working
// under, which is exactly the question §5b promises a reader can answer.
//
// prov_deterministic is a materialised column rather than a CASE over
// prov_producer. Producer.Deterministic() is the one implementation of that
// rule and it lives in Go; re-deriving it in SQL would be a second answer to a
// question that already has one, and the two would disagree the first time a
// producer is added. Materialising it means a buyer can write "filter to the
// half that was guessed" as a WHERE clause without owning the rule.
const provDDL = `
	prov_source        text NOT NULL,
	prov_chunk         int  NOT NULL,
	prov_producer      text NOT NULL,
	prov_deterministic boolean NOT NULL,
	prov_model         text NOT NULL,
	prov_ontology      text NOT NULL,
	prov_chunking      text NOT NULL,
	prov_confidence    double precision NOT NULL,
	prov_reviewed_by   text NOT NULL,
	prov_rule_set      text NOT NULL,
	prov_ruled_by      text NOT NULL`

// provCols is the same list as a projection, in the same order, for COPY and
// for the reads that rebuild an alchemy.Provenance.
const provCols = `prov_source, prov_chunk, prov_producer, prov_deterministic, prov_model, ` +
	`prov_ontology, prov_chunking, prov_confidence, prov_reviewed_by, prov_rule_set, prov_ruled_by`

// State is what a load row says about itself.
const (
	// stateLoading means rows are arriving, or arrived and then stopped. It is
	// the state a crashed load is left in, and the read views exclude it.
	stateLoading = "loading"
	// stateComplete is set by the last statement of a load, in its own
	// transaction. Nothing else writes it.
	stateComplete = "complete"
)

// Migrate creates the tables. It is idempotent and safe to run from every
// process that starts at once: the advisory lock serialises the DDL, so ten
// loaders starting together produce one set of tables rather than nine
// "already exists" crashes. It is separate from Open for that reason — a
// deployment should be able to start without every node racing on DDL.
func (l *Loader) Migrate(ctx context.Context) error {
	if err := l.requireSchema(ctx); err != nil {
		return err
	}
	if err := l.requireExtension(ctx); err != nil {
		return err
	}
	if err := l.withDDLLock(ctx, func(tx pgx.Tx) error {
		for _, stmt := range l.ddl() {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("pgvector: %s: %w", firstLine(stmt), err)
			}
		}
		// The chunk view is created last and from the column that is actually
		// there, because a view's shape is fixed when it is made and Postgres
		// will not let CREATE OR REPLACE drop a column from one. Migrating a
		// schema whose dimension was bound by an earlier run would otherwise
		// try to recreate the view without the embedding column and fail — so
		// a restart after the first load would refuse to start.
		mod, err := boundIn(ctx, tx, l.schema)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, chunkViewSQL(l.schema, mod > 0)); err != nil {
			return fmt.Errorf("pgvector: recreating loaded_chunks: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	// A configured dimension binds the column now. A zero one leaves it
	// unbound, and the first result carrying vectors binds it instead.
	if l.dim > 0 {
		return l.bindDimension(ctx, l.dim, "")
	}
	return nil
}

// requireSchema turns the least helpful error this connector can produce into
// the most helpful one. Without it a mistyped schema surfaces as "CREATE TABLE
// …loads (: schema does not exist" — the failing statement rather than the
// missing precondition, which is a minute of somebody's life every time.
//
// It does not create the schema. Creating one in a buyer's database is a
// surprise nobody asked for, and pkg/job's clustered store takes the same
// position about the same question; saying plainly what to run is the whole of
// what a connector owes here.
func (l *Loader) requireSchema(ctx context.Context) error {
	var n int
	err := l.pool.QueryRow(ctx, `SELECT count(*) FROM pg_namespace WHERE nspname = $1`, l.schema).Scan(&n)
	if err != nil {
		return fmt.Errorf("pgvector: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("pgvector: schema %q does not exist in this database; "+
			"run CREATE SCHEMA %s, or point Config.Schema at one that is there. "+
			"The connector does not create it: a schema appearing in a buyer's database "+
			"because a library felt like it is not a surprise worth saving one statement",
			l.schema, l.schema)
	}
	return nil
}

// requireExtension fails early and in words rather than letting the first
// CREATE TABLE fail with "type vector does not exist". Creating the extension
// is not this connector's business: it needs privileges a loader should not
// have, and a buyer whose DBA has a policy about extensions is entitled to be
// asked rather than surprised.
func (l *Loader) requireExtension(ctx context.Context) error {
	var n int
	err := l.pool.QueryRow(ctx, `SELECT count(*) FROM pg_extension WHERE extname = 'vector'`).Scan(&n)
	if err != nil {
		return fmt.Errorf("pgvector: %w", err)
	}
	if n == 0 {
		return errors.New("pgvector: the vector extension is not installed in this database; " +
			"run CREATE EXTENSION vector as a user that may, then migrate again")
	}
	return nil
}

// withDDLLock runs f in a transaction holding this schema's advisory lock, so
// that concurrent migrations and concurrent dimension bindings serialise
// against each other rather than racing.
func (l *Loader) withDDLLock(ctx context.Context, f func(pgx.Tx) error) error {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgvector: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "alchemy.pgvector."+l.schema); err != nil {
		return fmt.Errorf("pgvector: %w", err)
	}
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (l *Loader) ddl() []string {
	return []string{
		// The load is the unit of everything here: of identity, of
		// idempotency, and of visibility. Entity.ID is stable within one
		// result and says nothing across runs, so there is no key on which two
		// runs could be merged without inventing one — and inventing one is
		// how a store silently joins two graphs that disagree.
		l.q(`CREATE TABLE IF NOT EXISTS {s}.loads (
	id           text PRIMARY KEY,
	-- fingerprint is a digest of the graph this load carries. It is what makes
	-- loading the same result twice a no-op rather than a doubling.
	fingerprint  text NOT NULL,
	state        text NOT NULL,
	-- dimension and embed_model are this load's, not the schema's. The schema's
	-- is the embedding column's typmod, which is the only copy that cannot
	-- drift from the data.
	dimension    int  NOT NULL DEFAULT 0,
	embed_model  text NOT NULL DEFAULT '',
	started_at   timestamptz NOT NULL DEFAULT now(),
	completed_at timestamptz,
	-- §5's obligation travels with the graph: "every returned graph is
	-- accompanied by the numbers needed to distrust it". A store that keeps the
	-- edges and drops the counts has kept the part that looks good.
	counts       jsonb NOT NULL DEFAULT '{}'::jsonb,
	-- The findings that are read whole rather than joined against. Conflicts
	-- here are answered ones by construction: an unanswered conflict refuses
	-- the load (§7.3), and keeping the answered ones records that a person
	-- decided rather than that nothing was ever in question.
	rule_sets    jsonb NOT NULL DEFAULT '[]'::jsonb,
	conflicts    jsonb NOT NULL DEFAULT '[]'::jsonb,
	guesses      jsonb NOT NULL DEFAULT '[]'::jsonb,
	unread       jsonb NOT NULL DEFAULT '[]'::jsonb,
	model_calls  jsonb NOT NULL DEFAULT '[]'::jsonb
)`),
		// One complete load per fingerprint. Load checks first and returns
		// early, so this index is the backstop for the case the check cannot
		// cover: two processes loading the same result at the same time, where
		// both checks pass before either commits.
		//
		// Partial on state, because an abandoned load carrying the same
		// fingerprint must not block the retry that replaces it.
		l.q(`CREATE UNIQUE INDEX IF NOT EXISTS loads_fingerprint ON {s}.loads (fingerprint)
	WHERE state = '` + stateComplete + `'`),
		l.q(`CREATE INDEX IF NOT EXISTS loads_state ON {s}.loads (state, started_at)`),

		l.q(`CREATE TABLE IF NOT EXISTS {s}.chunks (
	load_id    text NOT NULL REFERENCES {s}.loads(id) ON DELETE CASCADE,
	idx        int  NOT NULL,
	source     text NOT NULL,
	strategy   text NOT NULL,
	heading    text NOT NULL,
	start_byte int  NOT NULL,
	end_byte   int  NOT NULL,
	body       text NOT NULL,
	-- The embedding model is on the chunk rather than only on the load because
	-- alchemy.Vector carries it per vector, and a result that embedded two
	-- chunks with two models is a fact a reader must be able to see.
	embed_model text NOT NULL DEFAULT '',
	PRIMARY KEY (load_id, idx)
)`),

		l.q(`CREATE TABLE IF NOT EXISTS {s}.entities (
	load_id    text NOT NULL REFERENCES {s}.loads(id) ON DELETE CASCADE,
	entity_id  text NOT NULL,
	type       text NOT NULL,
	name       text NOT NULL,
	attributes jsonb,` + provDDL + `,
	PRIMARY KEY (load_id, entity_id)
)`),
		l.q(`CREATE INDEX IF NOT EXISTS entities_chunk ON {s}.entities (load_id, prov_chunk)`),
		l.q(`CREATE INDEX IF NOT EXISTS entities_type ON {s}.entities (load_id, type)`),

		// No foreign key from a relation to an entity, deliberately. A relation
		// naming an entity the result does not contain is
		// ViolationDanglingRelation, and §7.3 puts violations on the side of
		// the line where the graph is still delivered: "attributable,
		// excludable, and the rest of the graph is usable without it". A
		// foreign key would turn a reported violation into a refused load and
		// take the usable rest of the graph with it.
		l.q(`CREATE TABLE IF NOT EXISTS {s}.relations (
	load_id    text NOT NULL REFERENCES {s}.loads(id) ON DELETE CASCADE,
	-- seq is the relation's position in Result.Relations. It is the key
	-- because two edges of the same type between the same pair, extracted from
	-- two chunks, are two facts with two provenances and not one row; and it
	-- is the caller's index rather than a sequence so that replaying a load
	-- writes the same rows.
	seq        int  NOT NULL,
	from_id    text NOT NULL,
	to_id      text NOT NULL,
	type       text NOT NULL,
	attributes jsonb,` + provDDL + `,
	PRIMARY KEY (load_id, seq)
)`),
		l.q(`CREATE INDEX IF NOT EXISTS relations_from ON {s}.relations (load_id, from_id)`),
		l.q(`CREATE INDEX IF NOT EXISTS relations_to ON {s}.relations (load_id, to_id)`),
		l.q(`CREATE INDEX IF NOT EXISTS relations_chunk ON {s}.relations (load_id, prov_chunk)`),

		l.q(`CREATE TABLE IF NOT EXISTS {s}.violations (
	load_id text NOT NULL REFERENCES {s}.loads(id) ON DELETE CASCADE,
	seq     int  NOT NULL,
	kind    text NOT NULL,
	detail  text NOT NULL,
	subject text NOT NULL,` + provDDL + `,
	PRIMARY KEY (load_id, seq)
)`),

		// Duplicates are a table rather than a jsonb blob on the load because
		// "this node may be the same as that one" is a fact a reader needs
		// beside the node at query time, joinable on entity_id. The two sides'
		// provenance is jsonb because it is read whole by a person looking at
		// the pair, never filtered on.
		l.q(`CREATE TABLE IF NOT EXISTS {s}.duplicates (
	load_id    text NOT NULL REFERENCES {s}.loads(id) ON DELETE CASCADE,
	seq        int  NOT NULL,
	signal     text NOT NULL,
	subject    text NOT NULL,
	detail     text NOT NULL,
	left_id    text NOT NULL,
	left_type  text NOT NULL,
	left_name  text NOT NULL,
	left_prov  jsonb NOT NULL,
	right_id   text NOT NULL,
	right_type text NOT NULL,
	right_name text NOT NULL,
	right_prov jsonb NOT NULL,
	PRIMARY KEY (load_id, seq)
)`),
		l.q(`CREATE INDEX IF NOT EXISTS duplicates_left ON {s}.duplicates (load_id, left_id)`),
		l.q(`CREATE INDEX IF NOT EXISTS duplicates_right ON {s}.duplicates (load_id, right_id)`),

		// The views are the mechanism that makes a half-written load
		// unreadable rather than merely unlikely. A buyer who queries the
		// tables directly can see a load in progress; a buyer who queries
		// these cannot, and these are what the connector's own reads use.
		l.q(`CREATE OR REPLACE VIEW {s}.loaded_entities AS
	SELECT e.* FROM {s}.entities e JOIN {s}.loads l ON l.id = e.load_id
	WHERE l.state = '` + stateComplete + `'`),
		l.q(`CREATE OR REPLACE VIEW {s}.loaded_relations AS
	SELECT r.* FROM {s}.relations r JOIN {s}.loads l ON l.id = r.load_id
	WHERE l.state = '` + stateComplete + `'`),
		l.q(`CREATE OR REPLACE VIEW {s}.loaded_duplicates AS
	SELECT d.* FROM {s}.duplicates d JOIN {s}.loads l ON l.id = d.load_id
	WHERE l.state = '` + stateComplete + `'`),
		l.q(`CREATE OR REPLACE VIEW {s}.loaded_violations AS
	SELECT v.* FROM {s}.violations v JOIN {s}.loads l ON l.id = v.load_id
	WHERE l.state = '` + stateComplete + `'`),
	}
}

// boundIn reads the width the embedding column is declared at, through the
// catalog rather than a ::regclass cast, so that asking before the table exists
// is "not bound" rather than an error. Migrate has to be able to ask on a
// completely empty schema.
func boundIn(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, schema string) (int, error) {
	const sql = `SELECT a.atttypmod FROM pg_attribute a
	JOIN pg_class c ON c.oid = a.attrelid
	JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = $1 AND c.relname = 'chunks' AND a.attname = 'embedding' AND NOT a.attisdropped`
	var mod int
	err := q.QueryRow(ctx, sql, schema).Scan(&mod)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("pgvector: %w", err)
	}
	if mod < 0 {
		// An unconstrained `vector` column, which pgvector cannot index. The
		// connector never creates one, so reporting 0 says "not bound" and is
		// true of anything this package made.
		return 0, nil
	}
	return mod, nil
}

// chunkViewSQL renders the chunk view with or without the embedding column.
//
// The column list is written out rather than SELECT *, and that is the whole
// reason this is a function. A view's columns are fixed when it is created, so
// a SELECT * view made before the embedding column exists would keep hiding it
// afterwards — a store that has vectors and a view that says it does not.
// CREATE OR REPLACE VIEW may append columns and may not reorder them, so
// embedding goes last and the binding recreates the view in place.
func chunkViewSQL(schema string, withEmbedding bool) string {
	cols := "c.load_id, c.idx, c.source, c.strategy, c.heading, c.start_byte, c.end_byte, c.body, c.embed_model"
	if withEmbedding {
		cols += ", c.embedding"
	}
	sql := `CREATE OR REPLACE VIEW ` + schema + `.loaded_chunks AS
	SELECT ` + cols + ` FROM ` + schema + `.chunks c JOIN ` + schema + `.loads l ON l.id = c.load_id
	WHERE l.state = '` + stateComplete + `'`
	return sql
}
