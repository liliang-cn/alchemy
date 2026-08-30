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
	// RuleSet names the standing answers (§5c's `always`) that were already in
	// force when this record was extracted. It is the set's *name* — an
	// identity computed from what was in it — and not the set itself; the
	// contents are on the result once, in Result.RuleSets, and the name is how
	// a record points at them.
	//
	// It is a different fact from ReviewedBy and needs its own field for a
	// reason that only shows up once decisions can arrive mid-run (§6). At the
	// end of a job a rule is applied to everything it covers, so ReviewedBy
	// ends up on the record extracted in minute two and on the one extracted
	// in minute ninety alike — it says a person's decision reached this
	// record, and by then it reached both. This says something ReviewedBy
	// cannot: the model that proposed *this* record had already been told, and
	// the model that proposed the other one had not. §7.1 puts Chunking here
	// for the same reason and in almost the same words — a reader comparing
	// two runs needs to know which one they are looking at, and "extracted
	// under a rule the previous chunk was not" is exactly that kind of
	// difference.
	//
	// It holds a name rather than the shapes themselves because the shapes are
	// the same on every record and the field is on every record. A fifty-rule
	// policy is four kilobytes; §8's import is four hundred thousand records;
	// the answer was correct and weighed more than the graph. The name is
	// fixed-width, so a run under fifty rules costs a record exactly what a run
	// under one does, and §8.4's paged result carries the policy once.
	//
	// Empty is the normal case and means what it says: nobody had decided
	// anything by the time this record was proposed.
	RuleSet string `json:"rule_set,omitempty"`
	// RuledBy names the one rule that actually acted on this record — retyped
	// it, renamed it, or answered the question it raised — by the same name
	// Result.RuleSets uses for it.
	//
	// RuleSet says which rules were in the room; this says which one moved. They
	// are different claims and a reader who has only the first cannot get to the
	// second: a graph under a fifty-rule policy in which one record came back
	// retyped says nothing about which of the fifty did it. review.Rule.Covers
	// makes the case for the neighbouring question — "a queue that is three
	// items shorter than the findings should be able to say which rule took each
	// of the three away, and who made it" — and a record that survived a rule is
	// owed the same answer.
	//
	// Empty means no rule acted on this record, which is not the same as no rule
	// being in force; see RuleSet.
	RuledBy string `json:"ruled_by,omitempty"`
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
	Type string `json:"type"`
	// Key is the producer's own name for this edge, and it is what makes two
	// parallel edges two edges rather than one edge described twice.
	//
	// An Entity has had an ID since the first line of this file; a Relation has
	// never had one, and the asymmetry looked harmless because from, to and
	// type are usually enough. They are not enough for the most ordinary thing
	// in SQL: a table that models a relationship between two rows of one table
	// references that table twice, once per end, and a customer schema's NODE_CONNECTIONS
	// does exactly that. Both foreign keys are correct, they say different
	// things about themselves — different columns, different constraint names —
	// and a verifier keying identity on {from, to, type} alone reads them as
	// two sources contradicting each other about one edge. Five of that
	// customer's tables have the shape, and the job could never finish.
	//
	// Identity cannot be recovered from the attributes, either: the attributes
	// are precisely what the conflict check compares, so promoting them to
	// identity would make every disagreement a different edge and
	// ConflictRelationAttributes could never fire again. The producer is the
	// only party that knows. A DDL reader has the constraint name, which SQL
	// itself requires to be unique among a table's constraints; a graph import
	// has the edge's own id when the document carried one; a model reading
	// prose has nothing — it cannot say whether the edge it just proposed is
	// the one it proposed two chunks ago, and inventing a key for it would be
	// exactly the inference wearing a producer's badge that §2.1 warns about.
	//
	// So it is optional, and empty is the honest default. Empty means "this
	// producer cannot tell its edges apart", which is what identity keyed on
	// {from, to, type} has always assumed — so an llm-extract graph behaves
	// exactly as it did, and two sources of equal standing disagreeing about
	// one edge is still the conflict it was built to be.
	//
	// It is scoped to the pair and the type, not global. A producer's ids are
	// its own, and demanding they be unique across a whole job would make one
	// document's edge id collide with another's; two records that carry the
	// same key between the same two nodes are two claims about one edge, which
	// is exactly the reading wanted.
	Key        string         `json:"key,omitempty"`
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

