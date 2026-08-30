// Package preflight is the set of refusals a writer makes before it writes.
//
// It exists because four stores were built against alchemy.Result by four
// people who could not see each other's work, and all four wrote the same
// checks: is the job held, do two entities share an ID, does every vector name
// a chunk that exists, is there one vector width. Two of them wrote
// byte-identical dimension checks down to the wording of the error. Where they
// differed, they differed by omission rather than by opinion — one store
// refused two chunks under one index and the others were exposed to the same
// silent overwrite without knowing it, and none of them refused two vectors for
// one chunk, which is the same defect one layer along.
//
// That is the shape of a missing package rather than of four bugs. Every one of
// these questions is answerable from alchemy.Result alone, none of them needs a
// store, and every one of them is a thing DESIGN.md already promises: §7.3 that
// a held graph does not reach anybody, §5b that a citation resolves, §5 that
// the counts are numbers a reader can distrust the graph with, §5c that a
// record's policy travels with it. A promise nobody can evaluate is a promise
// kept by whoever remembers to keep it.
//
// It is deliberately not a Sink interface. §4 defers that until a second
// consumer's needs define its shape, and that argument is untouched: nothing
// here writes anything, opens anything, or knows a store exists. It is the
// reading of a result that every writer was already doing.
//
// Two severities, and the line between them is what a defect costs. A refusal
// is a defect that would silently lose or corrupt data on the way into a store:
// two chunks under one index become one row and the load reports two. A report
// is a defect that leaves the graph writable and something in it untrue — a
// count that does not add up, a citation whose chunk is missing, a rule name
// that resolves to nothing. The first is a writer's business and the second is
// a reader's, and collapsing them would either block imports over a wrong
// number or wave through a silent overwrite.
//
// Check is over a whole result. §8.4 pages a large one, and a page is not a
// result: it holds a window of each slice, so a vector on page 3 legitimately
// names a chunk that arrived on page 1. A consumer streaming a graph
// reassembles it and checks that, which is also the only point at which the
// counts on page 0 can be compared with anything.
package preflight

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Kind names one thing that can be wrong with a result.
//
// The set is closed and the strings are stable, because they end up in an error
// a person reads and in whatever a caller decides to log: a kind renamed
// quietly is a filter that stops matching without saying so. They are the same
// discipline alchemy.ViolationKind is under and for the same reason.
type Kind string

const (
	// Held — a conflict nobody has answered. §7.3: a job that finds one does
	// not finish, whether or not the caller asked for review, and a store that
	// wrote the graph anyway would hold the contradiction this design exists to
	// keep out of an agent's reach.
	Held Kind = "held"
	// EntityIDReused — two entities under one ID. Relations refer to entities
	// by ID, so a store writing one node for both leaves every edge naming it
	// pointing at whichever record was written last. Two of the four stores
	// refused this; the other two upsert and would have overwritten.
	EntityIDReused Kind = "entity_id_reused"
	// EntityCorroborated — two records under one ID that agree about what the
	// node is. A store holding one row per entity writes one node and keeps
	// one of the two provenances.
	//
	// It is a report and not a refusal, and the line is this package's own:
	// the graph is writable and something in it is lost, rather than corrupt.
	// alchemy.Relation.Identity settled the same question for edges years
	// earlier -- "two records asserting one edge are one edge here" -- and
	// entities had no equivalent, so two sources agreeing that Ravel is a
	// Product called Ravel was refused as an ID collision.
	//
	// That was not a corner case. It made every multi-source graph unloadable
	// into every store, which is the one thing this product exists to produce:
	// four sources describing one company refused by all four connectors with
	// eighteen defects, every one of them two documents agreeing.
	EntityCorroborated Kind = "entity_corroborated"
	// ChunkIndexReused — two chunks claiming one index. A chunk index is what
	// Provenance.Chunk and Vector.Chunk name, and alchemy.Chunk carries an
	// Index next to a Source, which reads as "index within this source" and is
	// not: only pkg/pipeline's adopt makes the number job-wide. A result
	// assembled another way can hand two files' first chunks the same number,
	// and then two chunks derive one record, the second overwrites the first,
	// and the load reports two chunks where the store holds one.
	ChunkIndexReused Kind = "chunk_index_reused"
	// ChunkVectoredTwice — two vectors naming one chunk. Every store that
	// joined them built a map keyed on the index, so the second silently wins;
	// none of the four noticed, which is what an unstated invariant does.
	ChunkVectoredTwice Kind = "chunk_vectored_twice"
	// VectorWithoutChunk — a vector naming a chunk the result does not carry.
	// Storing it leaves an embedding with no text behind it: searchable,
	// citable, and pointing at nothing.
	VectorWithoutChunk Kind = "vector_without_chunk"
	// VectorEmpty — a vector with no dimensions. It is well-formed enough to be
	// stored and searched against, and then matches everything or nothing
	// depending on the index — wrong in the way that never raises an error.
	VectorEmpty Kind = "vector_empty"
	// VectorWidth — two widths in one result. An index takes vectors of a
	// single dimension, and there is nothing in the data to say which width was
	// the one meant.
	VectorWidth Kind = "vector_width"
	// ProvenanceWithoutChunk — a record citing a chunk this result does not
	// carry, in a result that does carry chunks. §5b's promise is that an agent
	// can name "the chunk of which file", and here half of that citation
	// resolves to nothing. It is a report and not a refusal: the record is
	// still writable and still attributable to its source.
	ProvenanceWithoutChunk Kind = "provenance_without_chunk"
	// CountsDisagree — Counts claims a number the slices beside it contradict.
	// §5 makes this block the obligation that justifies the release, and until
	// something could check it, it was a claim every store copied verbatim
	// because there was nothing else to do with it.
	CountsDisagree Kind = "counts_disagree"
	// RuleSetUnresolved — a Provenance.RuleSet name that Result.RuleSets does
	// not define. §5c: the name is how a record points at the policy it was
	// proposed under, and a result that carries the names and leaves the sets
	// behind gives every record a pointer into nothing.
	RuleSetUnresolved Kind = "rule_set_unresolved"
	// RuleUnresolved — a Provenance.RuledBy name no rule in any of the result's
	// sets carries. RuleSet says which rules were in the room; RuledBy says
	// which one moved, and a reader who cannot resolve the second has a graph
	// that came back retyped by nobody in particular.
	RuleUnresolved Kind = "rule_unresolved"
	// AttributeType — an attribute value outside the JSON value domain
	// Entity.Attributes declares. It is the one defect that is invisible inside
	// the process that made it: the value round-trips in Go and changes type on
	// the way to any consumer, so each store meets a different graph.
	AttributeType Kind = "attribute_type"
	// AssertionUnsigned — a record whose producer is alchemy.ProducerHuman and
	// whose provenance names nobody.
	//
	// It is a report and not a refusal, deliberately, and the line in this
	// package's doc comment is why: the graph is writable and something in it
	// is untrue, which is a reader's problem rather than a writer's. The edge
	// is real and a store that held it would hold a correct edge; what is
	// missing is the only thing that made a human assertion admissible in the
	// first place. §5b sells the ability to ask "who says so", and an
	// unattributed assertion is the one record in a graph that answers "a
	// person" and cannot say which — worse than an inferred edge, which at
	// least admits that nobody can be asked.
	AssertionUnsigned Kind = "assertion_unsigned"
)

