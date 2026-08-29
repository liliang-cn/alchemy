package ddl

import (
	"strings"
	"testing"
)

// A truncated CREATE TABLE is an error, not a half-read table: an entity
// carrying whichever columns survived the truncation is a loss nobody can see
// in the output, which is the failure mode this package exists to prevent.
func TestTruncatedCreateTableIsAnError(t *testing.T) {
	_, err := Parse("dump.sql", "CREATE TABLE customers (id INT);\nCREATE TABLE orders (\n  id INT,\n  customer_id INT")
	if err == nil {
		t.Fatal("want an error for a truncated CREATE TABLE")
	}
	if !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "orders") {
		t.Errorf("error = %q, want it to name line 2 and the table", err)
	}
}

func TestCreateTableWithoutColumnListIsAnError(t *testing.T) {
	_, err := Parse("dump.sql", "\n\nCREATE TABLE orders;")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "orders") {
		t.Errorf("error = %q, want it to name line 3 and the table", err)
	}
}

// After an unterminated quote every following statement boundary is fiction, so
// skipping ahead would silently swallow an unknown number of tables.
func TestUnterminatedStringLiteralIsAnError(t *testing.T) {
	_, err := Parse("dump.sql", "CREATE TABLE a (id INT);\nCREATE TABLE b (motto TEXT DEFAULT 'oops);\nCREATE TABLE c (id INT);")
	if err == nil {
		t.Fatal("want an error for an unterminated string literal")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error = %q, want it to name line 2", err)
	}
}

func TestUnterminatedBlockCommentIsAnError(t *testing.T) {
	_, err := Parse("dump.sql", "CREATE TABLE a (id INT);\n/* what happened here\nCREATE TABLE b (id INT);")
	if err == nil {
		t.Fatal("want an error for an unterminated block comment")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error = %q, want it to name line 2", err)
	}
}

func TestUnterminatedQuotedIdentifierIsAnError(t *testing.T) {
	if _, err := Parse("dump.sql", "CREATE TABLE `orders (id INT);"); err == nil {
		t.Fatal("want an error for an unterminated quoted identifier")
	}
}

// Garbage that declares nothing is not an error: a source that contains no
// CREATE TABLE produced no tables, and that is a fact the counts already state.
func TestGarbageIsSkippedNotRejected(t *testing.T) {
	res, err := Parse("garbage.sql", "hello world; 42; ??? ; DROP TABLE customers; \xef\xbb\xbf")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Entities) != 0 || len(res.Relations) != 0 || len(res.Violations) != 0 {
		t.Errorf("want an empty result, got %+v", res)
	}
}

func TestEmptyInput(t *testing.T) {
	res, err := Parse("empty.sql", "   \n\t -- nothing here\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Entities) != 0 {
		t.Errorf("entities = %v", entityNames(res))
	}
}

// Every prefix of a real dump is a plausible truncation. None of them may panic.
func TestNoPrefixPanics(t *testing.T) {
	const dump = "-- dump\nSET x = 1;\nCREATE TABLE `public`.\"customers\" (\n" +
		"  id INT PRIMARY KEY, /* c */ name VARCHAR(64) DEFAULT 'a;b'\n);\n" +
		"ALTER TABLE ONLY customers ADD CONSTRAINT c FOREIGN KEY (id) REFERENCES [dbo].[x] (id);\n"
	for i := 0; i <= len(dump); i++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on prefix %d (%q): %v", i, dump[:i], r)
				}
			}()
			_, _ = Parse("dump.sql", dump[:i])
		}()
	}
}

// A pipeline reads many sources; an error that does not name the file it came
// from makes the operator find it by hand.
func TestErrorNamesTheSource(t *testing.T) {
	_, err := Parse("schemas/orders.sql", "CREATE TABLE orders (")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "schemas/orders.sql") {
		t.Errorf("error = %q, want it to name the source", err)
	}
}