// The kinds above are ontology-shaped: a source said something the declared
// vocabulary does not allow. The kinds below are source-shaped — a file that
// does not fit its own header, which is a way a table can fail and a schema
// cannot.
//
// Both families live here for one reason: Result.Violations is the JSON a
// buyer parses, so its "kind" field has to be a closed set. A reader that
// declares its own kinds privately leaves that field open while the contract
// claims it is not, and a consumer switching on it silently falls through.
const (
	// ViolationMalformedRow — a row that cannot be read against its header.
	ViolationMalformedRow ViolationKind = "malformed_row"
	// ViolationUnnamedColumn — a header cell with no name, so no mapping can
	// refer to the column and its values are left out.
	ViolationUnnamedColumn ViolationKind = "unnamed_column"
	// ViolationMissingID — a record whose identifying field is empty.
	ViolationMissingID ViolationKind = "missing_id"
	// ViolationDuplicateID — two records claiming the same identity, differently.
	ViolationDuplicateID ViolationKind = "duplicate_id"
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
	// ConflictRelationAttributes — the same edge given different attribute values
	// by two sources of equal standing. It is separate from
	// ConflictContradiction because that kind tells a reviewer a schema is
	// involved, and here none is: neither side has more standing than the
	// other, which is precisely what leaves the question for a person.
	ConflictRelationAttributes ConflictKind = "relation_attributes"
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

// DuplicateSignal names the deterministic evidence that two nodes may be one
// thing. It is on the finding rather than left implicit because a reviewer
// answering "are these the same?" is entitled to know what asked them, and
// because a signal that turns out to be noisy has to be identifiable in a
// corpus of past decisions before anybody can argue for removing it.
type DuplicateSignal string

const (
	// DuplicateNameAffix — under one type, one node's name is the other's with
	// whole words added at the front or the back: "document" and "document
	// package", "SQL" and "SQL dumps". It is the shape a per-chunk extractor
	// produces, because each chunk is a separate call that cannot see how the
	// others named the thing, and the commonest addition is the type word
	// itself.
	//
	// What it will wrongly join is a name that was qualified because it names
	// something narrower: "language" and "language model", "Ada" and "Ada
	// Lovelace". That is why this is a finding and not a merge — the evidence
	// is real, and it is not enough to act on alone.
	DuplicateNameAffix DuplicateSignal = "name_affix"
)

// Duplicate is two nodes that may be one node, and are not joined.
//
// It is a third kind of finding beside Violation and Conflict because it is
// neither of them, and pushing it into either would make that one mean less.
// A violation is one source saying something the ontology does not allow: the
// ontology can name the rule that was broken, and the graph minus the offending
// record is still usable. Neither holds here — no declared rule says a
// vocabulary may not contain two names for one thing, and there is no record to
// remove, since removing either node takes its edges with it. A conflict is two
// sources both claiming to be right with nothing in the data to decide between
// them (§7.3), and these two records do not disagree about anything; they agree
// and are merely not joined. Making it a conflict would also hold every prose
// job, since roughly one node in six was one of these in the run that motivated
// it — which turns §7.3's refusal to let a caller opt out of a person into a
// dialog people learn to click through.
//
// So: nothing is wrong, and the graph is delivered. What is owed is the number
// (Counts.Duplicates) and the pair, so that a reader can distrust the graph by
// exactly as much as it deserves.
type Duplicate struct {
	Signal DuplicateSignal `json:"signal"`
	// Subject is the pair, rendered "left ~ right". It is one string because
	// every other finding has a single Subject and the queue, the rules and
	// the stamping all key on it.
	Subject string `json:"subject"`
	// Detail states the case in words a person can answer without opening the
	// source: both names, both chunks, and what joined them.
	Detail string `json:"detail"`
	// Left and Right are the two nodes. Left is the one whose name is
	// contained in the other's, which is a property of the pair rather than of
	// the order they were extracted in — so the same corpus names the same
	// side left however its sections were ordered.
	//
	// Neither side is the "wrong" one. A Conflict has an incumbent and a
	// dissenter because one of them arrived second at a key somebody already
	// held; here nobody is holding anything, which is precisely the defect.
	Left  DuplicateSide `json:"left"`
	Right DuplicateSide `json:"right"`
}

// DuplicateSide is one node of a candidate pair, carrying what a reviewer
// needs to answer without a lookup: which node, what it is called, and where it
// came from.
type DuplicateSide struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	// Provenance is this node's, whole. §5b: a wrong edge is attributable, and
	// the answer to "are these the same thing?" is usually "which chunk said
	// which, and did the second one know about the first".
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
	// Duplicates is how many pairs of nodes may be one node (see Duplicate).
	//
	// It is in this block for the reason the block exists: "one node in six may
	// be a duplicate of another" is exactly a number needed to distrust a
	// graph, and it is one no reader can derive from the entities beside it —
	// the two nodes are well-formed, correctly typed and separately
	// attributable, and nothing about either says the other exists. A graph
	// returned without it looks like a graph of 17 things when it may be a
	// graph of 14.
	Duplicates int `json:"duplicates"`
	// ChunksEmpty is chunks that produced nothing. A high number means the
	// extraction is failing quietly.
	ChunksEmpty int `json:"chunks_empty"`
	// ChunksUnread is source text that could not be read at all — a scanned PDF
	// page with no OCR model supplied. §5: never silently returned as empty.
	ChunksUnread int `json:"chunks_unread"`
	// Dropped is how many entities and relations a standing rule (§5c's
	// `always`) removed without anybody being asked about them.
	//
	// It exists because a rule may now say "reject", and a rejection that
	// leaves no trace is the one way this design can lose a record silently.
	// Everything else a graph is missing is reported: an unread page is in
	// Unread, an empty chunk is in ChunksEmpty, a record a person threw away
	// was thrown away by somebody who saw it. A record a written policy
	// removed before any queue was shown to anyone is invisible in the result
	// — the graph simply comes back one record shorter — and §5's obligation
	// to return "the numbers needed to distrust" the graph would be quietly
	// false without this one.
	//
	// It deliberately does not count what a person rejected while working a
	// queue. That number would conflate a judgement somebody made on a record
	// they read with a policy applied to a record nobody read, and it is the
	// second that a reader needs to be able to see.
	Dropped int `json:"dropped,omitempty"`
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
	// Duplicates is returned whether or not review is on, for the same reason
	// Violations is: a caller running unattended is owed the numbers needed to
	// distrust what it got, and "these two nodes may be one" is not something
	// it can work out later from a graph in which both look fine.
	Duplicates []Duplicate `json:"duplicates"`

	Counts     Counts      `json:"counts"`
	ModelCalls []ModelCall `json:"model_calls,omitempty"`
	// Unread names source material that could not be read, with why.
	Unread []Unread `json:"unread,omitempty"`
	// RuleSets is every standing policy this job's records were extracted
	// under, each named once. Provenance.RuleSet is a name into it.
	//
	// There is more than one because the set changes while the job runs: §6's
	// whole reason for choosing a stream is that "a person working a queue
	// wants their decisions to take effect on work still running", so a rule
	// made in minute three is in force for minute four and was not in force
	// for minute two. A result that named "the job's rules" would be answering
	// a question nobody asked; each set is the policy as it stood when some
	// chunk was actually asked, and a record points at the one it was asked
	// under.
	//
	// It is a separate field from the `always` rules a job's review produced
	// (which this package does not carry at all, and the service returns
	// beside the result) and the difference is not cosmetic. Those are this
	// job's output — the policy a caller keeps and supplies to the next job
	// (§4, §7.3) — and these are its input, whatever the model had been told.
	// An authored rule that was in force and matched nothing is in here and
	// will never be in there, which is exactly the rule a reader chasing a
	// name from a record needs to resolve; and a caller who fed one field back
	// as the other would be re-declaring somebody else's policy as their own
	// job's finding.
	RuleSets []RuleSet `json:"rule_sets,omitempty"`
}

