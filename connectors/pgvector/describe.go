package pgvector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Describe returns one entity whole.
//
// The columns are named rather than taken with SELECT *, because this table has
// a shape this package owns; what it cannot name is the CONTENTS of the
// attributes column, which is jsonb precisely so a source's own fields need no
// migration. So the row is a fixed projection and the attributes are decoded
// out of one cell of it.
//
// NULL and '{}' are kept apart on the way back the way they are on the way in:
// attrs writes NULL for a record that said nothing beyond type and name, and an
// empty map for one that stated an empty object. A reader counting fields
// should be able to tell "the source was silent" from "the source said
// nothing", which is the same distinction Contributions draws about names.
func (l *Loader) Describe(ctx context.Context, load, id string) (recall.Description, error) {
	const sql = `SELECT type, name, aliases, attributes, ` + provCols + `
	FROM {s}.loaded_entities WHERE load_id = $1 AND entity_id = $2::text`
	var (
		d       recall.Description
		aliases []string
		raw     []byte
		p       alchemy.Provenance
		det     bool
	)
	err := l.pool.QueryRow(ctx, l.q(sql), load, id).Scan(
		&d.Type, &d.Name, &aliases, &raw,
		&p.Source, &p.Chunk, &p.Producer, &det, &p.Model,
		&p.Ontology, &p.Chunking, &p.Confidence, &p.ReviewedBy, &p.RuleSet, &p.RuledBy,
		&p.By, &p.At)
	if errors.Is(err, pgx.ErrNoRows) {
		// The two absences, told apart the way Contributions tells them apart:
		// an id this load does not hold is an ordinary answer, and a load that
		// is not here is the caller naming the wrong import.
		return recall.Description{}, l.noEntity(ctx, load)
	}
	if err != nil {
		return recall.Description{}, fmt.Errorf("pgvector: describe %q in load %q: %w", id, load, err)
	}
	// prov_deterministic is scanned and dropped. It is written for the buyer's
	// own SQL and it is the rule as it stood on the day of the import;
	// recall.NewClaim gives the argument for reading the rule today instead.
	_ = det
	d.ID, d.Aliases, d.Provenance = id, aliases, p
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &d.Attributes); err != nil {
			return recall.Description{}, fmt.Errorf("pgvector: describe %q: attributes: %w", id, err)
		}
	}
	return d, nil
}

func (l *Loader) noEntity(ctx context.Context, load string) error {
	var n int
	err := l.pool.QueryRow(ctx, l.q(`SELECT count(*) FROM {s}.loads WHERE id = $1 AND state = '`+stateComplete+`'`), load).Scan(&n)
	if err != nil {
		return fmt.Errorf("pgvector: reading load %q: %w", load, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %q is not a finished load in this schema; "+
			"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
			recall.ErrNoLoad, load)
	}
	return nil
}
