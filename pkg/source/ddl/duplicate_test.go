package ddl

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// A schema assembled from migrations can declare the same table twice. Two
// entities sharing one ID would break every consumer that walks the graph by
// id, so the declarations are collapsed — but only when they agree.
func TestIdenticalDuplicateDeclarationsCollapse(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE IF NOT EXISTS customers (id INT PRIMARY KEY, name TEXT);
CREATE TABLE IF NOT EXISTS customers (id INT PRIMARY KEY, name TEXT);`)
	if len(res.Entities) != 1 {
		t.Fatalf("entities = %v", entityNames(res))
	}
	if len(res.Conflicts) != 0 {
		t.Errorf("conflicts = %+v, want none: the two declarations agree", res.Conflicts)
	}
}

// When they disagree, nothing in the file decides which one is right. DESIGN.md
// §7.3: that is a question for a person, not a quality score to be averaged.
func TestDisagreeingDuplicateDeclarationsConflict(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE customers (id INT PRIMARY KEY, name TEXT);
CREATE TABLE customers (id INT PRIMARY KEY, name TEXT, deleted_at TIMESTAMP);`)
	if len(res.Entities) != 1 {
		t.Fatalf("entities = %v", entityNames(res))
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v", res.Conflicts)
	}
	c := res.Conflicts[0]
	if c.Kind != alchemy.ConflictEntityAttributes {
		t.Errorf("Kind = %q", c.Kind)
	}
	if c.Subject != "table:customers" {
		t.Errorf("Subject = %q", c.Subject)
	}
	if !strings.Contains(c.Left.Statement, "line 2") || !strings.Contains(c.Right.Statement, "line 3") {
		t.Errorf("statements = %q / %q, want each to name its line", c.Left.Statement, c.Right.Statement)
	}
	if c.Left.Provenance.Producer != alchemy.ProducerDDL || c.Right.Provenance.Chunk != -1 {
		t.Errorf("provenance = %+v / %+v", c.Left.Provenance, c.Right.Provenance)
	}
}

// A duplicate must not turn every reference to the table into an ambiguity.
func TestReferencesResolveAcrossADuplicateDeclaration(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE customers (id INT PRIMARY KEY);
CREATE TABLE customers (id INT PRIMARY KEY);
CREATE TABLE orders (id INT, customer_id INT REFERENCES customers(id));`)
	if len(res.Relations) != 1 || len(res.Violations) != 0 {
		t.Fatalf("relations = %+v, violations = %+v", res.Relations, res.Violations)
	}
}

// Two schemas can each hold a table of the same name. A bare reference to it
// names both, and picking one by declaration order is the silent guess §2.1
// warns about, so it is reported instead.
func TestAmbiguousBareReferenceIsReported(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE sales.customers (id INT PRIMARY KEY);
CREATE TABLE support.customers (id INT PRIMARY KEY);
CREATE TABLE orders (id INT, customer_id INT REFERENCES customers(id));`)
	if len(res.Relations) != 0 {
		t.Errorf("relations = %+v, want none: which customers is unknown", res.Relations)
	}
	if len(res.Violations) != 1 {
		t.Fatalf("violations = %+v", res.Violations)
	}
	if !strings.Contains(res.Violations[0].Detail, "ambiguous") {
		t.Errorf("Detail = %q", res.Violations[0].Detail)
	}
}
