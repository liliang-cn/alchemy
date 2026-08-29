// Package alchemy holds the types every stage of the pipeline speaks.
//
// It contains declarations and no behaviour, deliberately: it is the contract
// between the chunker, the source readers, the extractor, the verifier and the
// service, and a contract that computes things is one that has opinions about
// stages it should not know exist.
package alchemy

import "time"

// Producer names what made an entity or a relation. It is the field DESIGN.md
// §5b calls "the field that matters": ProducerDDL and ProducerGraphImport mean
// a machine read something that already said this, ProducerLLMExtract means a
// model decided it, and an agent citing the graph can say which.
type Producer string

const (
	// ProducerDDL — a CREATE TABLE stated the entity, a FOREIGN KEY the relation.
	ProducerDDL Producer = "ddl"
	// ProducerGraphImport — an existing graph already asserted it.
	ProducerGraphImport Producer = "graph-import"
	// ProducerTabular — a table's header and rows produced it, under a mapping
	// that may itself have been inferred (see Guess).
	ProducerTabular Producer = "tabular"
	// ProducerLLMExtract — a model read prose and proposed it.
	ProducerLLMExtract Producer = "llm-extract"
)

// Deterministic reports whether the producer read something that already
// stated the fact, rather than inferring it. It is the split Counts reports and
// the split §5c uses to decide what is worth a reviewer's time — a person asked
// to confirm what a CREATE TABLE says is a person being taught to click Approve
// without reading.
//
// The default is false on purpose: a producer added without a decision here
// arrives marked as inferred, which is the safe direction to be wrong in.
func (p Producer) Deterministic() bool {
	switch p {
	case ProducerDDL, ProducerGraphImport:
		return true
	default:
		return false
	}
}

// Provenance says where a fact came from and how it was produced. Every entity
// and every relation carries one; §5b makes this a product guarantee rather
// than a debugging aid.
type Provenance struct {
	// Source is the name of the file or connection the fact came from.
	Source string `json:"source"`
	// Chunk is the index of the chunk it was extracted from, or -1 when the
	// producer did not work in chunks (DDL, graph import).
	Chunk int `json:"chunk"`
	// Producer is what made it.
	Producer Producer `json:"producer"`
	// Model is the model that produced it, empty for deterministic producers.
	Model string `json:"model,omitempty"`
	// Ontology identifies the vocabulary the extraction was constrained by.
	Ontology string `json:"ontology,omitempty"`
	// Chunking is the strategy that produced the chunk. §7.1: a graph
	// re-extracted under a different strategy is a different graph, and a
	// reader comparing two runs needs to know which one they are looking at.
	Chunking string `json:"chunking,omitempty"`
	// Confidence is the model's own confidence, 0 for deterministic producers.
	Confidence float64 `json:"confidence,omitempty"`
	// ReviewedBy records who accepted this after review. §5c: review adds to
	// provenance, it does not overwrite it — a reviewed edge still says a model
	// proposed it.
	ReviewedBy string `json:"reviewed_by,omitempty"`
}

// Entity is a node of the returned graph.
type Entity struct {
	// ID is stable within one result and is how relations refer to entities.
	ID string `json:"id"`
	// Type must be a type the ontology declares; anything else is a Violation.
	Type string `json:"type"`
	Name string `json:"name"`
	// Attributes are whatever the source stated beyond type and name.
	Attributes map[string]any `json:"attributes,omitempty"`
	Provenance Provenance     `json:"provenance"`
}

// Relation is an edge of the returned graph. From and To are Entity IDs.
type Relation struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Type must be a relation type the ontology declares between these entity
	// types; anything else is a Violation.
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Provenance Provenance     `json:"provenance"`
}

// Chunk is a span of source text an extractor can see at once. §7.1: chunk
// boundaries decide what an extractor can see, so the strategy that produced
// one travels with it.
type Chunk struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
	// Source is the file the text came from.
	Source string `json:"source"`
	// Strategy is the chunking strategy that produced this chunk.
	Strategy string `json:"strategy"`
	// Heading is the section this chunk sits under, when the strategy knows.
	Heading string `json:"heading,omitempty"`
	// Start and End are byte offsets into the source text, so a reader can find
	// the chunk in the original.
	Start int `json:"start"`
	End   int `json:"end"`
}

// Vector is an embedding of one chunk. §5c: vectors are computed after review,
// so they describe the text that survived it.
type Vector struct {
	Chunk  int       `json:"chunk"`
	Values []float32 `json:"values"`
	Model  string    `json:"model"`
}

// ViolationKind says which rule an extraction broke.
type ViolationKind string

const (
	// ViolationUnknownEntityType — an entity whose type the ontology does not declare.
	ViolationUnknownEntityType ViolationKind = "unknown_entity_type"
	// ViolationUnknownRelationType — a relation whose type the ontology does not declare.
	ViolationUnknownRelationType ViolationKind = "unknown_relation_type"
	// ViolationRelationNotAllowed — a declared relation type used between entity
	// types the ontology does not allow it between.
	ViolationRelationNotAllowed ViolationKind = "relation_not_allowed"
	// ViolationDanglingRelation — a relation naming an entity the result does not contain.
	ViolationDanglingRelation ViolationKind = "dangling_relation"
)

// Violation is one source saying something the ontology does not allow. §7.3:
// it is attributable, excludable, and the rest of the graph is usable without
// it — which is why a violation does not hold the job.
type Violation struct {
	Kind ViolationKind `json:"kind"`
	// Detail says what was wrong in words a person can act on.
	Detail string `json:"detail"`
	// Subject is the entity ID or the "from -[type]-> to" that broke the rule.
	Subject    string     `json:"subject"`
	Provenance Provenance `json:"provenance"`
}

