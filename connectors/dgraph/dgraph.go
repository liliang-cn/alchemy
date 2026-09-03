// Package dgraph loads an alchemy.Result into Dgraph, and reads one back.
//
// It is the sixth store and the second kind of answer to the same question the
// rdf connector opens with: an alchemy relation carries eleven provenance
// fields (DESIGN.md §5b), and where do they go?
//
// # What Dgraph brings that a triple store does not
//
// A FACET — a typed key/value pair attached to one edge. Measured against a
// live server before any of this was written:
//
//	<nadia> <ceoOf> <halcyon> (producer="llm-extract", source="halcyon.pdf",
//	                            chunk=20, confidence=0.81)
//
// reads back as ceoOf|producer "llm-extract", ceoOf|chunk 20 (an integer),
// ceoOf|confidence 0.81 (a float). No reification, no quoted triples, no
// singleton predicates — and the numbers come back as numbers, which in RDF
// costs a datatype IRI on every literal and a decoder that does not drop it.
// This is the shape alchemy has wanted since §5b was written.
//
// # What it costs, and it is the same cost every store pays
//
// FACETS OVERWRITE, SILENTLY. Measured: writing (producer, source, chunk) onto
// an edge that already carried (producer, source, chunk, ontology, reviewedBy,
// ruleSet) leaves the second write's three and drops the other three, and the
// server answers "Success". So one (subject, predicate, object) holds exactly
// one provenance here, which is CortexDB's identity rule arrived at from a
// different direction and the same rule the rdf connector states for a quoted
// triple: two records under one edge would put two sources on it with nothing
// saying which goes with which.
//
// pkg/sink already folds the entity records that share an ID before this
// connector is handed any of them. Relations are this connector's own problem
// and are grouped here, with the second and later members reported in
// Report.MergedRelations rather than written and lost — the same answer
// connectors/rdf gives, for the same reason.
//
// # The thing that will bite an implementer, and did
//
// DGRAPH ANSWERS HTTP 200 WHEN IT REFUSES. Measured, on both endpoints:
//
//	POST /mutate  "this is not rdf at all"  -> 200 {"errors":[{"message":"while lexing..."}]}
//	POST /query   "{ q(func: nonsense(x))" -> 200 {"errors":[{"message":"...not valid."}]}
//
// A connector that checked resp.StatusCode would write nothing, read nothing,
// and report success — for a whole corpus, with no error anywhere. Every
// request in http.go therefore parses the body and treats a non-empty errors
// array as the failure it is, and a test holds that in place by sending
// deliberate garbage.
//
// The second trap is smaller and has the same shape: N-Quads wants each
// statement on its own line, and a mutation whose newlines were collapsed is
// refused with "Expected newline or # after ." — again under a 200.
//
// # How a load is scoped
//
// Dgraph has no named graphs. Every node this connector writes carries a `run`
// predicate with an exact index, and every read filters on it; the node's
// identity is an `xid` of "<run>:<entity id>" with @upsert, which is what makes
// a re-load converge rather than duplicate. Measured: the same upsert twice
// returns the same uid and creates nothing.
//
// The eight recall primitives all land on something Dgraph indexes: Find is
// regexp(name, /needle/i) over a trigram index, Types is @groupby(etype),
// OfType is eq on the declared type, and the rest are lookups by xid.
package dgraph

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Loader writes results into one Dgraph alpha under one set of options.
type Loader struct {
	opts Options
	// rels memoises which relation predicates this process has already
	// declared. See ensureRelPred: it is a cache, never a source of truth.
	//
	// A pointer, because Begin copies the Loader to give a load its own RunID
	// and a copied mutex is a vet error and a real one — two copies guarding
	// two halves of one map. Sharing it is also what the cache means: the
	// schema belongs to the cluster, not to a load.
	rels *relCache
	// http is the client every request goes through. Never http.DefaultClient,
	// which has no timeout: a load that hung on one batch would hang forever,
	// holding a half-written graph open with nothing to cancel.
	http *http.Client
}

// New builds a Loader without touching the network.
// relCache is the shared memo of declared relation predicates.
type relCache struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (c *relCache) has(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[name]
}

func (c *relCache) add(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[name] = true
}

func New(o Options) *Loader {
	o = o.withDefaults()
	c := o.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	return &Loader{opts: o, http: c, rels: &relCache{seen: map[string]bool{}}}
}

