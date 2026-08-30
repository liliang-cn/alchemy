package graphimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// AmbiguityError refuses a document that spells one slot two ways and gives
// the two spellings different values — a node with both "id" and "name", an
// edge with both "from" and "source".
//
// The alternative was to record an alchemy.Guess and carry on. That is right
// where one candidate is defensibly better than another, and it is exactly
// wrong here: "from" and "source" are two canonical spellings of the same
// slot, so the only reason this package could give for preferring one is the
// order of its own alias list. A Guess whose Reason is "we looked at this
// spelling first" is not a reason a reviewer can check, and the failure it
// would hide is the one DESIGN.md §2.1 describes: the run is clean, the graph
// is well-formed, and every edge points somewhere plausible but wrong. An
// edge read backwards is not locally excludable the way a violation is, so
// refusing the document is the only outcome that keeps a person in the loop.
//
// Two spellings that agree are not ambiguous: the document stated one thing
// twice, and there is no question to ask.
type AmbiguityError struct {
	// Location is "node 3" or "edge 12", by position in the document, because
	// an object this confused often has no usable id to name it by.
	Location string
	// Slot is the logical field: "id", "name", "type", "from", "to".
	Slot string
	// Spellings are the member names that were present, in the order this
	// package accepts them; Values are what each one said, index for index.
	Spellings []string
	Values    []string
}

func (e *AmbiguityError) Error() string {
	parts := make([]string, 0, len(e.Spellings))
	for i, s := range e.Spellings {
		parts = append(parts, fmt.Sprintf("%q says %q", s, e.Values[i]))
	}
	return fmt.Sprintf("%s: ambiguous %s — %s; the document states it more than once with different values "+
		"and nothing in it says which is meant, so it is refused rather than guessed",
		e.Location, e.Slot, strings.Join(parts, " and "))
}

// DuplicateNodeError refuses a document that gives one id to two different
// nodes.
//
// The alternative was last-wins with a report. It was rejected because the
// damage is not local. A dangling edge is one edge: it is reported, kept, and
// the rest of the graph is usable without it (§7.3). A repeated id is every
// edge that names it — each one silently attaches to whichever copy the
// document happened to list last, so "the rest of the graph is usable" is
// false, and the resulting graph depends on JSON member order. That is the
// §2.1 failure exactly: it runs clean, it looks right, and it is found by a
// person three months later.
//
// A node repeated with identical content is not this error. It states one
// thing twice, which is redundant rather than contradictory.
type DuplicateNodeError struct {
	ID string
	// First and Second are positions in the document's node list, because the
	// id is by definition not enough to tell the two apart.
	First, Second int
}

func (e *DuplicateNodeError) Error() string {
	return fmt.Sprintf("nodes %d and %d both claim the id %q but state different things; "+
		"every edge naming it would attach to whichever came last, so the document is refused",
		e.First, e.Second, e.ID)
}

// MalformedError says where a document stopped being JSON.
//
// encoding/json already carries the byte offset and then prints a message
// without it. A knowledge graph is machine written and routinely megabytes
// long, so "invalid character 'o'" with no position is a bug report nobody
// can act on; the line and column are computed here so a person can open the
// file at the place it broke.
type MalformedError struct {
	Source string
	Offset int64
	Line   int
	Column int
	Err    error
}

func (e *MalformedError) Error() string {
	return fmt.Sprintf("%s: malformed JSON at offset %d (line %d, column %d): %v",
		e.Source, e.Offset, e.Line, e.Column, e.Err)
}

func (e *MalformedError) Unwrap() error { return e.Err }

// malformed locates a decoding error in the bytes it came from. Offsets past
// the end are clamped: a truncated document reports the position of its last
// byte, which is where a person has to look.
func malformed(source string, b []byte, err error) *MalformedError {
	var offset int64
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		offset = syn.Offset
	case errors.As(err, &typ):
		offset = typ.Offset
	default:
		offset = int64(len(b))
	}
	if offset > int64(len(b)) {
		offset = int64(len(b))
	}
	if offset < 0 {
		offset = 0
	}
	line, column := 1, 1
	for _, c := range b[:offset] {
		if c == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}
	return &MalformedError{Source: source, Offset: offset, Line: line, Column: column, Err: err}
}

// DirectionError refuses an edge whose "direction" member says something this
// package cannot read.
//
// The field is a real one: Understand-Anything writes it on every edge, and it
// says which way the record runs relative to the endpoints it wrote down. Only
// "forward" has ever been observed — all 21854 edges of the graph in
// pkg/verify/testdata say it — and "forward" states what this package already
// assumed, that the edge runs source -> target.
//
// Any other value is refused rather than ignored, and the alternative is what
// this package used to do: leave it among the attributes and import the edge as
// written. "backward" would most likely mean the edge runs the other way;
// "both" or "undirected" would mean it runs each way; every reading produces a
// different graph and nothing in the document says which was meant. An edge
// read backwards is not locally excludable the way a dangling one is — it
// points somewhere plausible and wrong, which is §2.1's bug with a three-month
// fuse — so this is AmbiguityError's rule applied to a slot spelled once and
// meant unknowably: refuse the document, keep a person in the loop.
//
// Guessing a meaning for a value no tool is known to emit would be worse than
// refusing it: this package accepts only spellings real documents contain,
// because a tolerated spelling nobody writes can only ever misread a document
// that meant something else by it.
type DirectionError struct {
	// Location is "edge 12", by position, for the same reason AmbiguityError
	// names a position: an edge has no id of its own in most documents.
	Location string
	// Value is what the member said, verbatim.
	Value string
}

func (e *DirectionError) Error() string {
	return fmt.Sprintf("%s: direction %q, which this package cannot read — it could mean the edge runs "+
		"the other way, or that it runs both, and those are different graphs. Only %q is understood, "+
		"and it states what the endpoints already say; the document is refused rather than read under a "+
		"coin flip", e.Location, e.Value, directionForward)
}
