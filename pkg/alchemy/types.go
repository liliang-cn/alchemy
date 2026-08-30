// Package alchemy holds the types every stage of the pipeline speaks.
//
// It contains declarations and no behaviour, deliberately: it is the contract
// between the chunker, the source readers, the extractor, the verifier and the
// service, and a contract that computes things is one that has opinions about
// stages it should not know exist.
package alchemy

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
	// ProducerHuman — a named person asserted it, because they know it.
	//
	// It is the only producer whose source is not a document, and it exists
	// because the alternative was worse. A fact somebody knows and no file
	// states had exactly one way in: write it into a JSON file and import it,
	// which arrives as ProducerGraphImport — "an existing graph already
	// asserted it". That is not what happened. An agent citing such an edge
	// could name the file and could not name the person, the date or the
	// reason, which is the half of §5b that makes the other half worth paying
	// for: a reader can tell a guess from a statement, and could not tell one
	// person's statement from another system's export.
	//
	// It is deterministic, and that is the substantive claim here rather than a
	// bookkeeping detail. §5b's split is "somebody stated the fact, rather than
	// inferring it", and a person signing their name to a sentence is the
	// clearest case of stating there is — clearer than a foreign key, which
	// states what a schema believes rather than what anybody checked. What
	// makes an llm-extract edge inferred is not that a machine wrote it down;
	// it is that nobody can be asked about it.
	//
	// The obligation that comes with it is Provenance.By and Provenance.At,
	// which pkg/preflight requires for this producer and no other. An assertion
	// nobody is named for is an anonymous claim wearing a person's badge, and
	// it would be the §2.1 failure with a REST endpoint attached.
	ProducerHuman Producer = "human"
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
	case ProducerDDL, ProducerGraphImport, ProducerHuman:
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
	// Chunk is the Chunk.Index of the chunk it was extracted from, or -1 when
	// the producer did not work in chunks (DDL, graph import).
	//
	// It is a Chunk.Index and not a position in Result.Chunks, and the two are
	// only the same thing by accident. §8.4 pages a large result, so an entity
	// can arrive in a message whose Chunks slice is empty or holds a different
	// window of the job; a consumer that indexed the slice with this would
	// silently read the wrong chunk or run off the end. Join on Chunk.Index.
	//
	// That join is well-defined because a job's chunk indexes are unique across
	// the whole job — see Chunk.Index, which is where that invariant is stated
	// and pkg/preflight where it is checked.
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
	// By names the person who asserted this, for ProducerHuman. It is required
	// for that producer and meaningless for the others, whose sources are
	// documents and whose Source field already says which one.
	//
	// It is not ReviewedBy. ReviewedBy says somebody accepted a record another
	// producer proposed, and the record still says who proposed it; this says
	// nobody proposed it and a person is the whole of its authority. A reader
	// filtering to "what a machine read" must exclude these and include the
	// reviewed ones, and a single field could not tell them apart.
	By string `json:"by,omitempty"`
	// At is when the assertion was made, RFC 3339.
	//
	// A string rather than a time.Time so that an unset value marshals to
	// nothing: every other optional field here omits when empty, and a struct
	// cannot. That is not a formatting preference — sink.Digest hashes this
	// struct through encoding/json, so a field that always renders would change
	// the content address of every result ever produced the moment it was
	// added. Provenance can grow and alchemy.Counts cannot, and omitempty is
	// the entire difference.
	At string `json:"at,omitempty"`
}

// Entity is a node of the returned graph.
type Entity struct {
	// ID is stable within one result and is how relations refer to entities.
	ID string `json:"id"`
	// Type must be a type the ontology declares; anything else is a Violation.
	Type string `json:"type"`
	Name string `json:"name"`
	// Attributes are whatever the source stated beyond type and name.
	//
	// The values are what encoding/json produces and nothing else: string,
	// float64, bool, nil, []any and map[string]any, nested to any depth. §4
	// makes the JSON the contract, so the domain of an attribute value is the
	// JSON value domain — and saying so is not pedantry, because four stores
	// were written against this map by four people and each had to decide for
	// itself what a nested value even is. Two of them invented the same
	// breadcrumb convention for values their property model cannot hold; two
	// needed none. All four were right, and none of them could tell.
	//
	// So the split is stated rather than left to be discovered: what may be in
	// here is this package's business, and what a store does with a value its
	// type system cannot hold is that store's — a property graph flattening a
	// nested object to JSON text is a faithful store making the best of its own
	// model, and it owes its reader a way to see that it did.
	//
	// A producer that puts anything else in here — a time.Time, a struct, an
	// int that survives only because nobody marshalled it yet — breaks the
	// contract for every consumer at once, and quietly: it round-trips through
	// this process and changes type on the way to the next one. pkg/preflight
	// checks it, which is what turns this paragraph into something a run can
	// fail rather than a claim in a comment.
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
	//
	// Key is what an edge's identity is made of and not the whole of it; see
	// Identity, which renders the whole of it, because leaving the rule stated
	// here and rendered nowhere is what let four stores each invent a different
	// one.
	Key string `json:"key,omitempty"`
	// Attributes are whatever the source stated about the edge. The value
	// domain is Entity.Attributes'; the argument is there and is the same one.
	Attributes map[string]any `json:"attributes,omitempty"`
	Provenance Provenance     `json:"provenance"`
}

// Chunk is a span of source text an extractor can see at once. §7.1: chunk
// boundaries decide what an extractor can see, so the strategy that produced
// one travels with it.
type Chunk struct {
	// Index is this chunk's number within the whole job, not within its
	// source, and it is the only name a chunk has.
	//
	// The distinction is the whole of it, and this type reads as if the
	// opposite were true: an Index sitting next to a Source is exactly how one
	// would spell "the third chunk of this file". It is not. Provenance.Chunk
	// and Vector.Chunk are plain ints with no source beside them, so the only
	// way either of them names one span of one file is if the number is unique
	// across the entire result — and it is, because pkg/pipeline's adopt
	// renumbers every source's chunks as it takes them in.
	//
	// That invariant was true and unwritten, which is a bad combination: a
	// store that joined a vector to a chunk by index would work perfectly until
	// two files' first chunks both arrived numbered 0, and then it would write
	// one chunk, lose the other, and report having written two. One of the four
	// stores found that path and refuses it by name; the others were exposed to
	// it and had no way to know. So it is stated here, checked in
	// pkg/preflight, and enforced by the pipeline on its own output.
	//
	// A result assembled by something other than this pipeline owes the same
	// promise. It is not "renumber your chunks"; it is "a chunk index names one
	// chunk", which is what everything downstream already assumes.
	Index int    `json:"index"`
	Text  string `json:"text"`
	// Source is the file the text came from. It is the chunk's origin, not part
	// of its identity — see Index.
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
	// Chunk is the Chunk.Index of the text this embeds — a chunk's number, not
	// a position in Result.Chunks, for the reason Provenance.Chunk gives.
	Chunk  int       `json:"chunk"`
	Values []float32 `json:"values"`
	Model  string    `json:"model"`
}
