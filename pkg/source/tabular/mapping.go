package tabular

import (
	"fmt"
	"strings"
)

// Mapping says what a row becomes. It is either supplied by the caller or
// inferred; nothing else about reading the table depends on which.
type Mapping struct {
	// EntityType is what a row becomes.
	EntityType string
	// IDColumn identifies the row. It is the one field with no default: an
	// entity whose identity was invented does not survive a re-import.
	IDColumn   string
	NameColumn string
	// Attributes maps a source column to the attribute name it becomes.
	Attributes map[string]string
	// Relations maps a source column to an edge pointing at another entity.
	Relations []RelationMapping
}

// RelationMapping turns one column into an edge.
type RelationMapping struct {
	// Column holds the target entity's identifier.
	Column string
	// RelationType is the edge type.
	RelationType string
	// TargetType is the entity type the column points at. It decides the
	// target's ID, so it is required.
	TargetType string
}

// validate refuses a mapping that names a column this table does not have.
//
// It is an error rather than a violation, and the same error whether the
// mapping was supplied or inferred. A mapping is a statement about one table:
// if it names "custmer_id" then either it belongs to a different file or the
// model misread the header, and in both cases every row read under it is wrong
// in a way the rows themselves cannot show. Reporting it per column and reading
// on would produce exactly the table that runs cleanly and does not add up.
func validate(m *Mapping, head []string) error {
	has := columnIndex(head)
	if m.EntityType == "" {
		return fmt.Errorf("the mapping names no entity type")
	}
	if m.IDColumn == "" {
		return fmt.Errorf("the mapping names no id column")
	}
	roles := map[string]string{}
	for _, c := range m.columns() {
		if _, ok := has[c.name]; !ok {
			return fmt.Errorf("%s names column %q, which the header does not have; the header is %s",
				c.role, c.name, strings.Join(named(head), ", "))
		}
		// A column given two roles does not say what it becomes: an attribute
		// shadowing the identity, or an edge from a row to itself. Neither is
		// visible in the output, which is what makes it worth refusing.
		if first, dup := roles[c.name]; dup {
			return fmt.Errorf("column %q is mapped twice, as %s and as %s, so the mapping does not say what it becomes",
				c.name, first, c.role)
		}
		roles[c.name] = c.role
	}
	for _, r := range m.Relations {
		if r.RelationType == "" || r.TargetType == "" {
			return fmt.Errorf("the relation on column %q names no relation type or no target type, so its edge would point at nothing", r.Column)
		}
	}
	return nil
}

type mappedColumn struct{ role, name string }

func (m *Mapping) columns() []mappedColumn {
	out := []mappedColumn{{"the id column", m.IDColumn}}
	if m.NameColumn != "" {
		out = append(out, mappedColumn{"the name column", m.NameColumn})
	}
	for _, col := range sortedKeys(m.Attributes) {
		out = append(out, mappedColumn{"the attribute " + m.Attributes[col], col})
	}
	for _, r := range m.Relations {
		out = append(out, mappedColumn{"the relation " + r.RelationType, r.Column})
	}
	return out
}
