package rdf

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The two environment variables the live tests take, one per protocol.
//
// They skip without either so that `go test ./...` passes on a machine with no
// triple store — but a skipped test proves nothing, so the message says exactly
// what to set. Setting either runs the WHOLE suite against that store: the
// choice is per invocation rather than a subtest loop, because a store is a
// deployment and running both in one process would interleave two servers'
// failures under one test name.
const (
	envEndpoint = "ALCHEMY_TEST_GRAPHDB"
	// envSPARQL is a server that puts SPARQL 1.1 at /query and /update with no
	// repository layer — Oxigraph, or a Fuseki dataset root. It exists because
	// this package's doc has always claimed "nothing in it is GraphDB's" and a
	// portability claim with one store behind it is an untested claim.
	envSPARQL = "ALCHEMY_TEST_SPARQL"
)

// liveTarget fills in the endpoint, the protocol and — for RDF4J only — a
// repository made for this test.
//
// The two are not isolated the same way and they cannot be. Under RDF4J each
// test gets a private repository, which is also how the suite exercises a
// genuinely empty store: no vocabulary present, no index warmed, the state
// every first import is in. Oxigraph serves exactly one dataset per process,
// so tests there share it and are kept apart by the random RunID and its named
// graph, which is what every load is scoped by in production anyway. What is
// lost is the empty-store property, and it is lost rather than faked: a test
// that dropped the whole store between cases would be testing a gesture no
// deployment makes.
func liveTarget(t *testing.T, o Options) Options {
	t.Helper()
	if e := os.Getenv(envSPARQL); e != "" {
		o.Endpoint, o.Protocol, o.Repository = e, ProtocolSPARQL, ""
		return o
	}
	e := os.Getenv(envEndpoint)
	if e == "" {
		t.Skipf("no live triple store: set %s to a GraphDB base URL (e.g. %s=http://127.0.0.1:47200), "+
			"or %s to a SPARQL 1.1 server (e.g. %s=http://127.0.0.1:47878), to run this test",
			envEndpoint, envEndpoint, envSPARQL, envSPARQL)
	}
	o.Endpoint, o.Repository = e, testRepository(t, e)
	return o
}

// liveLoader opens a Loader against a repository created for this one test and
// deleted with it.
//
// A repository rather than a named graph, although a named graph would be
// enough to keep two tests apart. Two things are being exercised that a shared
// repository would not exercise: that this connector works against an empty
// store, with no vocabulary present and no index warmed, which is the state
// every first import is in; and the repository-creation route itself, which is
// the one place GraphDB's own JSON REST API and the RDF4J protocol diverge —
// the JSON one answers 400 (`Missing parameter Default namespaces for
// imports`) and the RDF4J one works. A test suite that used a repository
// somebody made by hand would never find that out.
func liveLoader(t *testing.T, o Options) *Loader {
	t.Helper()
	o = liveTarget(t, o)
	endpoint, repo := o.Endpoint, o.Repository
	if o.RunID == "" {
		o.RunID = "ld-" + randomName(t)
	}
	l, err := Open(context.Background(), o)
	if err != nil {
		t.Fatalf("Open(%s/%s): %v", endpoint, repo, err)
	}
	return l
}

// testRepository creates a private repository and tears it down.
//
// The configuration is an RDF4J repository config graph posted as Turtle, which
// is the working route. `ruleset "empty"` is deliberate and is not only about
// speed: this connector's whole argument about rdfs:domain and rdfs:range is
// that an inference licence changes what a store answers, and a test running
// under a reasoner would be testing the reasoner's conclusions rather than what
// this package wrote. `enable-context-index` is what makes a per-load named
// graph a lookup rather than a scan, which is the assumption every read in
// recall.go rests on.
func testRepository(t *testing.T, endpoint string) string {
	t.Helper()
	id := "alchemytest_" + randomName(t)
	config := fmt.Sprintf(`@prefix rep: <http://www.openrdf.org/config/repository#> .
@prefix sr: <http://www.openrdf.org/config/repository/sail#> .
@prefix sail: <http://www.openrdf.org/config/sail#> .
@prefix graphdb: <http://www.ontotext.com/config/graphdb#> .
[] a rep:Repository ; rep:repositoryID %q ;
   rep:repositoryImpl [ rep:repositoryType "graphdb:SailRepository" ;
     sr:sailImpl [ sail:sailType "graphdb:Sail" ;
       graphdb:base-URL "http://alchemy.example/" ; graphdb:ruleset "empty" ;
       graphdb:enable-context-index "true" ] ] .
`, id)
	u := strings.TrimSuffix(endpoint, "/") + "/repositories/" + id
	admin := New(Options{Endpoint: endpoint, Repository: id})
	if _, err := admin.do(context.Background(), http.MethodPut, u, "text/turtle", "", config); err != nil {
		t.Fatalf("creating repository %s: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := admin.do(context.Background(), http.MethodDelete, u, "", "", ""); err != nil {
			t.Errorf("deleting repository %s: %v", id, err)
		}
	})
	return id
}

