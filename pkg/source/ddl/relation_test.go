package ddl

import (
	"reflect"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func TestForeignKeyBecomesRelation(t *testing.T) {
	res, err := Parse("schema.sql", `
CREATE TABLE customers (id INT PRIMARY KEY);
CREATE TABLE orders (
  id INT PRIMARY KEY,
  customer_id INT REFERENCES customers(id)
);`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Relations) != 1 {
		t.Fatalf("want 1 relation, got %d: %+v", len(res.Relations), res.Relations)
	}
	r := res.Relations[0]
	if r.From != "table:orders" || r.To != "table:customers" {
		t.Errorf("edge = %s -> %s, want table:orders -> table:customers", r.From, r.To)
	}
	if r.Type != RelationType {
		t.Errorf("Type = %q, want %q", r.Type, RelationType)
	}
	if cols, _ := r.Attributes["columns"].([]string); !reflect.DeepEqual(cols, []string{"customer_id"}) {
		t.Errorf("columns = %#v, want [customer_id]", r.Attributes["columns"])
	}
	if refs, _ := r.Attributes["references"].([]string); !reflect.DeepEqual(refs, []string{"id"}) {
		t.Errorf("references = %#v, want [id]", r.Attributes["references"])
	}
	want := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	if r.Provenance != want {
		t.Errorf("Provenance = %+v, want %+v", r.Provenance, want)
	}
	if len(res.Violations) != 0 {
		t.Errorf("unexpected violations: %+v", res.Violations)
	}
}

// A dump is routinely partial: the table a foreign key points at may simply not
// be in the file. The edge must not vanish silently.
func TestDanglingForeignKeyIsReported(t *testing.T) {
	res, err := Parse("partial.sql", `
CREATE TABLE orders (
  id INT PRIMARY KEY,
  customer_id INT REFERENCES customers(id)
);`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Relations) != 0 {
		t.Errorf("dangling relation kept in graph: %+v", res.Relations)
	}
	if len(res.Violations) != 1 {
		t.Fatalf("want 1 violation, got %d: %+v", len(res.Violations), res.Violations)
	}
	v := res.Violations[0]
	if v.Kind != alchemy.ViolationDanglingRelation {
		t.Errorf("Kind = %q, want %q", v.Kind, alchemy.ViolationDanglingRelation)
	}
	if v.Subject != "table:orders -[REFERENCES]-> customers" {
		t.Errorf("Subject = %q", v.Subject)
	}
	if !strings.Contains(v.Detail, "customer_id") || !strings.Contains(v.Detail, "customers") {
		t.Errorf("Detail = %q, want it to name the column and the missing table", v.Detail)
	}
	if v.Provenance != (alchemy.Provenance{Source: "partial.sql", Chunk: -1, Producer: alchemy.ProducerDDL}) {
		t.Errorf("Provenance = %+v", v.Provenance)
	}
}

// A self-reference is a real edge. Dropping it because From equals To — which
// is the shortcut the earlier implementations took — loses the hierarchy that
// makes an org chart or a category tree navigable.
func TestSelfReferencingForeignKey(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE employees (
  id INT PRIMARY KEY,
  manager_id INT REFERENCES employees(id)
);`)
	if len(res.Relations) != 1 {
		t.Fatalf("relations = %+v", res.Relations)
	}
	if r := res.Relations[0]; r.From != "table:employees" || r.To != "table:employees" {
		t.Errorf("edge = %s -> %s", r.From, r.To)
	}
}

// Two foreign keys to one table are two relations, told apart by their columns.
// Collapsing them would lose which address is which.
func TestTwoForeignKeysToTheSameTable(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE addresses (id INT PRIMARY KEY);
CREATE TABLE orders (
  id INT PRIMARY KEY,
  billing_address_id INT REFERENCES addresses(id),
  shipping_address_id INT REFERENCES addresses(id)
);`)
	if len(res.Relations) != 2 {
		t.Fatalf("relations = %+v", res.Relations)
	}
	var cols []string
	for _, r := range res.Relations {
		c, _ := r.Attributes["columns"].([]string)
		cols = append(cols, c...)
	}
	if !reflect.DeepEqual(cols, []string{"billing_address_id", "shipping_address_id"}) {
		t.Errorf("columns = %v", cols)
	}
}

// REFERENCES without a column list means the referenced primary key. Inventing
// one would be a guess, so the attribute is simply absent.
func TestInlineReferencesWithoutColumnList(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE customers (id INT PRIMARY KEY);
CREATE TABLE orders (id INT, customer_id INT REFERENCES customers);`)
	if len(res.Relations) != 1 {
		t.Fatalf("relations = %+v, violations = %+v", res.Relations, res.Violations)
	}
	if _, ok := res.Relations[0].Attributes["references"]; ok {
		t.Errorf("references = %v, want the attribute to be absent", res.Relations[0].Attributes["references"])
	}
}

// Determinism is the product claim: the same file must produce the same graph,
// in the same order, every time. Go randomises map iteration, so a build that
// walked a map anywhere would fail this within a few runs.
func TestParseIsDeterministic(t *testing.T) {
	const ddl = `
CREATE TABLE users (id INT PRIMARY KEY, email TEXT);
CREATE TABLE roles (id INT PRIMARY KEY, name TEXT);
CREATE TABLE user_roles (user_id INT REFERENCES users(id), role_id INT REFERENCES roles(id), PRIMARY KEY (user_id, role_id));
CREATE TABLE audit (id INT, actor_id INT REFERENCES ghosts(id));`
	first := mustParse(t, ddl)
	for i := 0; i < 20; i++ {
		if got := mustParse(t, ddl); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, got, first)
		}
	}
}