// Open builds a Loader and checks that the store is reachable and its schema is
// present.
//
// The schema is asserted here rather than lazily on first write. Dgraph will
// happily accept a mutation naming a predicate it has no index for, and the
// write succeeds — it is the READS that come back empty, which is this
// package's least favourite shape of failure and the reason Ping does more than
// ping.
func Open(ctx context.Context, o Options) (*Loader, error) {
	l := New(o)
	if l.opts.Endpoint == "" {
		return nil, fmt.Errorf("dgraph: Options.Endpoint is required, e.g. http://host:47080")
	}
	if err := l.Ping(ctx); err != nil {
		return nil, err
	}
	if err := l.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// Close releases the HTTP client's idle connections. There is no session to
// end: every request here is independent, which is what lets a load be resumed
// by a different process after a crash.
func (l *Loader) Close() error {
	l.http.CloseIdleConnections()
	return nil
}

// Ping asks the store one question it must be able to answer.
func (l *Loader) Ping(ctx context.Context) error {
	if _, err := l.query(ctx, `{ q(func: has(`+l.pred(keyRun)+`), first: 1) { uid } }`); err != nil {
		return fmt.Errorf("dgraph: cannot reach %s: %w", l.opts.Endpoint, err)
	}
	return nil
}

// pred renders one of this connector's own predicate names.
//
// Everything alchemy writes is prefixed, and the prefix is not decoration: a
// Dgraph alpha is one namespace for every predicate any writer has ever used,
// so an unprefixed `source` would be shared with whatever else the buyer runs
// against this cluster — and a predicate's index and type are global. Two
// writers disagreeing about whether `source` is a string or a uid is a schema
// conflict that surfaces as somebody else's data disappearing from a query.
func (l *Loader) pred(name string) string { return l.opts.Prefix + name }

// entityXID is a node's identity across loads: the run and the id the result
// gave it.
//
// alchemy.Entity.ID is stable within one result and says nothing across runs,
// so the run has to be in the key or a second import of a different corpus
// would converge onto the first's nodes. It is the same argument every
// connector makes; only the spelling is Dgraph's.
func entityXID(run, id string) string { return "alchemy:" + run + ":" + id }

// chunkXID names one chunk of one load.
func chunkXID(run string, index int) string {
	return "alchemy:" + run + ":chunk:" + itoa(index)
}

// runXID names the marker node that says this load exists and is finished.
func runXID(run string) string { return "alchemy:" + run + ":run" }

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// intLit and floatLit render numbers as N-Quads literals.
//
// They exist because a bare number is not a legal N-Quads object and Dgraph
// says so in the one place it is easy to miss: `uid(v) <chunk> 0 .` is refused
// with "Invalid input: 0 at lexText" — under HTTP 200, like everything else
// here. Facets are a different grammar and take the bare form, which is exactly
// how this went unnoticed until the conformance suite ran: the edges were fine
// and every entity failed.
//
// Typed rather than left to the schema to coerce, so that a predicate whose
// declaration is missing or was changed by another writer fails loudly instead
// of storing the digits as a string that no int filter will ever match.
func intLit(n int) string { return `"` + itoa(n) + `"^^<xs:int>` }

func floatLit(f float64) string {
	return `"` + strconv.FormatFloat(f, 'g', -1, 64) + `"^^<xs:float>`
}

func boolLit(b bool) string { return `"` + strconv.FormatBool(b) + `"^^<xs:boolean>` }

// nquad renders one statement, on its own line.
//
// Its own line is the whole reason this is a function. Dgraph's N-Quads parser
// refuses a mutation whose statements share a line with "Expected newline or #
// after ." — and answers 200 while doing it, so a connector that built its
// mutation with strings.Join(stmts, " ") would send a corpus into a store that
// wrote none of it and said Success.
func nquad(subject, predicate, object string) string {
	return subject + " <" + predicate + "> " + object + " .\n"
}

// literal renders a string as an N-Quads literal.
//
// Escaping is not optional here and the list is short but exact: a quote or a
// backslash in a name — "the 12500T \"heavy\" line" — ends the literal early
// and the rest of the corpus is parsed as syntax. Newlines and tabs are escaped
// rather than passed through because a raw newline inside a literal is the same
// bug as the one nquad exists to avoid, arriving from the data instead of from
// the code.
func literal(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}
