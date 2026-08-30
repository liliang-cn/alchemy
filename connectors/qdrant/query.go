package qdrant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Records is a slice of the store read back as the types alchemy returned.
//
// It is typed slices rather than one list of a union type because the records
// are of different kinds and a caller almost always wants one of them; a
// []Record with a Kind field would make every call site a type switch to get
// at what it asked for.
//
// Everything in it comes from one read, but not necessarily from one load.
// That is deliberate and is why Filter.Loads exists: a collection can hold a
// corpus imported twice, and this connector will not silently pick a side.
type Records struct {
	Entities   []alchemy.Entity
	Relations  []alchemy.Relation
	Chunks     []alchemy.Chunk
	Violations []alchemy.Violation
	Duplicates []alchemy.Duplicate
	// Loads is which loads these records came from, so that a caller who did
	// not restrict the read can tell whether they just mixed two imports.
	Loads []string
}

// Records is the query §5b's guarantee turns into: every record matching a
// provenance filter, in the types the pipeline returned them in.
//
// This is the one of the three queries that has nothing to do with vectors,
// and it is the reason a payload store is a reasonable home for a graph at
// all. "Every edge from architecture.pdf that a model proposed and nobody has
// reviewed" is a filter over indexed keyword fields here; in the vector store
// this connector could have been — chunks only, everything else dropped — it
// would not be a question at all.
//
// limit of 0 means everything that matches.
func (l *Loader) Records(ctx context.Context, f Filter, limit int) (Records, error) {
	flt, err := l.resolve(ctx, f)
	if err != nil {
		return Records{}, err
	}
	pts, err := l.scroll(ctx, flt, limit)
	if err != nil {
		return Records{}, err
	}
	var out Records
	loads := map[string]bool{}
	for _, p := range pts {
		loads[str(p.Payload[keyLoad])] = true
		switch kind(str(p.Payload[keyKind])) {
		case kindEntity:
			out.Entities = append(out.Entities, readEntity(p.Payload))
		case kindRelation:
			out.Relations = append(out.Relations, readRelation(p.Payload))
		case kindChunk:
			out.Chunks = append(out.Chunks, readChunk(p.Payload))
		case kindViolation:
			out.Violations = append(out.Violations, readViolation(p.Payload))
		case kindDuplicate:
			out.Duplicates = append(out.Duplicates, readDuplicate(p.Payload))
		}
	}
	for id := range loads {
		out.Loads = append(out.Loads, id)
	}
	sort.Strings(out.Loads)
	// Qdrant returns a scroll in point-ID order, which is a hash and therefore
	// arbitrary. Sorting here costs nothing at these sizes and means a reader
	// diffing two runs sees the difference rather than the shuffling.
	sort.Slice(out.Entities, func(i, j int) bool { return out.Entities[i].ID < out.Entities[j].ID })
	sort.Slice(out.Relations, func(i, j int) bool { return relLess(out.Relations[i], out.Relations[j]) })
	sort.Slice(out.Chunks, func(i, j int) bool { return out.Chunks[i].Index < out.Chunks[j].Index })
	sort.Slice(out.Violations, func(i, j int) bool { return out.Violations[i].Subject < out.Violations[j].Subject })
	sort.Slice(out.Duplicates, func(i, j int) bool { return out.Duplicates[i].Subject < out.Duplicates[j].Subject })
	return out, nil
}

func relLess(a, b alchemy.Relation) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.To != b.To {
		return a.To < b.To
	}
	if a.Type != b.Type {
		return a.Type < b.Type
	}
	return a.Key < b.Key
}

// Findings is what a load carries beside its records: the numbers §5 obliges a
// graph to travel with, and the findings that are read whole rather than
// filtered on.
//
// They are read from the load marker in one request rather than joined,
// because that is how they were written: a conflict or a guess is about the
// import, not about a record a filter could find.
type Findings struct {
	Counts     alchemy.Counts
	Conflicts  []alchemy.Conflict
	Guesses    []alchemy.Guess
	Unread     []alchemy.Unread
	RuleSets   []alchemy.RuleSet
	ModelCalls []alchemy.ModelCall
	// Lost is what this store could not keep about the graph, as it was
	// recorded at load time. A buyer who finds the collection long after the
	// Load call returned is told the same thing the caller was.
	Lost []string
}

