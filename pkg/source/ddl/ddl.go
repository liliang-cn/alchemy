// Package ddl turns SQL DDL — a schema file or a dump — into entities and
// relations without a model.
//
// DESIGN.md §2.1: "Deterministic, no LLM — the database schema *is* the
// ground-truth ontology." A CREATE TABLE already states the entity and a
// FOREIGN KEY already states the relation, so there is nothing here to infer
// and nothing to be confident about. Everything this package emits carries
// Producer ddl and Chunk -1, and no Model and no Confidence: those fields exist
// to describe a guess, and a guess is the one thing this package never makes.
//
// What follows from that is the shape of the whole package. Nothing is dropped
// quietly: a foreign key whose target the source does not define becomes a
// Violation rather than a missing edge, a table declared twice with different
// columns becomes a Conflict rather than a silent winner, and a link table is
// marked rather than collapsed, because the collapsed edge's direction and name
// are nowhere in the file. Where the input cannot be read at all — an
// unterminated literal, a truncated CREATE TABLE — Parse fails, naming the
// source and the line, rather than returning the half it managed to read.
package ddl

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// EntityType is the type every table entity carries. It is a constant rather
// than a guess at a domain type: the DDL says "table", and inventing "Customer"
// out of a table called customers would be inference wearing a deterministic
// producer's badge.
const EntityType = "Table"

// RelationType is the type every foreign-key edge carries, for the same reason
// EntityType is a constant: REFERENCES is what the DDL said. Naming the edge
// "PLACED_BY" because the column was called customer_id would be reading a
// domain out of a naming convention.
const RelationType = "REFERENCES"

// Parse turns SQL DDL — a schema file or a dump — into entities and relations.
//
// Statements that are neither CREATE TABLE nor an ALTER TABLE declaring a
// foreign key are skipped rather than rejected, because real dumps are mostly
// SET, INSERT, CREATE INDEX and sequence statements and a reader that fails on
// them cannot read a real dump.
func Parse(source, ddl string) (Result, error) {
	toks, err := lex(ddl)
	if err != nil {
		return Result{}, sourceErr(source, err)
	}
	var tables []*table
	var alters []alterFK
	var keys []alterPK
	for _, stmt := range splitStatements(toks) {
		t, err := parseCreateTable(stmt)
		if err != nil {
			return Result{}, sourceErr(source, err)
		}
		if t != nil {
			tables = append(tables, t)
			continue
		}
		// A dump declares its foreign keys after all its tables, so ALTER
		// statements are collected and applied once every table is known.
		if schema, name, fk, ok := parseAlterForeignKey(stmt); ok {
			alters = append(alters, alterFK{schema: schema, name: name, fk: fk})
			continue
		}
		if schema, name, cols, ok := parseAlterPrimaryKey(stmt); ok {
			keys = append(keys, alterPK{schema: schema, name: name, cols: cols})
		}
	}
	return build(source, tables, alters, keys), nil
}

// sourceErr names the file. A pipeline reads many sources, and an error that
// says only "line 41" leaves the operator to work out which of them it meant.
func sourceErr(source string, err error) error {
	return fmt.Errorf("ddl: %s: %w", source, err)
}

// prov is the only provenance this package ever produces. Chunk is -1 because
// DDL does not work in chunks, and Model and Confidence stay zero because
// nothing here was inferred (DESIGN.md §5b).
func prov(source string) alchemy.Provenance {
	return alchemy.Provenance{Source: source, Chunk: -1, Producer: alchemy.ProducerDDL}
}
