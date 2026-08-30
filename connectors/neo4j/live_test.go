package neo4j

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// envURI names the one environment variable the live tests need. They skip
// without it so that `go test ./...` passes on a machine with no database —
// but a skipped test proves nothing, so the message says exactly what to set.
const envURI = "ALCHEMY_TEST_NEO4J"

func liveURI(t *testing.T) string {
	t.Helper()
	uri := os.Getenv(envURI)
	if uri == "" {
		t.Skipf("no live Neo4j: set %s to a bolt URI (e.g. %s=bolt://127.0.0.1:47687) to run this test", envURI, envURI)
	}
	return uri
}

func liveAuth() (string, string) {
	user, pass := os.Getenv(envURI+"_USER"), os.Getenv(envURI+"_PASSWORD")
	if user == "" {
		user = "neo4j"
	}
	if pass == "" {
		pass = "alchemy-test"
	}
	return user, pass
}

// testLabel gives every test its own base label, which is the whole cleanup
// strategy: Community edition has one database, so a label is the only handle
// a test has on the nodes it made. Random rather than derived from the test
// name so that a suite run with -count=2 does not have two tests sharing a
// namespace.
func testLabel(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "AlchemyTest_" + hex.EncodeToString(b[:])
}

// liveLoader opens a Loader against the live server, gives it a private label,
// and tears down everything it wrote — including the indexes, which outlive
// the nodes and would otherwise accumulate one per test run forever.
func liveLoader(t *testing.T, o Options) *Loader {
	t.Helper()
	uri := liveURI(t)
	user, pass := liveAuth()
	if o.BaseLabel == "" {
		o.BaseLabel = testLabel(t)
	}
	l, err := Open(context.Background(), uri, user, pass, o)
	if err != nil {
		t.Fatalf("Open(%s): %v", uri, err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		label, _ := quoteIdent(l.opts.BaseLabel)
		// DETACH DELETE in bounded bites: a test that loaded ten thousand
		// nodes should not need a transaction big enough to hold them.
		for {
			rec, err := l.query(ctx, fmt.Sprintf(
				"MATCH (n:%s) WITH n LIMIT 5000 DETACH DELETE n RETURN count(n) AS n", label), nil)
			if err != nil {
				t.Errorf("cleanup: %v", err)
				return
			}
			if len(rec) == 0 || rec[0]["n"].(int64) == 0 {
				break
			}
		}
		for _, name := range l.indexNames() {
			if _, err := l.query(ctx, "DROP INDEX "+name+" IF EXISTS", nil); err != nil {
				t.Errorf("cleanup index %s: %v", name, err)
			}
		}
		if err := l.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return l
}

// query runs one read-or-write statement and returns the records as maps. It
// lives in the test file rather than the API because a connector's job is to
// load a graph, not to become a second Cypher client.
func (l *Loader) query(ctx context.Context, cypher string, params map[string]any) ([]map[string]any, error) {
	s := l.driver.NewSession(ctx, driver.SessionConfig{DatabaseName: l.opts.Database})
	defer s.Close(ctx)
	res, err := s.Run(ctx, cypher, params)
	if err != nil {
		return nil, err
	}
	recs, err := res.Collect(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.AsMap())
	}
	return out, nil
}

func (l *Loader) mustQuery(t *testing.T, cypher string, params map[string]any) []map[string]any {
	t.Helper()
	recs, err := l.query(context.Background(), cypher, params)
	if err != nil {
		t.Fatalf("query %s: %v", cypher, err)
	}
	return recs
}

// fixture is a small result that exercises every shape the loader has an
// opinion about: two producers, an attribute map, a relation, a chunk, and one
// of each kind of finding.
func fixture() alchemy.Result {
	llm := alchemy.Provenance{
		Source: "architecture.pdf", Chunk: 14, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Ontology: "sds@3", Chunking: "heading", Confidence: 0.82,
	}
	ddl := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL, Ontology: "sds@3"}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "System", Name: "SuperAI", Attributes: map[string]any{"public": true}, Provenance: llm},
			{ID: "e2", Type: "System", Name: "CortexDB", Provenance: ddl},
			{ID: "e3", Type: "Person", Name: "Ada", Attributes: map[string]any{"address": map[string]any{"city": "Wien"}}, Provenance: llm},
		},
		Relations: []alchemy.Relation{
			{From: "e1", To: "e2", Type: "USES", Provenance: llm},
			{From: "e3", To: "e1", Type: "WORKS_ON", Attributes: map[string]any{"since": 2024.0}, Provenance: ddl},
		},
		Chunks: []alchemy.Chunk{{Index: 14, Text: "SuperAI uses CortexDB.", Source: "architecture.pdf", Strategy: "heading", Heading: "Storage", Start: 100, End: 122}},
		Violations: []alchemy.Violation{{
			Kind: alchemy.ViolationUnknownRelationType, Detail: "DEPLOYS is not in sds@3",
			Subject: "e1 -[DEPLOYS]-> e2", Provenance: llm,
		}},
		Duplicates: []alchemy.Duplicate{{
			Signal: alchemy.DuplicateNameAffix, Subject: "CortexDB ~ CortexDB store",
			Detail: "one name is the other with a word added",
			Left:   alchemy.DuplicateSide{ID: "e2", Type: "System", Name: "CortexDB", Provenance: ddl},
			Right:  alchemy.DuplicateSide{ID: "e1", Type: "System", Name: "SuperAI", Provenance: llm},
		}},
		Guesses: []alchemy.Guess{{Field: "owner_id", ChosenAs: "Person", Alternatives: []string{"Team"}, Reason: "column name", Provenance: ddl}},
		Unread:  []alchemy.Unread{{Source: "architecture.pdf", Locator: "page 9", Reason: "scanned, no OCR model supplied"}},
		Counts: alchemy.Counts{
			Entities: 3, Relations: 2, Deterministic: 2, Inferred: 3,
			Violations: 1, Duplicates: 1, Guesses: 1, ChunksEmpty: 2, ChunksUnread: 1,
		},
	}
}
