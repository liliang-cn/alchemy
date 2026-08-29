package ddl

import (
	"reflect"
	"testing"
)

func attrsOf(t *testing.T, res Result, name string) map[string]any {
	t.Helper()
	for _, e := range res.Entities {
		if e.Name == name {
			return e.Attributes
		}
	}
	t.Fatalf("no entity named %q in %v", name, entityNames(res))
	return nil
}

const manyToMany = `
CREATE TABLE users (id INT PRIMARY KEY);
CREATE TABLE roles (id INT PRIMARY KEY);
CREATE TABLE user_roles (
  user_id INT NOT NULL REFERENCES users(id),
  role_id INT NOT NULL REFERENCES roles(id),
  PRIMARY KEY (user_id, role_id)
);`

// A junction table is marked, kept, and never collapsed. See junctionOf for the
// reasoning; the test pins both halves of it.
func TestJunctionTableIsMarkedAndKept(t *testing.T) {
	res := mustParse(t, manyToMany)
	attrs := attrsOf(t, res, "user_roles")
	if attrs["junction"] != true {
		t.Errorf("junction = %v, want true", attrs["junction"])
	}
	if got, _ := attrs["junction_of"].([]string); !reflect.DeepEqual(got, []string{"table:users", "table:roles"}) {
		t.Errorf("junction_of = %#v", attrs["junction_of"])
	}
	// Both declared edges survive, and no third edge is invented.
	if len(res.Relations) != 2 {
		t.Fatalf("relations = %+v", res.Relations)
	}
	for _, r := range res.Relations {
		if r.From != "table:user_roles" {
			t.Errorf("unexpected edge %s -[%s]-> %s: nothing in the DDL states it", r.From, r.Type, r.To)
		}
	}
}

// A surrogate primary key does not stop a table being a junction.
func TestJunctionTableWithSurrogateKey(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE users (id INT PRIMARY KEY);
CREATE TABLE roles (id INT PRIMARY KEY);
CREATE TABLE user_roles (
  id INT PRIMARY KEY,
  user_id INT REFERENCES users(id),
  role_id INT REFERENCES roles(id)
);`)
	if attrsOf(t, res, "user_roles")["junction"] != true {
		t.Errorf("surrogate-keyed link table not marked as a junction")
	}
}

// One extra plain column and it is not a junction table any more: it carries
// facts of its own, and a consumer that collapsed it would lose them.
func TestTableWithPayloadIsNotAJunction(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE users (id INT PRIMARY KEY);
CREATE TABLE roles (id INT PRIMARY KEY);
CREATE TABLE user_roles (
  user_id INT REFERENCES users(id),
  role_id INT REFERENCES roles(id),
  granted_at TIMESTAMP,
  PRIMARY KEY (user_id, role_id)
);`)
	attrs := attrsOf(t, res, "user_roles")
	if _, ok := attrs["junction"]; ok {
		t.Errorf("junction = %v, want the attribute to be absent", attrs["junction"])
	}
}

// An ordinary table with one foreign key is not a junction either.
func TestSingleForeignKeyTableIsNotAJunction(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE customers (id INT PRIMARY KEY);
CREATE TABLE orders (id INT PRIMARY KEY, customer_id INT REFERENCES customers(id));`)
	if _, ok := attrsOf(t, res, "orders")["junction"]; ok {
		t.Errorf("orders marked as a junction table")
	}
}

// junction_of is only stated when both sides are actually in the graph: an id
// pointing at an entity the result does not contain is worse than no id.
func TestJunctionOfOmittedWhenATargetIsMissing(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE users (id INT PRIMARY KEY);
CREATE TABLE user_roles (
  user_id INT REFERENCES users(id),
  role_id INT REFERENCES roles(id),
  PRIMARY KEY (user_id, role_id)
);`)
	attrs := attrsOf(t, res, "user_roles")
	if attrs["junction"] != true {
		t.Errorf("junction = %v, want true: the shape is still a junction", attrs["junction"])
	}
	if _, ok := attrs["junction_of"]; ok {
		t.Errorf("junction_of = %v, want the attribute to be absent", attrs["junction_of"])
	}
	if len(res.Violations) != 1 {
		t.Errorf("violations = %+v", res.Violations)
	}
}