// RuleSet is the standing policy in force at one moment of a job, named so
// that a record can point at it rather than repeat it.
type RuleSet struct {
	// Name is this set's identity, and it is a digest rather than a label for
	// two reasons. It has to be the same on every node that runs a piece of
	// one job (§8.3), so it cannot be a counter or anything else a process
	// invents; and it has to differ whenever the policy differs, because "two
	// records were asked under different rules" is a fact a reader must not
	// lose. It is computed from exactly the members below, so a reader can
	// recompute it and check that this result is telling the truth about
	// itself.
	Name string `json:"name"`
	// Rules is what was in force, in a stable order.
	Rules []StandingRule `json:"rules"`
}

// StandingRule is one rule of such a set: what it is called, and what the
// model was told about it.
type StandingRule struct {
	// Name is the rule's identity — its origin and its shape, as
	// review.Rule.Name writes them. The shape is what the rule matches on, and
	// the origin says whether a person decided a finding or declared a policy
	// in advance; the second is the weaker warrant, and a name that dropped it
	// would let it be read as the stronger.
	Name string `json:"name"`
	// Told is the sentence the model was shown for this rule — the reason the
	// reviewer or the author gave, what to do about it, and who said so.
	//
	// It is here because "which rules were in force" and "what the model was
	// actually told" are the same question at §6's level and not at §5b's: a
	// shape says which class was suppressed, and only the sentence says what
	// the model was asked to stop doing and on whose word. Keeping it means a
	// reader holding the result alone can answer both, without the policy file
	// the job was run with.
	Told string `json:"told,omitempty"`
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
