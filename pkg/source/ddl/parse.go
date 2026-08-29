package ddl

import (
	"fmt"
	"strings"
)

// column is one declared column, as declared. The type is kept verbatim
// ("VARCHAR(64)", "NUMERIC(10,2)", "TIMESTAMP WITH TIME ZONE") rather than
// normalised into a family: normalising is a small inference, and this package
// is the one place in the pipeline where nothing is inferred. A consumer that
// wants families can compute them; a consumer that wants the precision of a
// DECIMAL cannot get it back once it has been thrown away.
type column struct {
	name       string
	typ        string
	nullable   bool
	primaryKey bool
}

// foreignKey is one declared reference. Columns is a list because a composite
// key is one relation over several columns, not several relations.
type foreignKey struct {
	constraint string
	columns    []string
	refSchema  string
	refTable   string
	refColumns []string
	line       int
}

// alterFK is a foreign key declared out of line, by ALTER TABLE.
type alterFK struct {
	schema string
	name   string
	fk     foreignKey
}

// alterPK is a primary key declared out of line, which is how pg_dump writes
// every one of them.
type alterPK struct {
	schema string
	name   string
	cols   []string
}

// table is one CREATE TABLE.
type table struct {
	schema      string
	name        string
	line        int
	columns     []column
	primaryKey  []string
	foreignKeys []foreignKey
}

// qualified is the name as a lookup key: schema-qualified when the DDL
// qualified it, bare when it did not.
func qualified(schema, name string) string {
	if schema == "" {
		return strings.ToLower(name)
	}
	return strings.ToLower(schema + "." + name)
}

func (t *table) qualified() string { return qualified(t.schema, t.name) }

// parseCreateTable reads a CREATE TABLE statement.
//
// It returns an error rather than skipping when the statement names a table but
// has no readable body: that is a truncated dump, and an entity carrying
// whichever columns survived the truncation is worse than a failure, because
// the loss is invisible in the output.
func parseCreateTable(toks []token) (*table, error) {
	i := 0
	if !toks[i].isWord("CREATE") {
		return nil, nil
	}
	i++
	// TEMPORARY, UNLOGGED, GLOBAL and friends sit between CREATE and TABLE.
	for i < len(toks) && !toks[i].isWord("TABLE") {
		if !toks[i].isWord("TEMP") && !toks[i].isWord("TEMPORARY") && !toks[i].isWord("UNLOGGED") &&
			!toks[i].isWord("GLOBAL") && !toks[i].isWord("LOCAL") && !toks[i].isWord("EXTERNAL") {
			return nil, nil // CREATE INDEX / VIEW / SEQUENCE: not ours, not an error
		}
		i++
	}
	if i >= len(toks) {
		return nil, nil
	}
	i++ // TABLE
	i = skipIfNotExists(toks, i)
	schema, name, next, ok := parseQualifiedName(toks, i)
	if !ok {
		return nil, fmt.Errorf("line %d: CREATE TABLE without a table name", toks[0].line)
	}
	i = next
	open := indexPunct(toks, i, "(")
	if open < 0 {
		// CREATE TABLE x AS SELECT ... and CREATE TABLE x LIKE y declare a table
		// whose columns are stated somewhere else; there is nothing here to read.
		if hasWord(toks[i:], "AS") || hasWord(toks[i:], "LIKE") {
			return nil, nil
		}
		return nil, fmt.Errorf("line %d: CREATE TABLE %s: no column list", toks[0].line, name)
	}
	closeIdx := matchParen(toks, open)
	if closeIdx < 0 {
		return nil, fmt.Errorf("line %d: CREATE TABLE %s: unbalanced parentheses (truncated?)", toks[0].line, name)
	}

	t := &table{schema: schema, name: name, line: toks[0].line}
	for _, item := range splitTopLevel(toks[open+1 : closeIdx]) {
		parseTableItem(t, item)
	}
	applyPrimaryKey(t)
	return t, nil
}

// parseTableItem reads one comma-separated item of a column list: a column
// definition or a table-level constraint.
func parseTableItem(t *table, item []token) {
	if len(item) == 0 {
		return
	}
	i := 0
	constraint := ""
	if item[i].isWord("CONSTRAINT") && i+1 < len(item) && item[i+1].isIdent() {
		constraint = item[i+1].text
		i += 2
	}
	if i >= len(item) {
		return
	}
	switch {
	case item[i].isWord("PRIMARY") && i+1 < len(item) && item[i+1].isWord("KEY"):
		t.primaryKey = append(t.primaryKey, identList(item, i+2)...)
	case item[i].isWord("FOREIGN") && i+1 < len(item) && item[i+1].isWord("KEY"):
		cols := identList(item, i+2)
		ref := indexWord(item, i, "REFERENCES")
		if ref < 0 || len(cols) == 0 {
			return
		}
		if fk, ok := parseReferences(item, ref, cols); ok {
			fk.constraint = constraint
			t.foreignKeys = append(t.foreignKeys, fk)
		}
	case item[i].isWord("UNIQUE"), item[i].isWord("CHECK"), item[i].isWord("KEY"),
		item[i].isWord("INDEX"), item[i].isWord("EXCLUDE"), item[i].isWord("FULLTEXT"),
		item[i].isWord("SPATIAL"), item[i].isWord("PERIOD"):
		// Constraints this package does not model. Skipping them loses no entity
		// and no relation, which is the only reason skipping is acceptable.
	default:
		parseColumn(t, item[i:], constraint)
	}
}

