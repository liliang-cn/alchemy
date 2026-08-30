// Package refusable is the corpus of results no store may write, shared by the
// four connectors' tests.
//
// It exists because the four were written without sight of each other and each
// arrived at a different subset of the same refusals: all four refused a held
// job, three refused two entities under one ID, one refused two chunks under
// one index, and none refused one chunk embedded twice. Every one of those
// gaps is a silent overwrite — a record written where two were counted — and
// the connector that had it could not know, because nothing said the invariant
// existed.
//
// pkg/preflight is where the rule now lives. This is the evidence that each
// connector actually asks: the same corpus, refused by all four, so a store
// that quietly stopped asking fails a test rather than a customer's import.
package refusable

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// Case is one result that must not reach a store, and why.
type Case struct {
	Name string
	// Why is what a failing test should print: the harm, not the rule.
	Why    string
	Result alchemy.Result
}

// prov is a full provenance so that a refusal is not accidentally about a
// missing field somewhere else.
func prov(chunk int) alchemy.Provenance {
	return alchemy.Provenance{
		Source: "architecture.md", Chunk: chunk,
		Producer: alchemy.ProducerLLMExtract, Model: "gemini-3.6-flash-high",
		Ontology: "sds@3", Chunking: "heading", Confidence: 0.82,
	}
}

// base is a small, correct graph. Each case below breaks exactly one thing in
// it, so a connector that refuses a case is refusing that thing and not some
// second defect the fixture happened to carry.
func base(dim int) alchemy.Result {
	vec := func(chunk int) alchemy.Vector {
		v := alchemy.Vector{Chunk: chunk, Model: "embed-3", Values: make([]float32, dim)}
		for i := range v.Values {
			v.Values[i] = float32(chunk + 1)
		}
		return v
	}
	res := alchemy.Result{
		Job: "job-refusable",
		Entities: []alchemy.Entity{
			{ID: "e1", Type: "System", Name: "SuperAI", Provenance: prov(0)},
			{ID: "e2", Type: "System", Name: "CortexDB", Provenance: prov(1)},
		},
		Relations: []alchemy.Relation{
			{From: "e1", To: "e2", Type: "USES", Provenance: prov(1)},
		},
		Chunks: []alchemy.Chunk{
			{Index: 0, Source: "architecture.md", Text: "SuperAI is a system.", Strategy: "heading"},
			{Index: 1, Source: "architecture.md", Text: "SuperAI uses CortexDB.", Strategy: "heading"},
		},
		Vectors: []alchemy.Vector{vec(0), vec(1)},
	}
	res.Counts = res.Derivable()
	return res
}

// Cases is every result pkg/preflight refuses, phrased as the harm a store
// would do by accepting one.
func Cases(dim int) []Case {
	held := base(dim)
	held.Conflicts = []alchemy.Conflict{{
		Kind: alchemy.ConflictEntityType, Subject: "e1",
		Detail: `entity "e1" is typed "System" by one source and "Node" by another`,
		Left:   alchemy.Claim{Statement: `entity "e1" is of type "System"`, Provenance: prov(0)},
		Right:  alchemy.Claim{Statement: `entity "e1" is of type "Node"`, Provenance: prov(1)},
	}}
	held.Counts = held.Derivable()

	sharedID := base(dim)
	sharedID.Entities = append(sharedID.Entities, alchemy.Entity{
		ID: "e1", Type: "Node", Name: "SuperAI-the-node", Provenance: prov(1),
	})
	sharedID.Counts = sharedID.Derivable()

	sharedChunk := base(dim)
	sharedChunk.Chunks = append(sharedChunk.Chunks, alchemy.Chunk{
		Index: 1, Source: "operations.md", Text: "A second file's second chunk.", Strategy: "heading",
	})
	sharedChunk.Counts = sharedChunk.Derivable()

	twoVectors := base(dim)
	extra := twoVectors.Vectors[0]
	extra.Values = make([]float32, dim)
	twoVectors.Vectors = append(twoVectors.Vectors, extra)
	twoVectors.Counts = twoVectors.Derivable()

	danglingVector := base(dim)
	danglingVector.Vectors = append(danglingVector.Vectors, alchemy.Vector{
		Chunk: 9, Model: "embed-3", Values: make([]float32, dim),
	})
	danglingVector.Counts = danglingVector.Derivable()

	twoWidths := base(dim)
	twoWidths.Vectors[1].Values = make([]float32, dim+1)
	twoWidths.Counts = twoWidths.Derivable()

	emptyVector := base(dim)
	emptyVector.Vectors[1].Values = nil
	emptyVector.Counts = emptyVector.Derivable()

	return []Case{
		{"held", "a graph that contradicts itself is one an agent answers from, confidently, with a citation (§7.3)", held},
		{"entity_id_reused", "relations name entities by ID, so both records become one node and every edge naming it points at whichever was written last", sharedID},
		{"chunk_index_reused", "a chunk index is what a vector and a provenance point at, so two files' chunks are stored as one and the other is lost", sharedChunk},
		{"chunk_vectored_twice", "a store joins a vector to its text by the chunk index, so the second is written over the first and nothing says which the search answered from", twoVectors},
		{"vector_without_chunk", "an embedding with no text behind it is searchable, citable and pointing at nothing", danglingVector},
		{"vector_width", "an index takes one width, and nothing in the data says which was meant", twoWidths},
		{"vector_empty", "a vector with no dimensions matches everything or nothing, depending on the index", emptyVector},
	}
}

// Clean is the same graph with nothing wrong with it, so a conformance test can
// prove the refusals above are about the defect rather than about the fixture.
func Clean(dim int) alchemy.Result { return base(dim) }
