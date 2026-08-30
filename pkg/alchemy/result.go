package alchemy

import "time"

// This file is what a whole job returns and what a job is: the counts, the
// bill, the policy, the graph they describe, and the Job that produced it.
// types.go is one record and findings.go is one complaint; this is the
// envelope both arrive in.

// Counts is the block that makes the difference between a graph you can act on
// and one you merely have. §5: every returned graph is accompanied by the
// numbers needed to distrust it.
// Most of it is checkable, and the two exceptions are named. Result.Derivable
// recomputes the eleven fields that are a function of the slices beside them, and
// pkg/preflight is where a consumer compares the two. That comparison is the
// point of the block: a claim nobody can test is a claim, and §5 asks for
// numbers a reader can distrust the graph *with*. One store wrote its own tally
// beside this one because the two could disagree and it had no way to say which
// was right — which is the honest thing to do when the contract offers no
// answer, and this is the answer.
type Counts struct {
	Entities  int `json:"entities"`
	Relations int `json:"relations"`
	// Chunks and Vectors are how many of each the job produced.
	//
	// They are here because §8.4 pages a large result and the summary rides on
	// the first page: a consumer streaming a graph gets counts.entities before
	// the entities and can say when it has them all, and until these two
	// existed it could say nothing of the sort about chunks or vectors. One
	// store had to write "I loaded M" with no way to finish the sentence "of
	// the N the job produced".
	//
	// They are also the pair that makes ChunksEmpty and ChunksUnread readable.
	// "23 chunks produced nothing" is a different corpus at 30 chunks than at
	// 3000, and the denominator was missing.
	Chunks        int `json:"chunks"`
	Vectors       int `json:"vectors"`
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
	// Job is the ID of the job that produced this graph, and it is the only
	// identity a result has.
	//
	// It was missing, and the shape of what four stores did about that is the
	// argument for it. Two demanded a run ID from their caller, which pushes
	// the question onto somebody who often does not have an answer either. Two
	// wrote a SHA-256 over the whole encoded result, independently, with
	// near-identical comments — and one of those had no choice, because a
	// Qdrant point ID must be a UUID or an integer, so it must be derived from
	// something. All four were solving "the same corpus loaded twice must not
	// double the graph", and none of them could, because Entity.ID is stable
	// within one result and says nothing across runs.
	//
	// The service had the answer the whole time. Job.ID is stated by the
	// producer, is stable across a retry, survives §8.3's takeover by another
	// node, and — unlike a content digest — does not change when this struct
	// grows a field. A fingerprint over the whole result would have orphaned
	// every loaded corpus each time Duplicates, RuleSets, RuledBy or this field
	// was added, which is the failure mode one of those two stores named in its
	// own comment. See the Fingerprint note in behaviour.go for why one is not
	// offered.
	//
	// Empty is a result nobody named — a library caller running the pipeline
	// directly — and it means what it says: this graph has no identity, so a
	// store must not pretend it can recognise it again.
	Job string `json:"job,omitempty"`

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
	// Supersessions are the records this run says are over, and who said so.
	//
	// It is the third thing a correction has to be able to state and the one
	// that had nowhere to go. A person who knows the CTO changed is stating
	// two facts — that Bruno holds the office, and that Ada no longer does —
	// and until this list existed only the first had a shape. The second
	// arrived as a new edge beside the old one and the graph reported itself
	// clean while holding both, which is measured rather than argued: one
	// company profile and one correction in a single job, conflicts zero, two
	// CTOs.
	//
	// It is beside the graph rather than a field on the record that supersedes
	// because alchemy.Provenance must stay comparable — review.Ref embeds one
	// and is a map key — and because a supersession is a claim about a pair,
	// like Conflict and Duplicate, not a property of one node.
	//
	// Alchemy does not act on it. §4 means it holds no graph and could not;
	// and a producer that could delete another producer's fact by naming it
	// would be §2.1 with write access. Where both records are in one result
	// the disagreement is still ConflictCardinality and still §7.3. What this
	// buys is that the statement survives the pipeline: a store can act on it
	// deliberately, and a reader who fetches the graph months later can see
	// that somebody said the old answer was over, and name them.
	Supersessions []Supersession `json:"supersessions,omitempty"`
	// Proposals are the types this corpus used and the ontology does not
	// declare, one per type rather than one per record. See Proposal: the
	// vocabulary is a claim about a corpus, and the corpus is what says what
	// the claim is missing.
	Proposals []Proposal `json:"proposals,omitempty"`
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

// Supersession is one record saying another is over.
//
// Retires is an Entity.ID or a Relation.Identity, whichever kind of record is
// being replaced, and it deliberately need not be present in this result: the
// thing being superseded is usually in a store from a run that finished last
// month, and refusing the claim because this result does not contain it would
// make the whole field useless for the case it exists for. A consumer that
// cannot find it says so; it does not fail.
//
// By is the record making the claim, so a reader can ask the same question of
// a supersession they can ask of any other fact here — who says so — and get a
// producer, a source and, for alchemy.ProducerHuman, a person and a date.
type Supersession struct {
	// Retires is the Entity.ID or Relation.Identity being replaced.
	Retires string `json:"retires"`
	// By is the record that replaces it.
	By Ref `json:"by"`
	// Reason is why, in the asserter's words. It is required in practice and
	// not enforced here, for the reason §5c gives about rules: a correction
	// nobody explained is one nobody can argue with later.
	Reason string `json:"reason,omitempty"`
	// Provenance is who is making this claim. It is the supersession's own,
	// not the superseding record's, because a reviewer may retire a record
	// that a model proposed and the two are different claims by different
	// parties.
	Provenance Provenance `json:"provenance"`
}
