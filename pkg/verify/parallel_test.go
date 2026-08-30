package verify_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/source/ddl"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// checkDDL is the whole of a schema import: parse, then verify what was parsed.
// The tests below are written against the SQL rather than against hand-built
// relations because the defect they pin was invisible in hand-built relations —
// it only appears once a producer that knows its edges apart is talking to a
// verifier that does not.
func checkDDL(t *testing.T, sql string) verify.Report {
	t.Helper()
	parsed, err := ddl.Parse("schema.sql", sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Conflicts) != 0 {
		t.Fatalf("the reader found %d conflicts of its own: %+v", len(parsed.Conflicts), parsed.Conflicts)
	}
	return verify.Check(verify.Input{
		Entities:   parsed.Entities,
		Relations:  parsed.Relations,
		Vocabulary: schemaVocab(),
		OntologyID: "schema@1",
	})
}

// A table that models a relationship between two rows of one table references
// that table twice, and both references are correct. This is the reduced shape
// of NODE_CONNECTIONS and of four more tables in the same schema.
func TestTwoForeignKeysOntoOneTableAreTwoEdges(t *testing.T) {
	got := checkDDL(t, `
CREATE TABLE nodes (name VARCHAR(24) NOT NULL, PRIMARY KEY (name));
CREATE TABLE node_connections (
    node_name_src VARCHAR(24) NOT NULL,
    node_name_dst VARCHAR(24) NOT NULL,
    CONSTRAINT fk_nc_src FOREIGN KEY (node_name_src) REFERENCES nodes(name),
    CONSTRAINT fk_nc_dst FOREIGN KEY (node_name_dst) REFERENCES nodes(name)
);`)

	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %d, want 0: %+v", len(got.Conflicts), got.Conflicts)
	}
	if len(got.Relations) != 2 {
		t.Fatalf("relations = %d, want both ends of the connection", len(got.Relations))
	}
}

// Two tables that reference each other is the other ordinary shape the old
// identity got wrong: A→B and B→A land in one undirected bucket, and the
// direction check reads them as one source contradicting another about which
// way one edge runs. They are two edges, and each runs the only way it can.
func TestTwoTablesReferencingEachOtherIsNotADirectionConflict(t *testing.T) {
	got := checkDDL(t, `
CREATE TABLE nodes (name VARCHAR(24) NOT NULL, default_pool VARCHAR(24),
    PRIMARY KEY (name),
    CONSTRAINT fk_node_pool FOREIGN KEY (default_pool) REFERENCES pools(name));
CREATE TABLE pools (name VARCHAR(24) NOT NULL, owner_node VARCHAR(24),
    PRIMARY KEY (name),
    CONSTRAINT fk_pool_node FOREIGN KEY (owner_node) REFERENCES nodes(name));`)

	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts = %d, want 0: %+v", len(got.Conflicts), got.Conflicts)
	}
	if len(got.Relations) != 2 {
		t.Fatalf("relations = %d, want one edge each way", len(got.Relations))
	}
}