// randomName gives each test its own repository. Random rather than derived
// from the test name so that a suite run with -count=2 does not have two
// iterations sharing a store.
func randomName(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// fixture is a small result that exercises every shape this loader has an
// opinion about: two producers, an attribute map with a nested value, aliases,
// a relation, a chunk with byte offsets, and one of each kind of finding.
func fixture() alchemy.Result {
	llm := alchemy.Provenance{
		Source: "architecture.pdf", Chunk: 14, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Ontology: "sds@3", Chunking: "heading", Confidence: 0.82,
	}
	ddl := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "sds@3"}
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "System", Name: "SuperAI", Aliases: []string{"Super AI", "SAI"},
				Attributes: map[string]any{"public": true, "since": 2024.0}, Provenance: llm},
			{ID: "e2", Type: "System", Name: "CortexDB", Provenance: ddl},
			{ID: "e3", Type: "Person", Name: "Ada",
				Attributes: map[string]any{"address": map[string]any{"city": "Wien"}}, Provenance: llm},
		},
		Relations: []alchemy.Relation{
			{From: "e1", To: "e2", Type: "USES", Provenance: llm},
			{From: "e3", To: "e1", Type: "WORKS_ON", Attributes: map[string]any{"since": "2024"}, Provenance: ddl},
		},
		Chunks: []alchemy.Chunk{{
			Index: 14, Text: "SuperAI uses CortexDB.", Source: "architecture.pdf",
			Strategy: "heading", Heading: "Storage", Start: 100, End: 122,
		}},
		Violations: []alchemy.Violation{{
			Kind: alchemy.ViolationUnknownRelationType, Detail: "DEPLOYS is not in sds@3",
			Subject: "e1 -[DEPLOYS]-> e2", Provenance: llm,
		}},
		Duplicates: []alchemy.Duplicate{{
			Signal: alchemy.DuplicateNameAffix, Subject: "CortexDB ~ SuperAI",
			Detail: "one name is the other with a word added",
			Left:   alchemy.DuplicateSide{ID: "e2", Type: "System", Name: "CortexDB", Provenance: ddl},
			Right:  alchemy.DuplicateSide{ID: "e1", Type: "System", Name: "SuperAI", Provenance: llm},
		}},
		Guesses: []alchemy.Guess{{
			Field: "owner_id", ChosenAs: "Person", Alternatives: []string{"Team"},
			Reason: "column name", Provenance: ddl,
		}},
		Unread: []alchemy.Unread{{Source: "architecture.pdf", Locator: "page 9", Reason: "scanned, no OCR model"}},
		Supersessions: []alchemy.Supersession{{
			Retires: "e-from-last-month", By: alchemy.Ref{Kind: alchemy.RefEntity, ID: "e3", Type: "Person"},
			Reason: "the office changed hands in March",
			Provenance: alchemy.Provenance{
				Source: "correction.md", Chunk: -1, Producer: alchemy.ProducerHuman,
				By: "ana@example.com", At: "2026-03-01T00:00:00Z",
			},
		}},
		RuleSets: []alchemy.RuleSet{{Name: "rs-9f21", Rules: []alchemy.StandingRule{
			{Name: "authored/violation/type=Flag", Told: "a switch is not an entity, said ana@example.com"},
		}}},
		ModelCalls: []alchemy.ModelCall{{Model: "gemini-3.6-flash-high", Stage: "extract", Calls: 2, Tokens: 900}},
	}
	res.Counts = res.Derivable()
	return res
}

// ask runs one SPARQL query against the live store, for a test that wants to
// look at what was actually written rather than at what a read path returns. It
// lives in the test file rather than in the API because a connector's job is to
// load a graph, not to become a second SPARQL client.
func (l *Loader) ask(t *testing.T, q string) []map[string]binding {
	t.Helper()
	rows, err := l.query(context.Background(), q)
	if err != nil {
		t.Fatalf("query:\n%s\n%v", q, err)
	}
	return rows
}
