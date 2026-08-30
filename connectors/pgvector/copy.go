package pgvector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The bulk path is COPY in its CSV form, written by hand, and both halves of
// that are decisions.
//
// COPY rather than INSERT because §8.4 says a big result is not one INSERT:
// four hundred thousand records through a parameterised statement is four
// hundred thousand round trips of planning, and a multi-row INSERT runs into
// the 65535-parameter limit long before it runs into anything interesting.
//
// CSV rather than pgx's binary CopyFrom because the binary protocol needs a
// codec for every column type, and `vector` is an extension type whose OID is
// per-database and unregistered in a pool the caller may have handed us. The
// choices there were a second dependency to register the type, or letting the
// server parse text it already knows how to parse. The server already has the
// parser; adding a dependency to avoid using it would be the wrong trade in a
// module whose entire reason for existing is that dependencies are not free.
//
// CSV rather than COPY's default text format because the escaping is smaller
// and total: quote every value, double every quote inside it, and that is the
// whole specification. Text format needs backslash escapes for tab, newline,
// carriage return and backslash itself, and gets `\.` wrong at the end of a
// line — four rules against one, over arbitrary document content.

// writeCSV renders one row in the form Postgres reads under FORMAT csv.
//
// Every non-nil value is quoted and nil is written as an empty unquoted field,
// because that is exactly how CSV mode distinguishes the empty string from
// NULL. Getting it the other way round turns every empty heading into a NULL
// and every NULL attribute into '{}' — both of which look fine until somebody
// counts.
func writeCSV(w io.Writer, row []any) error {
	var b bytes.Buffer
	for i, v := range row {
		if i > 0 {
			b.WriteByte(',')
		}
		if v == nil {
			continue // an unquoted empty field is NULL
		}
		s, err := csvValue(v)
		if err != nil {
			return fmt.Errorf("column %d: %w", i, err)
		}
		if strings.IndexByte(s, 0) >= 0 {
			// Postgres text cannot hold a NUL byte in any encoding. Stripping
			// it would be an edit to the buyer's corpus that nothing records,
			// which is the failure this whole connector is arranged against.
			return fmt.Errorf("column %d contains a NUL byte, which no Postgres text column can hold; "+
				"the source needs cleaning before it can be stored", i)
		}
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(s, `"`, `""`))
		b.WriteByte('"')
	}
	b.WriteByte('\n')
	_, err := w.Write(b.Bytes())
	return err
}

func csvValue(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case int:
		return strconv.Itoa(t), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	case bool:
		if t {
			return "t", nil
		}
		return "f", nil
	case json.RawMessage:
		return string(t), nil
	case []float32:
		return vectorLiteral(t), nil
	}
	return "", fmt.Errorf("pgvector: %T is not a value this encoder writes", v)
}

// vectorLiteral renders pgvector's input form. The components are formatted
// with bitSize 32 so each is the shortest decimal that reads back as the same
// float32 — printing a float32 through float64 formatting adds digits that are
// noise, and multiplies the size of the largest table in the store by them.
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v)*8 + 2)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// copyRows loads n rows into one table, l.batch of them per statement.
//
// Each batch is its own COPY and therefore its own transaction, which is the
// point rather than a compromise: one transaction across a four-hundred-
// thousand-record import holds an xmin horizon for the length of the load,
// blocks vacuum, and turns a failure at 99% into a rollback of everything.
// The cost of giving that up is that a failure leaves rows behind — which is
// why the load row exists and is written before any of this, and why the read
// views hide it until the last statement commits. Partial data that nothing
// can see is a cleanup problem; partial data that queries can see is a wrong
// answer with a citation.
func (l *Loader) copyRows(ctx context.Context, table string, cols []string, n int, at func(i int) ([]any, error)) error {
	if n == 0 {
		return nil
	}
	sql := fmt.Sprintf("COPY %s.%s (%s) FROM STDIN WITH (FORMAT csv)", l.schema, table, strings.Join(cols, ", "))
	var buf bytes.Buffer
	for start, batch := 0, 0; start < n; batch++ {
		end := min(start+l.batch, n)
		buf.Reset()
		for i := start; i < end; i++ {
			row, err := at(i)
			if err != nil {
				return fmt.Errorf("pgvector: %s row %d: %w", table, i, err)
			}
			if err := writeCSV(&buf, row); err != nil {
				return fmt.Errorf("pgvector: %s row %d: %w", table, i, err)
			}
		}
		if err := l.copyBatch(ctx, sql, buf.Bytes()); err != nil {
			return fmt.Errorf("pgvector: %s rows %d..%d: %w", table, start, end, err)
		}
		if l.hooks.afterBatch != nil {
			if err := l.hooks.afterBatch(table, batch); err != nil {
				return err
			}
		}
		start = end
	}
	return nil
}

// copyBatch runs one COPY on one pooled connection. The connection is returned
// before the next batch is built, so a slow row-builder does not hold a
// connection the rest of the pool's users are waiting for.
func (l *Loader) copyBatch(ctx context.Context, sql string, body []byte) error {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	_, err = conn.Conn().PgConn().CopyFrom(ctx, bytes.NewReader(body), sql)
	return err
}
