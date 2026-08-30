package ddl

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/preflight"
)

// alchemy.Entity.Attributes declares the JSON value domain, and a reader that
// writes a Go []string into it produces a graph that is one thing in this
// process and another thing everywhere else: encoding/json turns it into an
// []any of strings, so a consumer holding the Result directly and one holding
// the same result over §6's wire disagree about what the schema said.
//
// It is the same value either way, which is exactly why nobody noticed. What
// differs is the type, and a store branches on the type: one of the four
// connectors passes a []any of strings through as a native list and renders a
// []string as JSON text with a breadcrumb saying it had to. Same schema, same
// store, two shapes, decided by which side of a wire the caller was on.
func TestASchemasAttributesAreInTheDeclaredJSONDomain(t *testing.T) {
	res, err := Parse("schema.sql", `
CREATE TABLE users (id INT PRIMARY KEY, name TEXT);
CREATE TABLE roles (id INT PRIMARY KEY);
CREATE TABLE user_roles (
  user_id INT REFERENCES users(id),
  role_id INT REFERENCES roles(id),
  PRIMARY KEY (user_id, role_id)
);
CREATE TABLE orders (
  tenant_id INT, author_id INT,
  FOREIGN KEY (tenant_id, author_id) REFERENCES users (tenant_id, id)
);`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Entities) == 0 || len(res.Relations) == 0 {
		t.Fatalf("entities = %d, relations = %d; the fixture must produce both", len(res.Entities), len(res.Relations))
	}
	whole := alchemy.Result{Entities: res.Entities, Relations: res.Relations}
	whole.Counts = whole.Derivable()
	for _, d := range preflight.Check(whole) {
		if d.Kind == preflight.AttributeType {
			t.Errorf("%s", d.Detail)
		}
	}
}
