package preflight

import (
	"fmt"
	"strconv"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// entities finds two records claiming one identity.
//
// Only the first collision per ID is reported. A corpus that somehow holds a
// thousand records under one ID asks one question about it, not five hundred
// thousand — the same rule verify's conflict slots are under, and for the same
// reason: a list that repeats one fact is a list nobody reads to the end.
//
// The comparison is the ID alone and not the whole record. Two entities with
// one ID that agree about everything are still two rows a store has to merge
// into one, and it is verify's ConflictEntityType that asks whether they agree;
// what this asks is whether the store will be able to tell them apart, and it
// will not.
func entities(res alchemy.Result) []Defect {
	seen := make(map[string]string, len(res.Entities))
	said := make(map[string]bool)
	var out []Defect
	for _, e := range res.Entities {
		if e.ID == "" {
			// An entity nothing can refer to. It is separate from the reuse
			// case because the fix is different: an empty ID is a producer that
			// did not derive one, and a store writing it makes every edge
			// naming "" point at all of them at once.
			if !said[""] {
				said[""] = true
				out = append(out, Defect{
					Kind: EntityIDReused, Severity: SeverityRefuse, Subject: "",
					Detail: fmt.Sprintf("the entity %q of type %q has no ID, so no relation can name it and a store has nothing to key it on", e.Name, e.Type),
				})
			}
			continue
		}
		prev, dup := seen[e.ID]
		if !dup {
			seen[e.ID] = describe(e)
			continue
		}
		if said[e.ID] {
			continue
		}
		said[e.ID] = true
		out = append(out, Defect{
			Kind: EntityIDReused, Severity: SeverityRefuse, Subject: e.ID,
			Detail: fmt.Sprintf("the ID %q is claimed by %s and by %s; relations name entities by ID, so a store writing one node for both leaves every edge naming it pointing at whichever was written last",
				e.ID, prev, describe(e)),
		})
	}
	return out
}

func describe(e alchemy.Entity) string {
	return fmt.Sprintf("%s %q per %s", e.Type, e.Name, where(e.Provenance))
}

// where names a record's origin the way a person reading a finding thinks
// about it: the file, and the chunk when there was one. It is the same
// rendering verify.where produces, written again rather than exported from
// there, because this package must not depend on a stage — it is read by
// writers that have no reason to import the verifier.
func where(p alchemy.Provenance) string {
	switch {
	case p.Source == "":
		return fmt.Sprintf("an unnamed %s source", p.Producer)
	case p.Chunk < 0:
		return fmt.Sprintf("%s (%s)", p.Source, p.Producer)
	default:
		return fmt.Sprintf("%s chunk %d (%s)", p.Source, p.Chunk, p.Producer)
	}
}

// chunkIndex checks the invariant alchemy.Chunk.Index states and returns the
// index it built, so nothing walks the chunks twice.
//
// The returned map is nil when the result carries no chunks, and the
// difference matters downstream: "this result has no chunks" and "this result
// has chunks and not that one" are different facts, and only the second is a
// dangling citation. A DDL job has no chunks at all and must not be accused of
// losing them.
func chunkIndex(res alchemy.Result) (map[int]bool, []Defect) {
	if len(res.Chunks) == 0 {
		return nil, nil
	}
	seen := make(map[int]string, len(res.Chunks))
	have := make(map[int]bool, len(res.Chunks))
	said := make(map[int]bool)
	var out []Defect
	for _, c := range res.Chunks {
		prev, dup := seen[c.Index]
		if !dup {
			seen[c.Index], have[c.Index] = c.Source, true
			continue
		}
		if said[c.Index] {
			continue
		}
		said[c.Index] = true
		out = append(out, Defect{
			Kind: ChunkIndexReused, Severity: SeverityRefuse, Subject: strconv.Itoa(c.Index),
			Detail: fmt.Sprintf("chunk %d arrives twice, from %q and from %q; a chunk index is what a vector and a provenance point at, so it names one chunk across the whole result or two chunks are stored as one and the other is lost silently",
				c.Index, prev, c.Source),
		})
	}
	return have, out
}

// vectors checks the three things every store that holds an embedding had to
// check for itself, and the fourth none of them did.
//
// One width, no empty vectors, every vector naming a chunk that exists: two
// stores wrote all three, in the same order, with near-identical messages, and
// neither could see the other's file. The fourth is two vectors naming one
// chunk, which every one of them turned into a map keyed on the index and
// therefore resolved by silently keeping the last.
//
// The width is the first real vector's, and the chunk it belonged to is
// remembered so a later disagreement can name both sides — the same shape
// pkg/embed's own check has, because a caller told only "widths differ" has to
// go and find them.
func vectors(res alchemy.Result, chunks map[int]bool) []Defect {
	var out []Defect
	width, widthOf := -1, -1
	seen := make(map[int]bool, len(res.Vectors))
	said := make(map[int]bool)
	for _, v := range res.Vectors {
		if seen[v.Chunk] && !said[v.Chunk] {
			said[v.Chunk] = true
			out = append(out, Defect{
				Kind: ChunkVectoredTwice, Severity: SeverityRefuse, Subject: strconv.Itoa(v.Chunk),
				Detail: fmt.Sprintf("chunk %d is embedded twice; a store joins a vector to its text by the chunk index, so the second is written over the first and nothing says which one the search is answering from", v.Chunk),
			})
		}
		seen[v.Chunk] = true

		if len(v.Values) == 0 {
			out = append(out, Defect{
				Kind: VectorEmpty, Severity: SeverityRefuse, Subject: strconv.Itoa(v.Chunk),
				Detail: fmt.Sprintf("the vector for chunk %d has no dimensions; it is well-formed enough to be stored and searched against, and then matches everything or nothing depending on the index", v.Chunk),
			})
			continue
		}
		// Checked only against a result that carries chunks, for the reason
		// chunkIndex returns nil rather than an empty map: a page of a large
		// result (§8.4) legitimately holds vectors whose chunks arrived
		// elsewhere, and refusing that would refuse §8.4.
		if chunks != nil && !chunks[v.Chunk] {
			out = append(out, Defect{
				Kind: VectorWithoutChunk, Severity: SeverityRefuse, Subject: strconv.Itoa(v.Chunk),
				Detail: fmt.Sprintf("a vector names chunk %d and this result has no such chunk; storing it leaves an embedding with no text behind it — searchable, citable, and pointing at nothing", v.Chunk),
			})
		}
		if width < 0 {
			width, widthOf = len(v.Values), v.Chunk
			continue
		}
		if len(v.Values) != width {
			out = append(out, Defect{
				Kind: VectorWidth, Severity: SeverityRefuse, Subject: strconv.Itoa(v.Chunk),
				Detail: fmt.Sprintf("chunk %d has %d dimensions and chunk %d has %d; one run's vectors share a width or no index will hold them, and nothing in the data says which width was meant",
					widthOf, width, v.Chunk, len(v.Values)),
			})
		}
	}
	return out
}

// citations checks that a record's chunk resolves, which is the half of §5b's
// promise a store cannot keep on its own.
//
// "Every entity and relation can name the source, the chunk and the producer it
// came from" is the promise; a Provenance.Chunk that no chunk carries keeps the
// first and third and quietly drops the second. One graph store loads its
// records with the chunk number on them and its chunk nodes separately, so a
// result with a hole in its chunks produces a graph whose citations resolve to
// nothing — and it names that exact failure in a comment as the reason it loads
// chunks at all, without being able to detect it.
//
// A report rather than a refusal, and a strict reading of §7.3's own line
// decides which: the record is attributable to its source and excludable, and
// the rest of the graph is usable without it. What is owed is that the reader
// knows.
//
// -1 is not a dangling citation. pkg/alchemy defines it as "the producer did
// not work in chunks", which is a fact about the record rather than a missing
// value: a CREATE TABLE has no chunk and never had one.
func citations(res alchemy.Result, chunks map[int]bool) []Defect {
	if chunks == nil {
		return nil
	}
	var out []Defect
	said := make(map[int]bool)
	report := func(subject string, p alchemy.Provenance) {
		if p.Chunk < 0 || chunks[p.Chunk] || said[p.Chunk] {
			return
		}
		said[p.Chunk] = true
		out = append(out, Defect{
			Kind: ProvenanceWithoutChunk, Severity: SeverityReport, Subject: subject,
			Detail: fmt.Sprintf("%q cites chunk %d of %q and this result carries chunks but not that one; the source half of the citation survives and the chunk half resolves to nothing",
				subject, p.Chunk, p.Source),
		})
	}
	for _, e := range res.Entities {
		report(e.ID, e.Provenance)
	}
	for _, r := range res.Relations {
		report(fmt.Sprintf("%s -[%s]-> %s", r.From, r.Type, r.To), r.Provenance)
	}
	return out
}