// Findings reads one load's findings block.
func (l *Loader) Findings(ctx context.Context, loadID string) (Findings, error) {
	flt := map[string]any{"must": []map[string]any{
		match(keyKind, string(kindLoad)), match(keyLoad, loadID),
	}}
	pts, err := l.scroll(ctx, flt, 1)
	if err != nil {
		return Findings{}, err
	}
	if len(pts) == 0 {
		return Findings{}, fmt.Errorf("qdrant: collection %q holds no load called %q", l.collection, loadID)
	}
	p := pts[0].Payload
	var out Findings
	// Decoded by re-marshalling rather than field by field, so that a field
	// added to alchemy.Conflict or alchemy.Counts arrives here without this
	// function being edited — and, more to the point, so that it cannot be
	// half-added and quietly dropped on the way out.
	for dest, raw := range map[any]any{
		&out.Counts:     p[keyCounts],
		&out.Conflicts:  p[keyConflicts],
		&out.Guesses:    p[keyGuesses],
		&out.Unread:     p[keyUnread],
		&out.RuleSets:   p[keyRuleSets],
		&out.ModelCalls: p[keyModelCalls],
	} {
		if raw == nil {
			continue
		}
		body, err := json.Marshal(raw)
		if err != nil {
			return Findings{}, fmt.Errorf("qdrant: reading load %s: %w", loadID, err)
		}
		if err := json.Unmarshal(body, dest); err != nil {
			return Findings{}, fmt.Errorf("qdrant: reading load %s: %w", loadID, err)
		}
	}
	for _, s := range asSlice(p[keyLost]) {
		out.Lost = append(out.Lost, str(s))
	}
	return out, nil
}

func readEntity(p map[string]any) alchemy.Entity {
	return alchemy.Entity{
		ID:         str(p[keyEntityID]),
		Type:       str(p[keyType]),
		Name:       str(p[keyName]),
		Attributes: attrs(p[keyAttributes]),
		Provenance: readProvenance(p),
	}
}

func readRelation(p map[string]any) alchemy.Relation {
	return alchemy.Relation{
		From:       str(p[keyRelFrom]),
		To:         str(p[keyRelTo]),
		Type:       str(p[keyType]),
		Key:        str(p[keyRelKey]),
		Attributes: attrs(p[keyAttributes]),
		Provenance: readProvenance(p),
	}
}

func readChunk(p map[string]any) alchemy.Chunk {
	return alchemy.Chunk{
		Index:    num(p[keyChunkIndex]),
		Text:     str(p[keyText]),
		Source:   str(p[keySource]),
		Strategy: str(p[keyStrategy]),
		Heading:  str(p[keyHeading]),
		Start:    num(p[keyStart]),
		End:      num(p[keyEnd]),
	}
}

func readViolation(p map[string]any) alchemy.Violation {
	return alchemy.Violation{
		Kind:       alchemy.ViolationKind(str(p[keyViolationKind])),
		Detail:     str(p[keyDetail]),
		Subject:    str(p[keySubject]),
		Provenance: readProvenance(p),
	}
}

func readDuplicate(p map[string]any) alchemy.Duplicate {
	d := alchemy.Duplicate{
		Signal:  alchemy.DuplicateSignal(str(p[keySignal])),
		Subject: str(p[keySubject]),
		Detail:  str(p[keyDetail]),
	}
	d.Left, d.Right = readSide(p[keyLeft]), readSide(p[keyRight])
	return d
}

func readSide(v any) alchemy.DuplicateSide {
	m, _ := v.(map[string]any)
	if m == nil {
		return alchemy.DuplicateSide{}
	}
	prov, _ := m["provenance"].(map[string]any)
	return alchemy.DuplicateSide{
		ID: str(m[keyEntityID]), Type: str(m[keyType]), Name: str(m[keyName]),
		Provenance: readProvenance(prov),
	}
}

// attrs restores the source's own words. A nil map and an empty one are kept
// apart: "the source said nothing about this" and "the source said it had no
// attributes" are different claims, and the JSON contract can express both.
func attrs(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}
