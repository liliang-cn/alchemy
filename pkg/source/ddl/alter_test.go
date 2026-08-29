package ddl

import (
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// pg_dump declares every foreign key after every table, so a reader that only
// looks inside CREATE TABLE finds a schema with no relations at all.
func TestAlterTableAddForeignKey(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE public.customers (id integer NOT NULL);
CREATE TABLE public.orders (id integer NOT NULL, customer_id integer NOT NULL);
ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE CASCADE;`)
	if len(res.Relations) != 1 {
		t.Fatalf("relations = %+v, violations = %+v", res.Relations, res.Violations)
	}
	r := res.Relations[0]
	if r.From != "table:public.orders" || r.To != "table:public.customers" {
		t.Errorf("edge = %s -> %s", r.From, r.To)
	}
	if r.Attributes["constraint"] != "orders_customer_id_fkey" {
		t.Errorf("constraint = %v", r.Attributes["constraint"])
	}
	// The column is a foreign key even though the CREATE TABLE did not say so.
	cols, _ := res.Entities[1].Attributes["columns"].(map[string]any)
	col, _ := cols["customer_id"].(map[string]any)
	if col["foreign_key"] != true {
		t.Errorf("customer_id foreign_key = %v, want true", col["foreign_key"])
	}
}

func TestAlterTableForeignKeyOnUnknownTableIsReported(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE orders (id INT, customer_id INT);
ALTER TABLE shipments ADD CONSTRAINT c FOREIGN KEY (order_id) REFERENCES orders(id);`)
	if len(res.Relations) != 0 {
		t.Errorf("relations = %+v", res.Relations)
	}
	if len(res.Violations) != 1 || res.Violations[0].Kind != alchemy.ViolationDanglingRelation {
		t.Fatalf("violations = %+v", res.Violations)
	}
}

func TestCompositeForeignKeyIsOneRelation(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE tenants_users (
  tenant_id INT,
  user_id INT,
  PRIMARY KEY (tenant_id, user_id)
);
CREATE TABLE posts (
  id INT PRIMARY KEY,
  tenant_id INT,
  author_id INT,
  CONSTRAINT fk_author FOREIGN KEY (tenant_id, author_id) REFERENCES tenants_users (tenant_id, user_id)
);`)
	if len(res.Relations) != 1 {
		t.Fatalf("want one relation for one composite key, got %+v", res.Relations)
	}
	r := res.Relations[0]
	if cols, _ := r.Attributes["columns"].([]string); !reflect.DeepEqual(cols, []string{"tenant_id", "author_id"}) {
		t.Errorf("columns = %#v", r.Attributes["columns"])
	}
	if refs, _ := r.Attributes["references"].([]string); !reflect.DeepEqual(refs, []string{"tenant_id", "user_id"}) {
		t.Errorf("references = %#v", r.Attributes["references"])
	}
}

// A real dump is mostly statements this package has no opinion about.
func TestNonTableStatementsAreSkipped(t *testing.T) {
	res := mustParse(t, `
SET statement_timeout = 0;
SET search_path = public, pg_catalog;
CREATE SEQUENCE customers_id_seq START WITH 1;
CREATE TABLE customers (id INT PRIMARY KEY);
CREATE INDEX idx_customers_id ON customers USING btree (id);
CREATE UNIQUE INDEX ON customers (id);
INSERT INTO customers VALUES (1), (2);
CREATE VIEW big_customers AS SELECT * FROM customers;
ALTER TABLE customers OWNER TO postgres;
ALTER SEQUENCE customers_id_seq OWNED BY customers.id;
COMMENT ON TABLE customers IS 'people';
GRANT ALL ON TABLE customers TO admin;`)
	if got := entityNames(res); !reflect.DeepEqual(got, []string{"customers"}) {
		t.Fatalf("entities = %v", got)
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %+v", res.Violations)
	}
}

// pg_dump declares primary keys out of line as well. "Whether it is a key" is
// stated in the file, so it must not be lost just because it was stated late.
func TestAlterTableAddPrimaryKey(t *testing.T) {
	res := mustParse(t, `
CREATE TABLE public.customers (id integer NOT NULL, name text);
ALTER TABLE ONLY public.customers ADD CONSTRAINT customers_pkey PRIMARY KEY (id);`)
	attrs := res.Entities[0].Attributes
	if pk, _ := attrs["primary_key"].([]string); !reflect.DeepEqual(pk, []string{"id"}) {
		t.Errorf("primary_key = %#v, want [id]", attrs["primary_key"])
	}
	cols, _ := attrs["columns"].(map[string]any)
	id, _ := cols["id"].(map[string]any)
	if id["primary_key"] != true {
		t.Errorf("id primary_key = %v, want true", id["primary_key"])
	}
}
