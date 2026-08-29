package ddl

import (
	"reflect"
	"testing"
)

// entityNames returns the entity names in declaration order, which is the order
// Parse must preserve: two runs over the same file must produce the same graph.
func entityNames(res Result) []string {
	var out []string
	for _, e := range res.Entities {
		out = append(out, e.Name)
	}
	return out
}

func mustParse(t *testing.T, ddl string) Result {
	t.Helper()
	res, err := Parse("schema.sql", ddl)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return res
}

func TestMySQLBacktickQuoting(t *testing.T) {
	res := mustParse(t, "CREATE TABLE `customers` (`id` INT NOT NULL, PRIMARY KEY (`id`));\n"+
		"CREATE TABLE `orders` (`id` INT, `customer_id` INT, "+
		"FOREIGN KEY (`customer_id`) REFERENCES `customers` (`id`)) ENGINE=InnoDB;")
	if got := entityNames(res); !reflect.DeepEqual(got, []string{"customers", "orders"}) {
		t.Fatalf("entities = %v", got)
	}
	if len(res.Relations) != 1 || res.Relations[0].To != "table:customers" {
		t.Fatalf("relations = %+v", res.Relations)
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %+v", res.Violations)
	}
}

func TestPostgresDoubleQuoteQuoting(t *testing.T) {
	// "order" is a reserved word, which is exactly why it gets quoted; a quoted
	// keyword is an identifier and must not be read as a keyword.
	res := mustParse(t, `
CREATE TABLE "customers" ("id" integer PRIMARY KEY);
CREATE TABLE "order" ("id" integer, "customer_id" integer REFERENCES "customers" ("id"));`)
	if got := entityNames(res); !reflect.DeepEqual(got, []string{"customers", "order"}) {
		t.Fatalf("entities = %v", got)
	}
	if len(res.Relations) != 1 || res.Relations[0].From != "table:order" {
		t.Fatalf("relations = %+v", res.Relations)
	}
}

func TestSQLServerBracketQuoting(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE [dbo].[Customers] ([Id] INT NOT NULL PRIMARY KEY);
CREATE TABLE [dbo].[Orders] (
  [Id] INT NOT NULL,
  [CustomerId] INT NOT NULL,
  CONSTRAINT [FK_Orders_Customers] FOREIGN KEY ([CustomerId]) REFERENCES [dbo].[Customers] ([Id])
);`)
	if got := entityNames(res); !reflect.DeepEqual(got, []string{"Customers", "Orders"}) {
		t.Fatalf("entities = %v", got)
	}
	if len(res.Relations) != 1 {
		t.Fatalf("relations = %+v", res.Relations)
	}
	r := res.Relations[0]
	if r.From != "table:dbo.orders" || r.To != "table:dbo.customers" {
		t.Errorf("edge = %s -> %s", r.From, r.To)
	}
	if r.Attributes["constraint"] != "FK_Orders_Customers" {
		t.Errorf("constraint = %v", r.Attributes["constraint"])
	}
}

func TestIfNotExists(t *testing.T) {
	res := mustParse(t, "CREATE TABLE IF NOT EXISTS customers (id INT);")
	if got := entityNames(res); !reflect.DeepEqual(got, []string{"customers"}) {
		t.Fatalf("entities = %v", got)
	}
}

func TestSchemaQualifiedNames(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE public.customers (id INT PRIMARY KEY);
CREATE TABLE public.orders (id INT, customer_id INT REFERENCES customers (id));`)
	if res.Entities[0].ID != "table:public.customers" {
		t.Errorf("ID = %q, want table:public.customers", res.Entities[0].ID)
	}
	if res.Entities[0].Attributes["schema"] != "public" {
		t.Errorf("schema attribute = %v", res.Entities[0].Attributes["schema"])
	}
	if res.Entities[0].Name != "customers" {
		t.Errorf("Name = %q, want the bare table name", res.Entities[0].Name)
	}
	// The reference is unqualified and the declaration is qualified: same table.
	if len(res.Relations) != 1 || res.Relations[0].To != "table:public.customers" {
		t.Fatalf("relations = %+v, violations = %+v", res.Relations, res.Violations)
	}
}

func TestComments(t *testing.T) {
	res := mustParse(t, `
-- customers: REFERENCES nothing, despite this comment saying REFERENCES
CREATE TABLE customers ( -- the customer table
  id INT PRIMARY KEY, /* the key */
  /* a block comment
     spanning lines, mentioning FOREIGN KEY (id) REFERENCES ghosts(id) */
  name TEXT
);`)
	if got := entityNames(res); !reflect.DeepEqual(got, []string{"customers"}) {
		t.Fatalf("entities = %v", got)
	}
	cols, _ := res.Entities[0].Attributes["columns"].(map[string]any)
	if len(cols) != 2 {
		t.Fatalf("columns = %#v", cols)
	}
	if len(res.Relations) != 0 || len(res.Violations) != 0 {
		t.Errorf("a comment produced a relation: %+v %+v", res.Relations, res.Violations)
	}
}

func TestSemicolonInsideStringLiteralDoesNotEndStatement(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE settings (
  id INT PRIMARY KEY,
  motto TEXT DEFAULT 'a;b;c',
  escaped TEXT DEFAULT 'it''s ;fine',
  path TEXT DEFAULT 'C:\\tmp;x'
);
CREATE TABLE after (id INT);`)
	if got := entityNames(res); !reflect.DeepEqual(got, []string{"settings", "after"}) {
		t.Fatalf("entities = %v", got)
	}
	cols, _ := res.Entities[0].Attributes["columns"].(map[string]any)
	if len(cols) != 4 {
		t.Fatalf("columns = %#v", cols)
	}
}