// parseColumn reads "name TYPE [constraints...]".
func parseColumn(t *table, item []token, constraint string) {
	if !item[0].isIdent() {
		return
	}
	c := column{name: item[0].text, nullable: true}
	i := 1
	typeStart := i
	for i < len(item) && !isColumnConstraintStart(item[i]) {
		if item[i].isPunct("(") {
			end := matchParen(item, i)
			if end < 0 {
				break
			}
			i = end
		}
		i++
	}
	c.typ = renderTokens(item[typeStart:i])

	tail := item[i:]
	for j := 0; j < len(tail); j++ {
		if tail[j].isPunct("(") { // never read a constraint out of CHECK (x IS NOT NULL)
			end := matchParen(tail, j)
			if end < 0 {
				break
			}
			j = end
			continue
		}
		switch {
		case tail[j].isWord("NOT") && j+1 < len(tail) && tail[j+1].isWord("NULL"):
			c.nullable = false
		case tail[j].isWord("PRIMARY") && j+1 < len(tail) && tail[j+1].isWord("KEY"):
			c.primaryKey = true
			t.primaryKey = append(t.primaryKey, c.name)
		case tail[j].isWord("REFERENCES"):
			if fk, ok := parseReferences(tail, j, []string{c.name}); ok {
				fk.constraint = constraint
				t.foreignKeys = append(t.foreignKeys, fk)
			}
		}
	}
	t.columns = append(t.columns, c)
}

// applyPrimaryKey marks the key columns once the whole table is known, because
// a table-level PRIMARY KEY (id) is written after the column it names. A key
// column is NOT NULL by definition in the standard, so saying so is reading the
// DDL rather than guessing about it.
func applyPrimaryKey(t *table) {
	pk := map[string]bool{}
	for _, name := range t.primaryKey {
		pk[strings.ToLower(name)] = true
	}
	for i := range t.columns {
		if pk[strings.ToLower(t.columns[i].name)] {
			t.columns[i].primaryKey = true
			t.columns[i].nullable = false
		}
	}
}

// parseReferences reads "REFERENCES [schema.]table [(col, ...)]" starting at
// the REFERENCES token.
func parseReferences(toks []token, at int, cols []string) (foreignKey, bool) {
	schema, name, next, ok := parseQualifiedName(toks, at+1)
	if !ok {
		return foreignKey{}, false
	}
	fk := foreignKey{columns: cols, refSchema: schema, refTable: name, line: toks[at].line}
	if next < len(toks) && toks[next].isPunct("(") {
		fk.refColumns = identList(toks, next)
	}
	return fk, true
}

// parseAlterForeignKey reads "ALTER TABLE t ADD [CONSTRAINT c] FOREIGN KEY (...)
// REFERENCES ...", which is how dumps declare foreign keys after every table
// exists. Any other ALTER is not an error, just not ours.
func parseAlterForeignKey(toks []token) (schema, name string, fk foreignKey, ok bool) {
	schema, name, i, found := parseAlterHeader(toks)
	if !found {
		return "", "", foreignKey{}, false
	}
	constraint := ""
	if c := indexWord(toks, i, "CONSTRAINT"); c >= 0 && c+1 < len(toks) && toks[c+1].isIdent() {
		constraint = toks[c+1].text
	}
	f := indexWord(toks, i, "FOREIGN")
	if f < 0 || f+1 >= len(toks) || !toks[f+1].isWord("KEY") {
		return "", "", foreignKey{}, false
	}
	cols := identList(toks, f+2)
	ref := indexWord(toks, f, "REFERENCES")
	if ref < 0 || len(cols) == 0 {
		return "", "", foreignKey{}, false
	}
	fk, ok = parseReferences(toks, ref, cols)
	if !ok {
		return "", "", foreignKey{}, false
	}
	fk.constraint = constraint
	return schema, name, fk, true
}