// The other half of the claim: telling parallel edges apart must not stop two
// sources from disagreeing about one edge. These three cases are the ones the
// key could plausibly have swallowed, and each is a conflict this package was
// built to raise.
func TestAKeyStillLetsTwoSourcesDisagreeAboutOneEdge(t *testing.T) {
	schemaA := alchemy.Provenance{Source: "a.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	schemaB := alchemy.Provenance{Source: "b.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	model := alchemy.Provenance{Source: "notes.pdf", Chunk: 4, Producer: alchemy.ProducerLLMExtract}

	for _, tc := range []struct {
		name  string
		left  alchemy.Relation
		right alchemy.Relation
		want  alchemy.ConflictKind
	}{
		{
			// Two dumps of one database that name the same constraint and
			// disagree about what it constrains. The key says they are the same
			// edge, which is exactly why this is a question.
			name:  "two schemas, one constraint name",
			left:  alchemy.Relation{From: "c1", To: "n1", Type: "CONTAINS", Key: "fk_c_n", Attributes: map[string]any{"columns": "node_id"}, Provenance: schemaA},
			right: alchemy.Relation{From: "c1", To: "n1", Type: "CONTAINS", Key: "fk_c_n", Attributes: map[string]any{"columns": "member_id"}, Provenance: schemaB},
			want:  alchemy.ConflictRelationAttributes,
		},
		{
			// A model has no key to give. There is one edge in this bucket, so
			// nothing is ambiguous and the schema still contradicts it — the
			// finding §5c ranks first.
			name:  "a keyed schema against an unkeyed model",
			left:  alchemy.Relation{From: "c1", To: "n1", Type: "CONTAINS", Key: "fk_c_n", Attributes: map[string]any{"card": "1:n"}, Provenance: schemaA},
			right: alchemy.Relation{From: "c1", To: "n1", Type: "CONTAINS", Attributes: map[string]any{"card": "1:1"}, Provenance: model},
			want:  alchemy.ConflictContradiction,
		},
		{
			// Direction is settled the same way: one edge in the bucket, so a
			// model reversing the schema is still reported.
			name:  "a keyed schema reversed by an unkeyed model",
			left:  alchemy.Relation{From: "c1", To: "n1", Type: "CONTAINS", Key: "fk_c_n", Provenance: schemaA},
			right: alchemy.Relation{From: "n1", To: "c1", Type: "CONTAINS", Provenance: model},
			want:  alchemy.ConflictContradiction,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := verify.Check(verify.Input{
				Entities:   []alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}},
				Relations:  []alchemy.Relation{tc.left, tc.right},
				Vocabulary: vocab(),
			})
			if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != tc.want {
				t.Fatalf("conflicts = %+v, want one %q", got.Conflicts, tc.want)
			}
		})
	}
}

// Where a key is doing work, it is in the subject too. Two questions about two
// different foreign keys between one pair of tables that rendered the same
// string would be two questions a reviewer cannot tell apart, and one answer
// would land on both edges — the same defect one layer down.
func TestASubjectNamesWhichOfSeveralParallelEdgesItIsAbout(t *testing.T) {
	ddlA := alchemy.Provenance{Source: "a.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	ddlB := alchemy.Provenance{Source: "b.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}},
		Relations: []alchemy.Relation{
			{From: "c1", To: "n1", Type: "CONTAINS", Key: "fk_src", Attributes: map[string]any{"columns": "src"}, Provenance: ddlA},
			{From: "c1", To: "n1", Type: "CONTAINS", Key: "fk_dst", Attributes: map[string]any{"columns": "dst"}, Provenance: ddlA},
			// A second file disagrees about one of the two, and only that one.
			{From: "c1", To: "n1", Type: "CONTAINS", Key: "fk_dst", Attributes: map[string]any{"columns": "target"}, Provenance: ddlB},
		},
		Vocabulary: vocab(),
	})
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want exactly the one about fk_dst", got.Conflicts)
	}
	if !strings.Contains(got.Conflicts[0].Subject, "fk_dst") {
		t.Fatalf("subject = %q, want it to name the edge the question is about", got.Conflicts[0].Subject)
	}
}

// An unkeyed graph — every llm-extract job — is identified exactly as it was
// before Relation.Key existed, subject strings included. The field is optional
// and its absence must change nothing.
func TestAnUnkeyedGraphIsUnchanged(t *testing.T) {
	got := verify.Check(verify.Input{
		Entities: []alchemy.Entity{{ID: "c1", Type: "Cluster"}, {ID: "n1", Type: "Node"}},
		Relations: []alchemy.Relation{
			{From: "c1", To: "n1", Type: "CONTAINS", Attributes: map[string]any{"card": "1:n"}, Provenance: alchemy.Provenance{Source: "a.pdf", Chunk: 1, Producer: alchemy.ProducerLLMExtract}},
			{From: "c1", To: "n1", Type: "CONTAINS", Attributes: map[string]any{"card": "1:1"}, Provenance: alchemy.Provenance{Source: "b.pdf", Chunk: 2, Producer: alchemy.ProducerLLMExtract}},
		},
		Vocabulary: vocab(),
	})
	if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != alchemy.ConflictRelationAttributes {
		t.Fatalf("conflicts = %+v, want one relation_attributes", got.Conflicts)
	}
	if got.Conflicts[0].Subject != "c1 -[CONTAINS]-> n1.card" {
		t.Fatalf("subject = %q, want the spelling callers have always seen", got.Conflicts[0].Subject)
	}
}
