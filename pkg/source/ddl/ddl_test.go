package ddl

import (
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

func TestParseCreateTableBecomesEntity(t *testing.T) {
	res, err := Parse("schema.sql", "CREATE TABLE customers (id INT);")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Entities) != 1 {
		t.Fatalf("want 1 entity, got %d: %+v", len(res.Entities), res.Entities)
	}
	e := res.Entities[0]
	if e.Name != "customers" {
		t.Errorf("Name = %q, want customers", e.Name)
	}
	if e.Type != "Table" {
		t.Errorf("Type = %q, want Table", e.Type)
	}
	want := alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	if e.Provenance != want {
		t.Errorf("Provenance = %+v, want %+v", e.Provenance, want)
	}
}

func TestParseColumnsBecomeAttributes(t *testing.T) {
	res, err := Parse("schema.sql", `
CREATE TABLE customers (
  id INT PRIMARY KEY,
  name VARCHAR(64) NOT NULL,
  city TEXT
);`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Entities) != 1 {
		t.Fatalf("want 1 entity, got %d", len(res.Entities))
	}
	got := res.Entities[0].Attributes["columns"]
	want := map[string]any{
		"id":   map[string]any{"type": "INT", "nullable": false, "primary_key": true, "foreign_key": false},
		"name": map[string]any{"type": "VARCHAR(64)", "nullable": false, "primary_key": false, "foreign_key": false},
		"city": map[string]any{"type": "TEXT", "nullable": true, "primary_key": false, "foreign_key": false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("columns =\n%#v\nwant\n%#v", got, want)
	}
	if pk, _ := res.Entities[0].Attributes["primary_key"].([]any); !reflect.DeepEqual(pk, []any{"id"}) {
		t.Errorf("primary_key = %#v, want [id]", res.Entities[0].Attributes["primary_key"])
	}
}

// The point of the package, asserted once over everything it can emit: a DDL
// import is deterministic, so no output may carry a model, a confidence, an
// ontology or a chunk. Those fields describe a guess (DESIGN.md §5b), and a
// reviewer filtering "the half that was guessed" must find this half empty.
func TestNothingIsEverMarkedAsInferred(t *testing.T) {
	res, err := Parse("everything.sql", `
CREATE TABLE users (id INT PRIMARY KEY);
CREATE TABLE roles (id INT PRIMARY KEY);
CREATE TABLE user_roles (user_id INT REFERENCES users(id), role_id INT REFERENCES roles(id), PRIMARY KEY (user_id, role_id));
CREATE TABLE audit (id INT, ghost_id INT REFERENCES ghosts(id));
CREATE TABLE audit (id INT, ghost_id INT REFERENCES ghosts(id), extra TEXT);`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := alchemy.Provenance{Source: "everything.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	check := func(what string, got alchemy.Provenance) {
		if got != want {
			t.Errorf("%s provenance = %+v, want %+v", what, got, want)
		}
	}
	if len(res.Entities) == 0 || len(res.Relations) == 0 || len(res.Violations) == 0 || len(res.Conflicts) == 0 {
		t.Fatalf("this fixture must exercise all four outputs: %+v", res)
	}
	for _, e := range res.Entities {
		check("entity "+e.ID, e.Provenance)
	}
	for _, r := range res.Relations {
		check("relation "+r.From+"->"+r.To, r.Provenance)
	}
	for _, v := range res.Violations {
		check("violation "+v.Subject, v.Provenance)
	}
	for _, c := range res.Conflicts {
		check("conflict left", c.Left.Provenance)
		check("conflict right", c.Right.Provenance)
	}
}
