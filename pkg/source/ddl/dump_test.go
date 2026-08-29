package ddl

import (
	"reflect"
	"testing"
)

// A mysqldump, with the noise a real one carries: conditional comments, index
// lines, engine clauses and an INSERT whose values contain quotes and
// semicolons.
func TestMySQLDump(t *testing.T) {
	res := mustParse(t, "-- MySQL dump 10.13  Distrib 8.0.36\n"+
		"/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;\n"+
		"DROP TABLE IF EXISTS `customers`;\n"+
		"CREATE TABLE `customers` (\n"+
		"  `id` int NOT NULL AUTO_INCREMENT,\n"+
		"  `email` varchar(255) CHARACTER SET utf8mb4 NOT NULL,\n"+
		"  `note` text COMMENT 'free; text',\n"+
		"  PRIMARY KEY (`id`),\n"+
		"  UNIQUE KEY `email_unique` (`email`)\n"+
		") ENGINE=InnoDB AUTO_INCREMENT=42 DEFAULT CHARSET=utf8mb4;\n"+
		"INSERT INTO `customers` VALUES (1,'a;b','it\\'s fine'),(2,'c','d');\n"+
		"CREATE TABLE `orders` (\n"+
		"  `id` int NOT NULL,\n"+
		"  `customer_id` int NOT NULL,\n"+
		"  `total` decimal(10,2) DEFAULT '0.00',\n"+
		"  PRIMARY KEY (`id`),\n"+
		"  KEY `fk_customer` (`customer_id`),\n"+
		"  CONSTRAINT `fk_customer` FOREIGN KEY (`customer_id`) REFERENCES `customers` (`id`) ON DELETE CASCADE\n"+
		") ENGINE=InnoDB;\n")
	if got := entityNames(res); !reflect.DeepEqual(got, []string{"customers", "orders"}) {
		t.Fatalf("entities = %v", got)
	}
	cols, _ := res.Entities[1].Attributes["columns"].(map[string]any)
	total, _ := cols["total"].(map[string]any)
	if total["type"] != "decimal(10,2)" {
		t.Errorf("total type = %v, want decimal(10,2) verbatim", total["type"])
	}
	if total["nullable"] != true {
		t.Errorf("total nullable = %v, want true", total["nullable"])
	}
	id, _ := cols["id"].(map[string]any)
	if id["primary_key"] != true || id["nullable"] != false {
		t.Errorf("id = %v", id)
	}
	if len(res.Relations) != 1 || res.Relations[0].Attributes["constraint"] != "fk_customer" {
		t.Fatalf("relations = %+v", res.Relations)
	}
	if len(res.Violations) != 0 || len(res.Conflicts) != 0 {
		t.Errorf("violations = %+v conflicts = %+v", res.Violations, res.Conflicts)
	}
}

// A pg_dump: quoted lowercase identifiers, schema qualification everywhere, and
// every foreign key declared after every table.
func TestPostgresDump(t *testing.T) {
	res := mustParse(t, `
SET statement_timeout = 0;
SET default_tablespace = '';

CREATE TABLE public.customers (
    id integer NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now()
);

CREATE SEQUENCE public.customers_id_seq AS integer START WITH 1 INCREMENT BY 1;
ALTER SEQUENCE public.customers_id_seq OWNED BY public.customers.id;

CREATE TABLE public.orders (
    id integer NOT NULL,
    customer_id integer NOT NULL,
    amount numeric(10,2)
);

ALTER TABLE ONLY public.customers ADD CONSTRAINT customers_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.orders ADD CONSTRAINT orders_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON UPDATE RESTRICT ON DELETE CASCADE;
ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_promo_fkey FOREIGN KEY (id) REFERENCES public.promotions(id);
`)
	if got := entityNames(res); !reflect.DeepEqual(got, []string{"customers", "orders"}) {
		t.Fatalf("entities = %v", got)
	}
	cols, _ := res.Entities[0].Attributes["columns"].(map[string]any)
	created, _ := cols["created_at"].(map[string]any)
	if created["type"] != "timestamp with time zone" {
		t.Errorf("created_at type = %v", created["type"])
	}
	if len(res.Relations) != 1 || res.Relations[0].To != "table:public.customers" {
		t.Fatalf("relations = %+v", res.Relations)
	}
	// promotions is not in this dump: the edge is reported, not lost.
	if len(res.Violations) != 1 {
		t.Fatalf("violations = %+v", res.Violations)
	}
}