// Guess is an inferred mapping the pipeline made and is obliged to report.
// §2.1: a guess that does not announce itself is a bug with a three-month fuse.
type Guess struct {
	// Field is what was being mapped, e.g. a source column name.
	Field string `json:"field"`
	// ChosenAs is what it was mapped to.
	ChosenAs string `json:"chosen_as"`
	// Alternatives are the candidates that were not chosen. A guess with a
	// non-empty Alternatives list is one a reviewer should look at first.
	Alternatives []string `json:"alternatives,omitempty"`
	// Reason says why this candidate won.
	Reason     string     `json:"reason,omitempty"`
	Provenance Provenance `json:"provenance"`
}

// ConflictKind says what shape of disagreement was found.
type ConflictKind string

const (
	// ConflictEntityAttributes — the same entity arrived twice with different attributes.
	ConflictEntityAttributes ConflictKind = "entity_attributes"
	// ConflictEntityType — the same entity arrived twice with different types.
	ConflictEntityType ConflictKind = "entity_type"
	// ConflictRelationDirection — one source says A→B, another says B→A.
	ConflictRelationDirection ConflictKind = "relation_direction"
	// ConflictContradiction — a deterministic edge contradicted by an inferred one.
	ConflictContradiction ConflictKind = "contradiction"
)

// Conflict is two sources both claiming to be right, with nothing in the data
// to decide between them. §7.3: a conflict always holds the job, whether or not
// review mode is on.
type Conflict struct {
	Kind ConflictKind `json:"kind"`
	// Subject is what the two sources disagree about.
	Subject string `json:"subject"`
	// Detail states the disagreement in words.
	Detail string `json:"detail"`
	// Left and Right are the two claims, each with its own provenance, so a
	// reviewer can see a schema on one side and a PDF on the other.
	Left  Claim `json:"left"`
	Right Claim `json:"right"`
}

// Claim is one side of a Conflict.
type Claim struct {
	// Statement renders the claim for a person reading the queue.
	Statement  string     `json:"statement"`
	Provenance Provenance `json:"provenance"`
}

// Counts is the block that makes the difference between a graph you can act on
// and one you merely have. §5: every returned graph is accompanied by the
// numbers needed to distrust it.
type Counts struct {
	Entities      int `json:"entities"`
	Relations     int `json:"relations"`
	Deterministic int `json:"deterministic"`
	Inferred      int `json:"inferred"`
	Violations    int `json:"violations"`
	Conflicts     int `json:"conflicts"`
	Guesses       int `json:"guesses"`
	// ChunksEmpty is chunks that produced nothing. A high number means the
	// extraction is failing quietly.
	ChunksEmpty int `json:"chunks_empty"`
	// ChunksUnread is source text that could not be read at all — a scanned PDF
	// page with no OCR model supplied. §5: never silently returned as empty.
	ChunksUnread int `json:"chunks_unread"`
}

// ModelCall records what a job spent, by model and stage. §7.2: cost is not
// optimised for, but it is never hidden.
type ModelCall struct {
	Model string `json:"model"`
	Stage string `json:"stage"`
	Calls int    `json:"calls"`
	// Tokens is reported when the provider reports it, 0 otherwise.
	Tokens int `json:"tokens,omitempty"`
}

// Result is what a finished job returns. §4: it is returned, not stored.
type Result struct {
	Entities  []Entity   `json:"entities"`
	Relations []Relation `json:"relations"`
	Chunks    []Chunk    `json:"chunks,omitempty"`
	Vectors   []Vector   `json:"vectors,omitempty"`

	Conflicts  []Conflict  `json:"conflicts"`
	Violations []Violation `json:"violations"`
	Guesses    []Guess     `json:"guesses"`

	Counts     Counts      `json:"counts"`
	ModelCalls []ModelCall `json:"model_calls,omitempty"`
	// Unread names source material that could not be read, with why.
	Unread []Unread `json:"unread,omitempty"`
}

// Unread is source material the pipeline could not read. It exists so that
// "no text here" is never indistinguishable from "an empty page".
type Unread struct {
	Source string `json:"source"`
	// Locator is a page number, sheet name or byte range — whatever identifies
	// the part that could not be read.
	Locator string `json:"locator"`
	Reason  string `json:"reason"`
}

// JobState is where a job is. §7.3: NeedsReview is reachable without review
// mode being on, because a conflict always requires a person.
type JobState string

const (
	JobPending     JobState = "PENDING"
	JobRunning     JobState = "RUNNING"
	JobNeedsReview JobState = "NEEDS_REVIEW"
	JobSucceeded   JobState = "SUCCEEDED"
	JobFailed      JobState = "FAILED"
	JobExpired     JobState = "EXPIRED"
	JobCancelled   JobState = "CANCELLED"
)

// SourceKind is which of the four readers handles a source.
type SourceKind string

const (
	SourceTabular  SourceKind = "tabular"
	SourceDDL      SourceKind = "ddl"
	SourceDocument SourceKind = "document"
	SourceGraph    SourceKind = "graph"
)

// Job is the unit of work, and the only thing the service holds. §5c: what is
// held is work in progress, never a knowledge base.
type Job struct {
	ID        string    `json:"id"`
	State     JobState  `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is when un-reviewed work is discarded. §5c: without it the
	// "stateless" service quietly grows a database of abandoned reviews.
	ExpiresAt time.Time `json:"expires_at"`
	// Stage is the pipeline stage currently running, for progress reporting.
	Stage string `json:"stage,omitempty"`
	// Error is set when State is JobFailed.
	Error string `json:"error,omitempty"`
}
