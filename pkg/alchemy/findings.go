package alchemy

// This file holds what a job found wrong, or could not settle, or could not
// decide: violations, guesses, conflicts and duplicates. They are together
// because they are one thing from a reader's point of view — §5's "numbers
// needed to distrust" the graph, with the records behind each number — and
// apart from types.go because that file is the graph itself, and a reader
// looking for what an Entity is should not have to walk past four kinds of
// complaint to find it.

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

// RefKind says which half of the graph a Ref points into.
type RefKind string

const (
	RefEntity   RefKind = "entity"
	RefRelation RefKind = "relation"
)

// Ref names one graph record in fields rather than in prose.
//
// It exists because a finding that could only say which record it was about by
// rendering a sentence is a finding no consumer can act on. §5b's promise is
// that a wrong record is attributable — "you can see it was inferred, by which
// model, from which chunk of which file — and therefore checkable, correctable,
// and excludable" — and excludable is the word that fails: a sink holding a
// graph and a list of violations cannot exclude the offending records without
// parsing "a -[USES]-> b" back into three fields, which is a private copy of
// another package's output format that no test in either would notice drifting.
// Duplicate got this right from the start with Left.ID and Right.ID; Violation
// did not.
//
// Kind says which set of fields is populated: ID and Type for an entity, From,
// To, Type and Key for a relation. Key is here for the reason it is on
// Relation — without it two foreign keys between one pair of tables are one
// Ref, and a consumer acting on the second acts on the first.
//
// It carries no provenance. A Ref is which record, and where a finding needs
// the source too the finding carries its own; see review.Ref, which is this
// plus the narrowing a reviewer's decision needs and is a different question.
type Ref struct {
	Kind RefKind `json:"kind"`
	// ID and Type name an entity when Kind is RefEntity. The type is part of
	// what the record claims: two records both calling themselves n1 while
	// typing it differently are the whole of ConflictEntityType.
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
	// From, To and Key name a relation when Kind is RefRelation, together with
	// Type. They are the same four fields Relation.Identity is a function of,
	// so a consumer holding a Ref can compute the identity of the edge it names
	// without going back to the graph.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Key  string `json:"key,omitempty"`
}

// Violation is one source saying something the ontology does not allow. §7.3:
// it is attributable, excludable, and the rest of the graph is usable without
// it — which is why a violation does not hold the job.
type Violation struct {
	Kind ViolationKind `json:"kind"`
	// Detail says what was wrong in words a person can act on.
	Detail string `json:"detail"`
	// Subject is the entity ID or the "from -[type]-> to" that broke the rule,
	// rendered for a person. It is what a review item is filed under and what a
	// standing rule is matched on, so it stays exactly as it was; About is the
	// same subject in fields, for the consumer that has to join rather than
	// read. A finding whose subject a reader had to parse is one no sink can
	// act on — see Ref.
	Subject string `json:"subject"`
	// About is the record this violation is about, in fields.
	//
	// It is zero for a violation about something that is not a graph record:
	// a malformed row and an unnamed column are about a file, and inventing a
	// Ref for them would be a join that resolves to nothing.
	About      Ref        `json:"about,omitzero"`
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
	// Subject is what the two sources disagree about, rendered for a person.
	//
	// It stays prose, and the decision is deliberate rather than an oversight —
	// Violation grew an About because a sink has to join a finding to a row it
	// wrote, and a conflict is the one finding no sink ever holds. §7.3 is why:
	// a job with an unanswered conflict does not finish, GetResult refuses it,
	// and nothing writes it anywhere. By the time a conflict does reach a store
	// it has been answered, and what it is then is a note saying this graph was
	// questioned and by whom — read, not joined.
	//
	// It is also not one record. A conflict names two claims about a subject
	// that is sometimes neither an entity nor an edge but an attribute of one
	// ("n1.region"), and a structured form would have to invent a third shape
	// for that. pkg/review does need to reach the records, and does it by
	// building the same strings from the graph and looking the subject up —
	// which cannot drift silently, because a subject that no record renders
	// simply finds nothing.
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
	//
	// It needs no structured companion, and it is the only finding that never
	// did: Left.ID and Right.ID have been below since the first version, so a
	// consumer wanting the two nodes has never had to parse the rendering. That
	// is the shape Violation lacked and now has as About.
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