// Severity is what a defect costs, and it is the whole of the difference
// between the two lists this package produces.
type Severity string

const (
	// SeverityRefuse — writing this result would lose or corrupt something, and
	// the loss would be silent. Nothing should be written.
	SeverityRefuse Severity = "refuse"
	// SeverityReport — the result is writable and something in it is not true
	// about itself. §5's whole argument is that a graph you can distrust
	// precisely is worth more than one you cannot, so these are surfaced rather
	// than fixed and never rewritten: a checker that corrected a count would be
	// hiding the stage that got it wrong.
	SeverityReport Severity = "report"
)

// Defect is one finding about a result.
//
// It is a struct with a kind and a subject rather than an error string for the
// reason alchemy.Violation is: a caller that has to parse prose to decide what
// to do is a caller whose parser drifts away from the writer in silence.
type Defect struct {
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity"`
	// Subject is what the defect is about — an entity ID, a chunk index, an
	// attribute key — in the same spirit as alchemy.Violation.Subject: enough
	// to group two findings about one thing.
	Subject string `json:"subject"`
	// Detail says what is wrong in words a person can act on, and names both
	// sides where there are two. "Chunk 1 is ambiguous" sends a reader nowhere;
	// "a.md and b.md both call something chunk 1" sends them to the join.
	Detail string `json:"detail"`
}

// Check reports everything wrong with a result, in a fixed order.
//
// The order is by check rather than by severity, so that two runs over one
// result produce one list and a caller can diff them. Nothing here reads a map
// in iteration order.
func Check(res alchemy.Result) []Defect {
	var out []Defect
	out = append(out, held(res)...)
	out = append(out, entities(res)...)
	chunks, chunkDefects := chunkIndex(res)
	out = append(out, chunkDefects...)
	out = append(out, vectors(res, chunks)...)
	out = append(out, citations(res, chunks)...)
	out = append(out, counts(res)...)
	out = append(out, ruleNames(res)...)
	out = append(out, attributes(res)...)
	out = append(out, assertions(res)...)
	return out
}

// Refuse is Check filtered to the defects that would lose data, rendered as an
// error a writer can return.
//
// It is a filter over Check and not a second implementation: a store asking
// "may I write this" and a report asking "what is wrong with this" must never
// disagree about whether a graph is writable, and two passes over one rule set
// is how they come to.
//
// Every blocking defect is named, not the first. A writer that fixes one and
// re-runs to find the next is running an import to obtain an error message, and
// on the corpus sizes §8 is about that is an afternoon.
func Refuse(res alchemy.Result) error {
	var blocking []Defect
	for _, d := range Check(res) {
		if d.Severity == SeverityRefuse {
			blocking = append(blocking, d)
		}
	}
	if len(blocking) == 0 {
		return nil
	}
	lines := make([]string, 0, len(blocking))
	for _, d := range blocking {
		lines = append(lines, fmt.Sprintf("%s: %s", d.Kind, d.Detail))
	}
	return fmt.Errorf("preflight: %d defect(s) would be written as data loss, so nothing should be written: %s",
		len(blocking), strings.Join(lines, "; "))
}

// held is §7.3 asked of a result. The test is alchemy.Result.Held and not a
// copy of it: two definitions of when a job is finished is how the one
// guarantee this design refuses to make optional becomes optional.
func held(res alchemy.Result) []Defect {
	open := res.Held()
	if len(open) == 0 {
		return nil
	}
	return []Defect{{
		Kind: Held, Severity: SeverityRefuse, Subject: open[0].Subject,
		Detail: fmt.Sprintf("%d of %d conflict(s) are unanswered, the first about %q (%s); a graph that contradicts itself is not a finished graph and §7.3 holds the job until a person decides",
			len(open), len(res.Conflicts), open[0].Subject, open[0].Kind),
	}}
}
