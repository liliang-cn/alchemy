// Package tabular turns CSV, TSV and other delimited data into entities and
// relations.
//
// It is the counterpart of pkg/source/ddl and differs from it in one way that
// decides everything else: a CREATE TABLE states what its columns mean, and a
// CSV header does not. "id" is a column name, not a declaration, so a mapping
// from columns to a graph has to come from somewhere — either from the caller,
// or from a model.
//
// DESIGN.md §2.1 names what goes wrong when it comes from a model and nobody is
// told:
//
//	子配这一步是这一层唯一会静悄悄出错的地方 — id 同时是 order_id 和 product_id
//	的子串，取哪一个只看列在源里的先后顺序，而两种取法都会跑得干干净净。一个猜错的
//	映射不会报错，它只会让一整张表对不上账，然后在三个月后由一个人手工发现。
//
// So this package never resolves an ambiguity by column order. It has exactly
// three modes and no fourth: a caller-supplied Mapping (deterministic, no model,
// no Guess), an inferred Mapping (every decision reported as an alchemy.Guess
// carrying what else the column could have been), or an error. There is no
// heuristic fallback, because a heuristic fallback is the failure above wearing
// a different name — and the same rule decides the delimiter, which is only
// sniffed when exactly one candidate fits and is otherwise refused.
//
// The second rule is that nothing is dropped in silence. A row that does not
// fit its header, a row with no identity, a row claiming an identity another
// row already claimed with different values, a column the header failed to
// name: each is skipped and returned in Violations with the line it was on.
// A row that vanishes between the file and the graph is found the same way a
// wrong mapping is — months later, by hand.
package tabular

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Options configures one read.
type Options struct {
	// Delimiter is the field separator. Zero means sniff it (see sniff.go),
	// and a sniff that cannot decide is an error rather than a pick.
	Delimiter rune
	// Mapping, when supplied, is used verbatim: no model is called and the
	// result carries no Guess, because nothing was guessed.
	Mapping *Mapping
	// LLM infers the mapping when Mapping is nil. Both nil is an error — a
	// stage that needs a model it was not given fails loudly (pkg/alchemy
	// ports.go) rather than inventing a mapping out of column order.
	LLM alchemy.LLM
	// EntityHint is what the caller believes a row is ("Order"). It is a hint
	// to the inference, not a constraint on it, and it is ignored when Mapping
	// is supplied.
	EntityHint string
}

// Result is what one table produced. It is narrower than alchemy.Result: a
// table has no chunks and no vectors.
type Result struct {
	Entities   []alchemy.Entity
	Relations  []alchemy.Relation
	Guesses    []alchemy.Guess
	Violations []alchemy.Violation
	ModelCalls []alchemy.ModelCall
}

// Read turns a delimited table into entities and relations.
//
// The source is streamed: at most a bounded window of it is ever held, and rows
// are converted one at a time (DESIGN.md §8.4).
//
// On error the Result is still returned, carrying the ModelCalls that were paid
// for before the failure. §7.2: cost is never hidden, and a failed job that
// reports no calls makes an expensive retry look free.
func Read(ctx context.Context, source string, r io.Reader, opts Options) (Result, error) {
	br := bufio.NewReaderSize(r, sniffWindow)
	stripBOM(br)
	delim := opts.Delimiter
	if delim == 0 {
		var err error
		if delim, err = sniff(br); err != nil {
			return Result{}, sourceErr(source, err)
		}
	}
	cr := csv.NewReader(br)
	cr.Comma = delim
	// The record length is checked against the header in rows() rather than by
	// encoding/csv, so that a row of the wrong width can be reported and skipped
	// instead of ending the read.
	cr.FieldsPerRecord = -1
	// The record slice is reused between rows; nothing here keeps one past the
	// row it belongs to, and the sample is copied before it is held (§8.4).
	cr.ReuseRecord = true
	head, headVs, err := readHeader(source, cr)
	if err != nil {
		return Result{}, sourceErr(source, err)
	}
	prov := alchemy.Provenance{Source: source, Chunk: -1, Producer: alchemy.ProducerTabular}
	res := Result{Violations: headVs}
	rd := &reader{cr: cr}

	m := opts.Mapping
	if m == nil {
		if opts.LLM == nil {
			return Result{}, sourceErr(source, fmt.Errorf("no mapping supplied and no model to infer one"))
		}
		samples, err := sample(rd, sampleRows)
		if err != nil {
			return Result{}, sourceErr(source, err)
		}
		rd.pending = samples
		inferred, p, call, err := inferMapping(ctx, source, named(head), samples, opts)
		// The call was made whether or not it answered, so it is recorded before
		// anything can return. DESIGN.md §7.2: cost is not optimised for, but it
		// is never hidden — and a failure that reports no call makes a job that
		// retried three times look like a job that ran once. Every return below
		// carries the result for that reason, error or not.
		res.ModelCalls = append(res.ModelCalls, call)
		if err != nil {
			return res, err
		}
		m = inferred
		prov.Model = call.Model
		prov.Confidence = p.Confidence
		if err := validate(m, head); err != nil {
			return res, fmt.Errorf("tabular: %s: the model's mapping cannot be applied to this table: %w", source, err)
		}
		res.Guesses = guessesFor(source, named(head), m, p, prov)
	} else if err := validate(m, head); err != nil {
		return Result{}, fmt.Errorf("tabular: %s: the supplied mapping cannot be applied to this table: %w", source, err)
	}

	if err := rows(source, rd, head, m, prov, &res); err != nil {
		return res, sourceErr(source, err)
	}
	return res, nil
}

// sourceErr names the file. A pipeline reads many sources, and an error that
// says only "line 41" leaves the operator to work out which of them it meant.
func sourceErr(source string, err error) error {
	return fmt.Errorf("tabular: %s: %w", source, err)
}

// sample reads the first rows so the model can see values under the header. It
// is bounded: the point of §8.4 is that no part of the source is held whole.
func sample(rd *reader, n int) ([]record, error) {
	var out []record
	for len(out) < n {
		rec, err := rd.next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, record{fields: append([]string(nil), rec.fields...), line: rec.line})
	}
	return out, nil
}