// parseAlterPrimaryKey reads "ALTER TABLE t ADD [CONSTRAINT c] PRIMARY KEY (...)".
func parseAlterPrimaryKey(toks []token) (schema, name string, cols []string, ok bool) {
	schema, name, i, found := parseAlterHeader(toks)
	if !found {
		return "", "", nil, false
	}
	p := indexWord(toks, i, "PRIMARY")
	if p < 0 || p+1 >= len(toks) || !toks[p+1].isWord("KEY") {
		return "", "", nil, false
	}
	cols = identList(toks, p+2)
	if len(cols) == 0 {
		return "", "", nil, false
	}
	return schema, name, cols, true
}

// parseAlterHeader reads "ALTER TABLE [ONLY] [IF EXISTS] name" and returns the
// index just past the name.
func parseAlterHeader(toks []token) (schema, name string, next int, ok bool) {
	i := 0
	if !toks[i].isWord("ALTER") || i+1 >= len(toks) || !toks[i+1].isWord("TABLE") {
		return "", "", 0, false
	}
	i += 2
	for i < len(toks) && (toks[i].isWord("ONLY") || toks[i].isWord("IF") || toks[i].isWord("EXISTS")) {
		i++
	}
	return parseQualifiedName(toks, i)
}

func skipIfNotExists(toks []token, i int) int {
	if i+2 < len(toks) && toks[i].isWord("IF") && toks[i+1].isWord("NOT") && toks[i+2].isWord("EXISTS") {
		return i + 3
	}
	return i
}

// parseQualifiedName reads db.schema.table or schema.table or table, keeping
// only the last two parts: a catalogue prefix identifies the same table.
func parseQualifiedName(toks []token, i int) (schema, name string, next int, ok bool) {
	if i >= len(toks) || !toks[i].isIdent() {
		return "", "", i, false
	}
	parts := []string{toks[i].text}
	i++
	for i+1 < len(toks) && toks[i].isPunct(".") && toks[i+1].isIdent() {
		parts = append(parts, toks[i+1].text)
		i += 2
	}
	name = parts[len(parts)-1]
	if len(parts) > 1 {
		schema = parts[len(parts)-2]
	}
	return schema, name, i, true
}

// identList reads the identifiers of the first parenthesised group at or after
// i, e.g. "(a, b)" -> [a b].
func identList(toks []token, i int) []string {
	open := indexPunct(toks, i, "(")
	if open < 0 {
		return nil
	}
	end := matchParen(toks, open)
	if end < 0 {
		return nil
	}
	var out []string
	for _, part := range splitTopLevel(toks[open+1 : end]) {
		if len(part) > 0 && part[0].isIdent() {
			out = append(out, part[0].text)
		}
	}
	return out
}

// isColumnConstraintStart reports whether a token ends the type and starts the
// constraint tail of a column definition.
func isColumnConstraintStart(t token) bool {
	if t.kind != tokWord {
		return false
	}
	switch strings.ToUpper(t.text) {
	case "NOT", "NULL", "PRIMARY", "UNIQUE", "KEY", "REFERENCES", "DEFAULT", "CHECK",
		"CONSTRAINT", "COLLATE", "COMMENT", "GENERATED", "IDENTITY", "AUTO_INCREMENT",
		"AUTOINCREMENT", "ON", "AS", "STORED", "VIRTUAL", "SERIAL":
		return true
	}
	return false
}

// renderTokens puts a token run back together as text, spacing it the way SQL
// is normally written so a declared type reads as "NUMERIC(10,2)".
func renderTokens(toks []token) string {
	var b strings.Builder
	for i, t := range toks {
		text := t.text
		if t.kind == tokString {
			text = "'" + text + "'"
		}
		if i > 0 && !t.isPunct("(") && !t.isPunct(")") && !t.isPunct(",") &&
			!toks[i-1].isPunct("(") && !toks[i-1].isPunct(",") {
			b.WriteByte(' ')
		}
		b.WriteString(text)
	}
	return b.String()
}

func indexPunct(toks []token, from int, p string) int {
	for i := from; i < len(toks); i++ {
		if toks[i].isPunct(p) {
			return i
		}
	}
	return -1
}

func indexWord(toks []token, from int, kw string) int {
	depth := 0
	for i := from; i < len(toks); i++ {
		switch {
		case toks[i].isPunct("("):
			depth++
		case toks[i].isPunct(")"):
			depth--
		case depth == 0 && toks[i].isWord(kw):
			return i
		}
	}
	return -1
}

func hasWord(toks []token, kw string) bool { return indexWord(toks, 0, kw) >= 0 }

// matchParen returns the index of the ')' closing the '(' at open.
func matchParen(toks []token, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		switch {
		case toks[i].isPunct("("):
			depth++
		case toks[i].isPunct(")"):
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTopLevel cuts a token run on commas that are not inside parentheses.
func splitTopLevel(toks []token) [][]token {
	var out [][]token
	depth, start := 0, 0
	for i, t := range toks {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case t.isPunct(",") && depth == 0:
			out = append(out, toks[start:i])
			start = i + 1
		}
	}
	return append(out, toks[start:])
}
