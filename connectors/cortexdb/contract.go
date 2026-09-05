package cortexdb

import (
	"encoding/json"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The knowledge contract, and the part of it this connector can tell the truth
// about.
//
// CortexDB is a shared brain: alchemy, argus, DataIntelligence and CortexDB's
// own memory all write into it and none of them imports another, so the store
// is the only place their three vocabularies for "how do I know this" can be
// made one. The contract is that vocabulary — a set of reserved `_` keys, no
// new RPC — and it ratifies every key provenance.go already writes, unchanged.
// What it adds is the word alchemy never had to say, because until now nothing
// was going to read an alchemy edge next to a di-consult precedent.
//
// The constants are MIRRORED here rather than imported, and the reason is
// mechanical rather than a preference. Their home is CortexDB's
// pkg/cortexdb/contract.go, which landed in a commit that is not tagged; this
// module resolves the store from a published version (go.mod: v2.90.0, and
// v2.92.1 is the newest tag), and neither carries the file. An import would not
// compile against anything `go mod download` can fetch. contract_test.go writes
// every literal out as the spec's own table writes it, so the copy fails loudly
// on the day the two disagree rather than grading a corpus into a value nobody
// filters on.
const (
	keyGrade       = "grade"
	keyState       = "state"
	keyWhy         = "why"
	keyContradicts = "contradicts"
)

// The five grades: by what kind of thing a record's truth is established.
//
// Deliberately not a ladder the producers' own verdicts map onto one-to-one —
// di-anchor's Anchored and alchemy's confidence-plus-review are answers to
// different questions, and one axis for both would flatten what each of them
// documents at length. The finer word goes in keyState untouched.
//
// Two of the five are here for the mirror test and are never written by this
// connector; which two, and why, is the subject of contractGrade below.
const (
	gradeVerified       = "verified"
	gradeSelfConsistent = "self_consistent"
	gradeAsserted       = "asserted"
	gradeHeld           = "held"
	gradeRefused        = "refused"
)

// WHAT THIS CONNECTOR CANNOT SAY, and why it says nothing rather than
// something. §5b's guarantee is that a wrong record is attributable; a record
// carrying a grade its writer cannot justify from the data in front of it is
// the same failure with a confident face on.
//
// `held` — the spec maps any NEEDS_REVIEW onto it. A sink never sees one. The
// only NEEDS_REVIEW that survives to a load is a conflict, and §7.3 refuses the
// whole result before the first write (plan.go's ErrHeld check). The rest —
// violation, guess, duplicate, low confidence — are the review QUEUE's kinds,
// built in pkg/review from a threshold and a ranking this package does not
// import and must not copy; a connector holding a private second definition of
// "unsure enough to ask a person" is exactly the drift preflight was
// consolidated to end. A `held` alchemy record needs review.Kind to reach a
// sink, which is a change to pkg/sink and not to this directory.
//
// `refused` from a REJECTION — review.Apply removes a rejected record from the
// graph (its ref goes in plan.remove and the entity and relation walks skip
// it), which is the right behaviour for §5c and means the rejection is a record
// no store is ever handed. The contract's own argument — "the reader must be
// able to tell 'we have no precedent' from 'we refused to form one'" — applies
// and is unmet, and meeting it means alchemy.Result carrying its rejections,
// outside this directory. What IS reachable is the ontology's refusal, below.
//
// `_state` for a review — the spec asks for the verb. alchemy does not record
// one. Provenance carries ReviewedBy, a comma-separated list of NAMES, and
// RuledBy, a list of rule names; Result carries no decisions at all and
// StandingRule carries a name and the sentence the model was told. Since a
// rejected record is gone, a reviewed record that arrives here was accepted,
// edited or covered by an `always` rule, and nothing on the wire says which.
// Writing "accept" would be this connector inventing the one field the contract
// says is never normalised.
//
// `_contradicts` — WRITTEN, and it is the one row of the spec's second table
// that has moved. It was unreachable for a stated reason: a Conflict named its
// two sides as Claim{Statement, Provenance} — no Ref, no id — so the only route
// to the ids was parsing Conflict.Subject, which is the private copy of another
// package's output format that Ref exists to abolish. alchemy.Claim now carries
// an About, the same shape Violation already had, and plan.disagree turns the
// pairs into what a write puts on both records.
//
// What it does NOT write is as much of the row as what it does. A conflict
// whose two sides name one record gets nothing, because there is no other
// record to name — and that is most conflicts, since an entity or an edge given
// two values for one attribute is one row in this store however many sources
// stated it. Two rows is the reversal and the cardinality clash: the graph
// holds both, neither is being removed, and the disagreement between them is
// the information the contract says to keep. A record naming another this load
// is not writing gets nothing either; an id for a row in some other run is one
// this connector would be inventing.
//
// The `held` row above is what the first case is really asking for and it is
// still out of reach, so a disagreement inside one record leaves this store
// saying nothing about it rather than saying the wrong thing twice.

// contractGrade answers the contract's one narrow question about one record —
// by what kind of thing is this record's truth established — from the
// provenance the record carries and the ontology finding, if any, that names
// it. It returns the grade, the producer's own word for keyState, and the
// reason keyWhy requires beside a refusal.
//
// The order of the tests is the order of the spec's alchemy table, and the two
// that could be argued are argued here:
//
// Review outranks a violation. A reviewer working the queue was SHOWN the
// finding — KindViolation is the second row of §5c's ranking — so a record that
// carries a name and is still in the graph is one a person kept with the
// complaint in front of them, which is the contract's own definition of
// verified ("a named person's review"). Grading it refused would let a
// vocabulary overrule the human it was escalated to, and would do it silently.
//
// Producer.Deterministic() is not consulted, although it looks like exactly
// this question and is one line away. It answers a different one — was a model
// involved — and the two disagree in both directions: ProducerHuman is
// deterministic and `asserted`, because a person asserting something is still
// nobody checking it, and ProducerTabular is not deterministic (its mapping may
// have been guessed) and `self_consistent`, because the rows themselves stated
// what the graph says. Reusing the flag would have been right about four
// producers out of five and silently wrong about the two that matter.
func contractGrade(p alchemy.Provenance, v *alchemy.Violation) (grade, state, why string) {
	if p.ReviewedBy != "" {
		return gradeVerified, "", ""
	}
	if v != nil {
		// The ontology declined to have it and can say why, in the words §7.3
		// obliges a violation to carry: "it names an item, it names why".
		return gradeRefused, "violation:" + string(v.Kind), v.Detail
	}
	switch p.Producer {
	case alchemy.ProducerDDL, alchemy.ProducerGraphImport, alchemy.ProducerTabular:
		// Derived deterministically from something already stated — a CREATE
		// TABLE, an existing graph, a table's own rows — and checked against
		// nothing outside itself.
		return gradeSelfConsistent, "", ""
	default:
		// ProducerLLMExtract and ProducerHuman, and anything added later.
		// Defaulting to the weakest claim is the same decision, in the same
		// direction, that Producer.Deterministic() makes for the same reason: a
		// producer added without a thought here arrives saying nothing checked
		// it, which is the safe direction to be wrong in.
		return gradeAsserted, "", ""
	}
}

// contractMeta writes the contract's verdict about one record beside the
// provenance provenanceMeta already flattened.
//
// keyState and keyWhy are omitted when empty rather than written as "", for
// the reason provenanceMeta gives about every optional field: "this record has
// no reason" and "this record's reason is the empty string" are not the same
// claim, and a property set to "" is one every filter has to know to exclude.
func contractMeta(grade, state, why, prefix string, into map[string]string) {
	into[prefix+keyGrade] = grade
	if state != "" {
		into[prefix+keyState] = state
	}
	if why != "" {
		into[prefix+keyWhy] = why
	}
}

// contradictsMeta writes the ids of the records this one cannot both-be-true
// with, on the record, as the contract's JSON array.
//
// A JSON array in a string, which is the encoding attributeMeta and the alias
// list already use for a value this store's metadata cannot hold natively, and
// which is also what the contract specifies for this key by name: a
// comma-joined list would be shorter and would lose an id with a comma in it,
// and CortexDB's own ids are full of punctuation.
//
// Absent rather than "[]" for a record that disagrees with nothing, for the
// reason provenanceMeta gives about every optional field: "this record
// disagrees with nothing" and "this record's disagreements are the empty list"
// are not the same claim, and pkg/cortexdb.ValidateContract reads an empty
// array as a problem rather than as silence.
func contradictsMeta(ids []string, prefix string, into map[string]string) error {
	if len(ids) == 0 {
		return nil
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("render %s: %w", prefix+keyContradicts, err)
	}
	into[prefix+keyContradicts] = string(b)
	return nil
}

// established orders the grades this connector writes from least to most
// established, so that a fused edge can answer as its weakest member.
//
// It is a ranking for that one use and not a ladder the contract has: the
// spec's whole argument for five values is that they are not one axis. What it
// encodes is only "which of these two says less about the world", and the
// answer a fused edge needs. A refusal ranks below an assertion because an edge
// one source stated and another broke the ontology stating is an edge whose
// reader must see the complaint; ranking it above would hide the finding behind
// the member that happened to be clean.
var established = map[string]int{
	gradeRefused: 0, gradeAsserted: 1, gradeSelfConsistent: 2, gradeVerified: 3,
}

// weakest folds one more member's verdict into a group's.
//
// CortexDB identifies an edge by (from, to, type, document), so several alchemy
// relations can be one edge here, and only one grade fits on it. The rule is
// the one writeRelations already applies to CortexDB's `inferred` flag, in the
// same words: a merged edge that one deterministic source also stated is still
// an edge a model proposed, and marking it deterministic would launder it. A
// group with one reviewed member and one nobody looked at is not a verified
// edge — the reviewer looked at a claim, not at the group the store made of it.
// Every member's own provenance is still in the `_provenance` array, which is
// where a reader goes for the several claims the edge is.
func weakest(grade, state, why string, p alchemy.Provenance, v *alchemy.Violation) (string, string, string) {
	next, nextState, nextWhy := contractGrade(p, v)
	if grade == "" || established[next] < established[grade] {
		return next, nextState, nextWhy
	}
	return grade, state, why
}

// refOfEntity and refOfRelation name a record the way a finding names it, so a
// violation can be joined to the record it is about without parsing its
// Subject. They are the same two functions pkg/verify builds its Refs with, and
// they have to stay the same two: a Ref built differently on this side simply
// finds nothing, which is a grade quietly not written rather than a mismatch
// anybody sees. The test that catches it is the mapping-table one.
func refOfEntity(e alchemy.Entity) alchemy.Ref {
	return alchemy.Ref{Kind: alchemy.RefEntity, ID: e.ID, Type: e.Type}
}

func refOfRelation(r alchemy.Relation) alchemy.Ref {
	return alchemy.Ref{Kind: alchemy.RefRelation, From: r.From, To: r.To, Type: r.Type, Key: r.Key}
}
