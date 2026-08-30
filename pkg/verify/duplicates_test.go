package verify_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// prose is the run this file was written from: one markdown document, four
// sections, four entity types, one model call per chunk. Every entity below
// carries the chunk it came out of, because that is the whole mechanism — a
// chunk is a separate model call that cannot see what the other calls were
// told, so the system prompt's "use that same spelling every time" is advice
// the model cannot reliably follow.
func prose(chunk int) alchemy.Provenance {
	return alchemy.Provenance{
		Source: "design.md", Chunk: chunk,
		Producer: alchemy.ProducerLLMExtract, Model: "gemini-3.7-flash-high",
		Confidence: 0.8,
	}
}

func entity(typ, name string, chunk int) alchemy.Entity {
	return alchemy.Entity{ID: id(typ, name), Type: typ, Name: name, Provenance: prose(chunk)}
}

// id is the identity extract gives an entity: the folded type and the folded
// name. It is written out here rather than imported because that is the point
// of the test — two spellings of one thing land on two ids, and nothing
// downstream joins them.
func id(typ, name string) string {
	return fold(typ) + ":" + fold(name)
}

func fold(s string) string {
	out := ""
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		out += string(r)
	}
	return out
}

// proseVocab is the four-type prose ontology the run was governed by. The
// types matter to the detector — a candidate is two nodes of one type — and
// nothing else here does.
func proseVocab() verify.Input {
	v := ontology.Vocabulary{}
	for _, n := range []string{"Package", "Format", "Model", "Concept"} {
		v.Entities = append(v.Entities, ontology.EntityType{Name: n})
	}
	return verify.Input{OntologyID: "prose@1", Vocabulary: v}
}

// The case that started this. Two chunks, one package, two names: the second
// chunk wrote the type word into the name. §5b promises a graph that can
// explain itself, so the pair is reported and the two nodes are left exactly
// as the model proposed them — a node that silently absorbed another explains
// nothing.
func TestTwoSpellingsOfOnePackageAreOneCandidateAndStillTwoNodes(t *testing.T) {
	in := proseVocab()
	in.Entities = []alchemy.Entity{
		entity("Package", "document", 1),
		entity("Package", "document package", 2),
	}

	got := verify.Check(in)

	if len(got.Duplicates) != 1 {
		t.Fatalf("duplicates = %d, want the one pair: %+v", len(got.Duplicates), got.Duplicates)
	}
	d := got.Duplicates[0]
	if d.Left.ID != "package:document" || d.Right.ID != "package:document package" {
		t.Fatalf("candidate pairs %q with %q, want the shorter name on the left", d.Left.ID, d.Right.ID)
	}
	if d.Left.Provenance.Chunk != 1 || d.Right.Provenance.Chunk != 2 {
		t.Fatalf("candidate cites chunks %d and %d, want the two chunks that proposed them",
			d.Left.Provenance.Chunk, d.Right.Provenance.Chunk)
	}
	// Reported, never resolved.
	if len(got.Entities) != 2 {
		t.Fatalf("entities = %d, want both nodes left standing: %+v", len(got.Entities), got.Entities)
	}
	if got.Counts.Duplicates != 1 {
		t.Fatalf("counts.Duplicates = %d, want 1", got.Counts.Duplicates)
	}
}

// The whole of the run, as it came back: seventeen entities under a four-type
// ontology, of which these are the ones that matter. Three pairs are one thing
// named twice; the rest are not, and the detector's worth is entirely in the
// second half of that sentence — one that reported "extract package" against
// "document package", or the two models against each other, would be a queue
// nobody reads.
func TestTheRealRunReportsExactlyTheThreePairsAndNoOthers(t *testing.T) {
	in := proseVocab()
	in.Entities = []alchemy.Entity{
		entity("Package", "document", 1),
		entity("Package", "document package", 2),
		entity("Package", "ontology", 3),
		entity("Package", "ontology package", 2),
		entity("Package", "extract package", 2),
		entity("Format", "SQL", 1),
		entity("Format", "SQL dumps", 0),
	}

	assertPairs(t, verify.Check(in).Duplicates,
		"package:document ~ package:document package",
		"package:ontology ~ package:ontology package",
		"format:sql ~ format:sql dumps",
	)
}

// The false positives that would make the detector worthless, pinned in the
// same graph as the true ones so that widening the signal fails here rather
// than in somebody's corpus.
//
// "ddl" and "document" share nothing but a type. "extract package" shares its
// last word with two other packages and its first with nothing, which is the
// case the run itself flagged as "named unlike its siblings" — a naming
// problem, not a duplicate. And the two models are the reason edge
// neighbourhood and attribute equality are not signals here: they are siblings
// under one type, and every signal that finds them finds every taxonomy.
func TestSiblingsAndUnrelatedNamesAreNeverCandidates(t *testing.T) {
	in := proseVocab()
	in.Entities = []alchemy.Entity{
		entity("Package", "document", 1),
		entity("Package", "document package", 2),
		entity("Package", "ddl", 0),
		entity("Package", "extract package", 2),
		entity("Model", "language model", 1),
		entity("Model", "embedding model", 3),
		entity("Concept", "chunk", 0),
		entity("Concept", "chunking strategy", 1),
	}

	assertPairs(t, verify.Check(in).Duplicates,
		"package:document ~ package:document package",
	)
}

