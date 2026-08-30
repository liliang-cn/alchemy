package ddl

import "testing"

// A foreign key is a named thing in SQL, and its name is what tells one of a
// table's foreign keys from another. Emitting it as an attribute alone was not
// enough: an attribute is something the edge *says*, and two edges that say
// different things about themselves read as one edge two sources disagree
// about. The name goes on Relation.Key, where it says which edge this is.
func TestTheConstraintNameIsTheEdgesKey(t *testing.T) {
	res, err := Parse("schema.sql", `
CREATE TABLE nodes (name VARCHAR(24) PRIMARY KEY);
CREATE TABLE node_connections (
  node_name_src VARCHAR(24) NOT NULL,
  node_name_dst VARCHAR(24) NOT NULL,
  CONSTRAINT fk_nc_src FOREIGN KEY (node_name_src) REFERENCES nodes(name),
  CONSTRAINT fk_nc_dst FOREIGN KEY (node_name_dst) REFERENCES nodes(name)
);`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Relations) != 2 {
		t.Fatalf("relations = %d, want 2: %+v", len(res.Relations), res.Relations)
	}
	got := map[string]bool{res.Relations[0].Key: true, res.Relations[1].Key: true}
	if !got["fk_nc_src"] || !got["fk_nc_dst"] {
		t.Fatalf("keys = %v, want the two constraint names", got)
	}
}

// A foreign key the schema did not name is still a distinct foreign key, and a
// schema that names none of its constraints is common. What identifies it is
// the only thing the statement gave: the columns it constrains. That is not an
// inference about the world — a second FOREIGN KEY clause over the same columns
// onto the same table is the same constraint written twice — so it is derived
// here rather than reported as a Guess.
func TestAnUnnamedForeignKeyIsKeyedByItsColumns(t *testing.T) {
	res, err := Parse("schema.sql", `
CREATE TABLE nodes (name VARCHAR(24) PRIMARY KEY);
CREATE TABLE node_connections (
  node_name_src VARCHAR(24) NOT NULL,
  node_name_dst VARCHAR(24) NOT NULL,
  FOREIGN KEY (node_name_src) REFERENCES nodes(name),
  FOREIGN KEY (node_name_dst) REFERENCES nodes(name)
);`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Relations) != 2 {
		t.Fatalf("relations = %d, want 2: %+v", len(res.Relations), res.Relations)
	}
	a, b := res.Relations[0].Key, res.Relations[1].Key
	if a == "" || b == "" || a == b {
		t.Fatalf("keys = %q and %q, want two distinct non-empty keys", a, b)
	}
}
