package ddl

import (
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Result is what one DDL source produced. It is deliberately narrower than
// alchemy.Result: a schema has no chunks, no vectors and no guesses, and
// returning empty fields for them would invite a reader to wonder what
// deterministic guesses look like.
type Result struct {
	Entities   []alchemy.Entity
	Relations  []alchemy.Relation
	Violations []alchemy.Violation
	// Conflicts is one file contradicting itself: the same table declared twice
	// with different columns. DESIGN.md §7.3 forbids resolving that silently —
	// nothing in the DDL says which declaration is right, and a conflict is a
	// question for a person rather than a number to report.
	Conflicts []alchemy.Conflict
}

// build turns parsed tables into the graph.
func build(source string, tables []*table, alters []alterFK, keys []alterPK) Result {
	var res Result
	tables, dups := dedupe(tables)
	for _, d := range dups {
		res.Conflicts = append(res.Conflicts, dupConflict(source, d))
	}
	byName := index(tables)
	for _, k := range keys {
		// A key declared for a table this source does not define has no entity to
		// belong to. Nothing is lost that could be carried: unlike a foreign key,
		// a primary key is an attribute of an absent node, not an edge.
		if t, why := byName.table(k.schema, k.name); why == "" {
			t.primaryKey = append(t.primaryKey, k.cols...)
			applyPrimaryKey(t)
		}
	}
	for _, a := range alters {
		t, why := byName.table(a.schema, a.name)
		if why != "" {
			// The table being altered is the one this source does not define, so
			// the reason has to name it rather than the reference's target.
			owner := &table{schema: a.schema, name: a.name}
			res.Violations = append(res.Violations, danglingViolation(source, owner, a.fk,
				"ALTER TABLE "+refOf(a.schema, a.name)+" has "+why))
			continue
		}
		t.foreignKeys = append(t.foreignKeys, a.fk)
	}
	for _, t := range tables {
		res.Entities = append(res.Entities, entityFor(source, t, byName))
	}
	for _, t := range tables {
		for _, fk := range t.foreignKeys {
			to, why := byName.resolve(fk.refSchema, fk.refTable)
			if why != "" {
				res.Violations = append(res.Violations, danglingViolation(source, t, fk, why))
				continue
			}
			res.Relations = append(res.Relations, relationFor(source, t, fk, to))
		}
	}
	return res
}

// duplicate is one table declared more than once in a single source.
type duplicate struct {
	first, later *table
}

// dedupe collapses declarations of the same qualified table name.
//
// Two entities sharing an ID would break every consumer that walks the graph by
// id, so a collapse is required. Which declaration survives is decided by
// declaration order, and that would be exactly the silent guess §2.1 warns
// about — except that the ones that disagree are reported as conflicts, so no
// difference is resolved without someone seeing it. Foreign keys from every
// declaration are kept, because a relation is the one thing this package must
// never drop.
func dedupe(tables []*table) ([]*table, []duplicate) {
	var kept []*table
	var dups []duplicate
	seen := map[string]*table{}
	for _, t := range tables {
		first, ok := seen[t.qualified()]
		if !ok {
			seen[t.qualified()] = t
			kept = append(kept, t)
			continue
		}
		if signature(first) != signature(t) {
			dups = append(dups, duplicate{first: first, later: t})
		}
		for _, fk := range t.foreignKeys {
			if !hasForeignKey(first, fk) {
				first.foreignKeys = append(first.foreignKeys, fk)
			}
		}
	}
	return kept, dups
}

func hasForeignKey(t *table, fk foreignKey) bool {
	for _, have := range t.foreignKeys {
		if signatureFK(have) == signatureFK(fk) {
			return true
		}
	}
	return false
}

// signature renders everything a table declares, in declaration order, so that
// "the same table twice" and "two different tables under one name" can be told
// apart without asking what a difference means.
func signature(t *table) string {
	var b strings.Builder
	for _, c := range t.columns {
		fmt.Fprintf(&b, "%s %s null=%t pk=%t; ", c.name, c.typ, c.nullable, c.primaryKey)
	}
	fmt.Fprintf(&b, "primary key (%s); ", strings.Join(t.primaryKey, ", "))
	for _, fk := range t.foreignKeys {
		b.WriteString(signatureFK(fk) + "; ")
	}
	return b.String()
}

func signatureFK(fk foreignKey) string {
	return fmt.Sprintf("foreign key (%s) references %s(%s)",
		strings.Join(fk.columns, ", "), refName(fk), strings.Join(fk.refColumns, ", "))
}

func dupConflict(source string, d duplicate) alchemy.Conflict {
	return alchemy.Conflict{
		Kind:    alchemy.ConflictEntityAttributes,
		Subject: entityID(d.first.schema, d.first.name),
		Detail: fmt.Sprintf("%s is declared twice in %s with different columns; nothing in the DDL says which is current",
			refOf(d.first.schema, d.first.name), source),
		Left:  claimFor(source, d.first),
		Right: claimFor(source, d.later),
	}
}

func claimFor(source string, t *table) alchemy.Claim {
	return alchemy.Claim{
		Statement:  fmt.Sprintf("line %d: %s", t.line, signature(t)),
		Provenance: prov(source),
	}
}

// tableIndex resolves the name a foreign key writes to the entity a table
// declared. The two are not always spelled alike: a dump qualifies one and not
// the other, and both spellings mean the same table.
type tableIndex struct {
	byQualified map[string]*table
	byBare      map[string][]*table
}

func index(tables []*table) tableIndex {
	ix := tableIndex{byQualified: map[string]*table{}, byBare: map[string][]*table{}}
	for _, t := range tables {
		ix.byQualified[t.qualified()] = t
		bare := strings.ToLower(t.name)
		ix.byBare[bare] = append(ix.byBare[bare], t)
	}
	return ix
}

// resolve finds the entity a reference names. A bare name resolves to a
// qualified table only when exactly one table carries it: two schemas holding a
// table of the same name is a genuine ambiguity, and picking the first would be
// a guess made silently by a package whose whole claim is that it makes none.
// It returns an empty reason on success and, on failure, why the reference
// could not be resolved — which becomes the Detail a person reads.
func (ix tableIndex) resolve(schema, name string) (id, reason string) {
	t, reason := ix.table(schema, name)
	if reason != "" {
		return "", reason
	}
	return entityID(t.schema, t.name), ""
}

// table finds the declared table a name refers to.
func (ix tableIndex) table(schema, name string) (*table, string) {
	if t, ok := ix.byQualified[qualified(schema, name)]; ok {
		return t, ""
	}
	switch cands := ix.byBare[strings.ToLower(name)]; len(cands) {
	case 1:
		return cands[0], ""
	case 0:
		return nil, "no CREATE TABLE for it in this source"
	default:
		schemas := make([]string, 0, len(cands))
		for _, c := range cands {
			schemas = append(schemas, c.qualified())
		}
		sort.Strings(schemas)
		return nil, "ambiguous: this source defines " + strings.Join(schemas, " and ")
	}
}

// danglingViolation reports a foreign key whose target this source does not
// define. DESIGN.md §5b: an edge that does not fit is "not silently dropped and
// not silently kept" — it is returned with enough detail to act on. It is kept
// out of Relations because a relation pointing at an entity the result does not
// contain would corrupt every consumer that walks the graph; the whole edge
// survives here instead, in words, so nothing is lost.
func danglingViolation(source string, from *table, fk foreignKey, reason string) alchemy.Violation {
	return alchemy.Violation{
		Kind: alchemy.ViolationDanglingRelation,
		Detail: fmt.Sprintf("%s.%s references %s(%s) but %s",
			from.name, strings.Join(fk.columns, ","),
			refName(fk), strings.Join(fk.refColumns, ","), reason),
		Subject: fmt.Sprintf("%s -[%s]-> %s", entityID(from.schema, from.name), RelationType, refName(fk)),
		// The same edge in fields, so a consumer can act on the finding without
		// parsing the sentence above. To is the reference as the DDL wrote it
		// rather than an entity ID, because that is the whole finding: there is
		// no entity for it to be the ID of. The key travels for the reason
		// alchemy.Relation.Key exists — a table with two unresolvable foreign
		// keys onto one name states two of these, and a Ref that could not tell
		// them apart would let one report stand for both.
		About: alchemy.Ref{
			Kind: alchemy.RefRelation, Type: RelationType, Key: edgeKey(fk),
			From: entityID(from.schema, from.name), To: refName(fk),
		},
		Provenance: prov(source),
	}
}

// refName renders a reference the way the DDL wrote it, qualified or not, so a
// person can grep the source for it.
func refName(fk foreignKey) string { return refOf(fk.refSchema, fk.refTable) }

func refOf(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

func relationFor(source string, from *table, fk foreignKey, to string) alchemy.Relation {
	attrs := map[string]any{"columns": jsonList(fk.columns)}
	if len(fk.refColumns) > 0 {
		attrs["references"] = jsonList(fk.refColumns)
	}
	if fk.constraint != "" {
		attrs["constraint"] = fk.constraint
	}
	return alchemy.Relation{
		From:       entityID(from.schema, from.name),
		To:         to,
		Type:       RelationType,
		Key:        edgeKey(fk),
		Attributes: attrs,
		Provenance: prov(source),
	}
}

// edgeKey is what tells one of a table's foreign keys from another, which is
// what alchemy.Relation.Key is for. A table that references one table twice —
// each end of a connection between two of its rows — is ordinary SQL, and both
// edges are real; without this the verifier reads them as one edge two sources
// disagree about, because the only thing separating them is what they say.
//
// The constraint name is the answer whenever the schema gave one: SQL requires
// it to be unique among the table's constraints, so it is an identity the
// source itself states rather than one this package invented.
//
// An unnamed foreign key is keyed by the columns it constrains, and that is not
// a guess either. A second FOREIGN KEY clause over the same columns onto the
// same table is the same constraint written twice, so collapsing those two is
// right and separating any other pair is right. Falling back matters because
// schemas that name no constraints are common, and a fix that only worked for
// customers who name theirs would be half a fix.
// jsonList widens a list of names into the shape alchemy.Entity.Attributes
// declares: []any, the thing encoding/json produces when it decodes a JSON
// array into an any.
//
// A []string is the natural Go value here and is the wrong one, for a reason
// that only shows up once there are two consumers. It marshals to the identical
// JSON, so nothing in a document changes; what changes is the Go type a
// consumer meets, and a consumer holding this Result directly would meet
// []string where one reading the same result off §6's wire meets []any. A store
// branching on the type — and one of the four does, to decide whether a value
// is a list its property model can hold or a nested value it has to render as
// text with a breadcrumb — then writes the same schema two ways depending on
// which side of a wire its caller was on. Widening here is what makes those two
// paths one graph.
func jsonList(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func edgeKey(fk foreignKey) string {
	if fk.constraint != "" {
		return fk.constraint
	}
	return "(" + strings.Join(fk.columns, ", ") + ")"
}

func entityFor(source string, t *table, ix tableIndex) alchemy.Entity {
	fkCols := map[string]bool{}
	for _, fk := range t.foreignKeys {
		for _, c := range fk.columns {
			fkCols[strings.ToLower(c)] = true
		}
	}
	cols := map[string]any{}
	for _, c := range t.columns {
		cols[c.name] = map[string]any{
			"type":        c.typ,
			"nullable":    c.nullable,
			"primary_key": c.primaryKey,
			"foreign_key": fkCols[strings.ToLower(c.name)],
		}
	}
	attrs := map[string]any{"table": t.name, "columns": cols}
	if t.schema != "" {
		attrs["schema"] = t.schema
	}
	if len(t.primaryKey) > 0 {
		attrs["primary_key"] = jsonList(t.primaryKey)
	}
	if links, ok := junctionOf(t, ix); ok {
		attrs["junction"] = true
		if links != nil {
			attrs["junction_of"] = jsonList(links)
		}
	}
	return alchemy.Entity{
		ID:         entityID(t.schema, t.name),
		Type:       EntityType,
		Name:       t.name,
		Attributes: attrs,
		Provenance: prov(source),
	}
}

// entityID is stable within one result and is what relations point at.
func entityID(schema, name string) string { return "table:" + qualified(schema, name) }

// junctionOf reports whether t is a pure link table — its columns are exactly
// two foreign keys, plus at most a primary key over them or a surrogate one —
// and, when it is, the ids of the two entities it links.
//
// The decision this encodes: a junction table is marked, and never collapsed.
//
// Collapsing users <- user_roles -> roles into a single users->roles edge is
// what a graph modeller usually wants, and it is exactly what this package must
// not do. The edge would have to be given a direction and a type that no
// statement in the file states: which side is From is decided by column order,
// which is arbitrary, and the type would have to be invented from the table's
// name. That is inference, and an inferred edge carrying Producer ddl — the
// marker DESIGN.md §5b defines as "a machine read something that already said
// this" — is the precise failure this package exists to avoid. It would also
// lose information: a link table can carry a constraint name, a composite key
// and its own columns, and after collapsing, the fact that it ever existed is
// gone.
//
// So both declared REFERENCES edges are emitted, the entity is kept, and the
// shape is reported as an attribute. A consumer that wants a many-to-many edge
// has everything it needs to build one and, crucially, owns the choice of
// direction and name — which is a modelling decision, and modelling decisions
// belong to the caller, not to a deterministic reader.
//
// The rule is deliberately strict: one extra column that is not part of a key
// and the table is no longer a junction, because that column is a fact about
// the relationship (when a role was granted, by whom) that collapsing would
// discard.
func junctionOf(t *table, ix tableIndex) ([]string, bool) {
	if len(t.foreignKeys) != 2 || len(t.columns) == 0 {
		return nil, false
	}
	covered := map[string]bool{}
	for _, fk := range t.foreignKeys {
		for _, c := range fk.columns {
			covered[strings.ToLower(c)] = true
		}
	}
	for _, pk := range t.primaryKey {
		covered[strings.ToLower(pk)] = true
	}
	for _, c := range t.columns {
		if !covered[strings.ToLower(c.name)] {
			return nil, false
		}
	}
	links := make([]string, 0, 2)
	for _, fk := range t.foreignKeys {
		id, why := ix.resolve(fk.refSchema, fk.refTable)
		if why != "" {
			// The shape is still a junction, but half of what it links is not in
			// this source; an id pointing at nothing would be a worse answer than
			// no id, and the missing side is already a Violation.
			return nil, true
		}
		links = append(links, id)
	}
	return links, true
}