// A type is part of the identity, so the same word under two types is two
// things. §5b's whole mechanism is that the vocabulary is on both sides of the
// model, and a detector that reached across it would be proposing to merge two
// nodes the ontology says are different kinds.
func TestOneNameUnderTwoTypesIsNotACandidate(t *testing.T) {
	in := proseVocab()
	in.Entities = []alchemy.Entity{
		entity("Package", "document", 1),
		entity("Format", "document format", 2),
		entity("Concept", "document", 3),
	}

	assertPairs(t, verify.Check(in).Duplicates)
}

// assertPairs compares the reported subjects against exactly the wanted ones,
// in order — a detector whose output order moves with a map is one whose
// findings cannot be diffed between two runs (§7.1).
func assertPairs(t *testing.T, got []alchemy.Duplicate, want ...string) {
	t.Helper()
	var subjects []string
	for _, d := range got {
		subjects = append(subjects, d.Subject)
	}
	if len(subjects) != len(want) {
		t.Fatalf("duplicates = %q, want exactly %q", subjects, want)
	}
	for i := range want {
		if subjects[i] != want[i] {
			t.Fatalf("duplicate %d = %q, want %q (all: %q)", i, subjects[i], want[i], subjects)
		}
	}
}

// supplied is an id a source chose rather than one this pipeline minted: a
// graph import brings the document's own ids, which is the case the whole
// second signal turns on.
func supplied(id, typ, name string) alchemy.Entity {
	return alchemy.Entity{ID: id, Type: typ, Name: name,
		Provenance: alchemy.Provenance{Source: "northgate.json", Chunk: -1, Producer: alchemy.ProducerGraphImport}}
}

// The case the original rejection did not cover, measured on the evaluation corpus:
// a graph import brought `org:northgate` and the extractor minted
// `organization:northgate` for the same company under the same type, and the
// duplicate count sat at two with neither of them this. Names are equal, so
// name_affix cannot see it; ids are unequal, so nothing joined it.
func TestOneNameFromTwoProducersWithTwoIdsIsACandidate(t *testing.T) {
	in := proseVocab()
	in.Vocabulary.Entities = append(in.Vocabulary.Entities, ontology.EntityType{Name: "Organization"})
	in.Entities = []alchemy.Entity{
		supplied("org:northgate", "Organization", "Northgate"),
		entity("Organization", "Northgate", 3),
	}

	got := verify.Check(in).Duplicates

	if len(got) != 1 {
		t.Fatalf("duplicates = %+v, want the one pair", got)
	}
	if got[0].Signal != alchemy.DuplicateNameAcrossProducers {
		t.Fatalf("signal = %q, want %q", got[0].Signal, alchemy.DuplicateNameAcrossProducers)
	}
	if got[0].Subject != "org:northgate ~ organization:northgate" {
		t.Fatalf("subject = %q, want the two ids", got[0].Subject)
	}
	if got[0].Left.Provenance.Producer != alchemy.ProducerGraphImport || got[0].Right.Provenance.Producer != alchemy.ProducerLLMExtract {
		t.Fatalf("candidate cites %q and %q, want each side to keep the producer that made it",
			got[0].Left.Provenance.Producer, got[0].Right.Provenance.Producer)
	}
	// Reported, never resolved: two ids for one name is evidence, and the
	// pipeline has no way to know which id the rest of the graph points at.
	if len(verify.Check(in).Entities) != 2 {
		t.Fatalf("entities = %d, want both nodes left standing", len(verify.Check(in).Entities))
	}
}

// The property the narrowed rejection preserves. public.users and audit.users
// are two tables one CREATE TABLE declared, both named "users", and asking a
// person to confirm they are two is §5c's last row — the one it says teaches
// people to click Approve without reading. One producer made both, so nothing
// fires.
func TestTwoTablesOneDDLDeclaredAreNeverACandidate(t *testing.T) {
	ddl := func(id, name string) alchemy.Entity {
		return alchemy.Entity{ID: id, Type: "Table", Name: name,
			Provenance: alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}}
	}
	in := verify.Input{
		OntologyID: "sql@1",
		Vocabulary: ontology.Vocabulary{Entities: []ontology.EntityType{{Name: "Table"}}},
		Entities: []alchemy.Entity{
			ddl("table:public.users", "users"),
			ddl("table:audit.users", "users"),
		},
	}

	assertPairs(t, verify.Check(in).Duplicates)
}

// An entity nobody named is not evidence of anything. Two of them under one
// type have equal folded names in the trivial sense and nothing has been
// stated, so a signal that read that as "one name under two ids" would fire on
// every pair of unnamed nodes a graph import and an extractor happen to share a
// type on. name_affix never had to say this — a nameless entity has no affixes
// — and this one does.
func TestTwoUnnamedNodesFromTwoProducersAreNeverACandidate(t *testing.T) {
	in := verify.Input{
		OntologyID: "prose@1",
		Vocabulary: ontology.Vocabulary{Entities: []ontology.EntityType{{Name: "Package"}}},
		Entities: []alchemy.Entity{
			supplied("pkg:one", "Package", ""),
			{ID: "pkg:two", Type: "Package", Provenance: prose(1)},
		},
	}

	assertPairs(t, verify.Check(in).Duplicates)
}
