package tabular

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// record is one row and the line it was on. The line travels with the fields
// because every report this package makes has to be findable in the file, and
// by the time a row is rejected the reader has moved on.
type record struct {
	fields []string
	line   int
}

// reader hands out records: the sampled ones first, then the rest of the
// stream. The sample was already consumed to build the model's prompt, and
// re-reading the source is not an option — it is an io.Reader, and §8.4 forbids
// holding one whole.
type reader struct {
	pending []record
	cr      *csv.Reader
}

func (rd *reader) next() (record, error) {
	if len(rd.pending) > 0 {
		rec := rd.pending[0]
		rd.pending = rd.pending[1:]
		return rec, nil
	}
	fields, err := rd.cr.Read()
	if err != nil {
		return record{}, err
	}
	line, _ := rd.cr.FieldPos(0)
	return record{fields: fields, line: line}, nil
}

// rows streams the table one record at a time. Nothing accumulates here except
// the graph itself and one fingerprint per emitted id (see ids.go).
func rows(source string, rd *reader, head []string, m *Mapping, prov alchemy.Provenance, res *Result) error {
	index := columnIndex(head)
	seen := newIDs()
	for {
		rec, err := rd.next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			// A quote left open makes every following row boundary fiction, so
			// reading on would swallow an unknown number of rows. pkg/source/ddl
			// refuses an unterminated literal for the same reason.
			return err
		}
		if len(rec.fields) != len(head) {
			res.Violations = append(res.Violations, violation(ViolationMalformedRow,
				at(source, rec.line),
				fmt.Sprintf("the row has %s where the header has %s, so no cell can be matched to a column; the row is skipped",
					plural(len(rec.fields), "field"), plural(len(head), "field")),
				prov))
			continue
		}
		emit(source, rec, index, m, prov, seen, res)
	}
}

func emit(source string, rec record, index map[string]int, m *Mapping, prov alchemy.Provenance, seen *ids, res *Result) {
	// Values reach the graph as the file has them. A value this reader trimmed
	// is a value that no longer matches the source, and a consumer checking the
	// graph against the file would not find it.
	cell := func(col string) string {
		i, ok := index[col]
		if !ok || i >= len(rec.fields) {
			return ""
		}
		return rec.fields[i]
	}
	// Identity is the exception: an id differing from another by a space is a
	// duplicate nobody can see, so ids — and the cells that point at them — are
	// normalised, and only they.
	key := strings.TrimSpace(cell(m.IDColumn))
	if key == "" {
		// The alternative is an id made from the row's position, and an entity
		// whose identity is its line number is a different entity after the
		// file is re-sorted. A re-import would then read as new data.
		res.Violations = append(res.Violations, violation(ViolationMissingID,
			at(source, rec.line),
			fmt.Sprintf("column %q is empty, so the row has no identity that survives a re-import; the row is skipped", m.IDColumn),
			prov))
		return
	}
	id := entityID(m.EntityType, key)
	if first, differs, dup := seen.add(id, rec.line, fingerprint(mapped(cell, m))); dup {
		if differs {
			res.Violations = append(res.Violations, violation(ViolationDuplicateID,
				at(source, rec.line),
				fmt.Sprintf("id %q was already claimed by line %d with different values, so column %q does not identify a row; the first row is kept",
					id, first, m.IDColumn),
				prov))
		}
		return
	}
	e := alchemy.Entity{ID: id, Type: m.EntityType, Name: cell(m.NameColumn), Provenance: prov}
	for col, attr := range m.Attributes {
		if e.Attributes == nil {
			e.Attributes = map[string]any{}
		}
		e.Attributes[attr] = cell(col)
	}
	res.Entities = append(res.Entities, e)
	for _, r := range m.Relations {
		target := strings.TrimSpace(cell(r.Column))
		if target == "" {
			// An empty foreign key says "no link", which is a fact about the
			// row rather than a defect in it. An edge to nothing would be.
			continue
		}
		res.Relations = append(res.Relations, alchemy.Relation{
			From:       id,
			To:         entityID(r.TargetType, target),
			Type:       r.RelationType,
			Provenance: prov,
		})
	}
}

// mapped is every cell the mapping reads, in an order that does not depend on
// map iteration, so two rows producing the same entity fingerprint the same.
func mapped(cell func(string) string, m *Mapping) []string {
	out := []string{cell(m.IDColumn), cell(m.NameColumn)}
	for _, col := range sortedKeys(m.Attributes) {
		out = append(out, m.Attributes[col], cell(col))
	}
	for _, r := range m.Relations {
		out = append(out, r.RelationType, r.TargetType, cell(r.Column))
	}
	return out
}

// entityID is derived from the data, never from the row's position, so the same
// row read twice — or read again next month out of a re-sorted export — is the
// same entity. It is readable rather than hashed because provenance is for
// people: "order:1001" can be looked up in the source, a digest cannot.
func entityID(entityType, id string) string {
	return strings.ToLower(entityType) + ":" + strings.TrimSpace(id)
}

// plural keeps a violation readable: "1 fields" reads like a bug in the message
// and invites a reader to distrust the number.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
