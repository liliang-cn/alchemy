package alchemy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// This file is the whole of the behaviour in a package whose own doc comment
// says it has none, so each entry has to say why it is here rather than in the
// stage that wants it.
//
// The rule the exceptions are granted under is the one Producer.Deterministic
// was already granted under, stated properly now that there are four of them:
// a function belongs here when every consumer must compute the same answer
// from the declarations in this file, and the declarations alone do not say
// what that answer is. Deterministic classifies a declared constant.
// Relation.Identity renders a declared identity. Result.Held classifies a
// declared field. Counts.Derivable says which of a declared block's numbers
// are recomputable. None of them consults a stage, a model or a store, and
// none of them can be moved to one without the next consumer guessing again —
// which is exactly what four connectors, written without sight of each other,
// each did.
//
// The JSON is the contract (§4), so a Go method is not the deliverable; the
// documented rule is. Every function below states its algorithm in prose
// precise enough for a consumer in another language to reproduce byte for
// byte, and the Go body exists so that Go consumers cannot drift from it.
// Where that is not possible the answer is to decline, and see Fingerprint for
// the one that was declined.

// identityDomain prefixes the digest so that a value computed here can never
// be mistaken for one computed over some other structure that happens to hash
// the same bytes, and so a future encoding change is told from this one by
// bumping the number rather than by silently returning different identities
// under the same name. It is the same discipline pkg/cache's address and
// pkg/review's set name use, for the same reason.
const identityDomain = "alchemy/relation/identity/1"

// Identity is what makes two records one edge, as one string.
//
// Relation.Key's comment already states the rule — identity is the two ends,
// the type, and the producer's own key where the producer has one — and
// leaving it stated but not rendered is what this method exists to fix. Four
// stores were written against this type by four people who could not see each
// other's work, and all four had to invent an edge identity: one hashed the
// attributes and the provenance in with it, so two chunks corroborating one
// edge became two edges; two used the record's position in the slice, so the
// identity of an edge changed when a page boundary moved; one added the source
// document, so one edge asserted by two files became two. The same result had
// a different edge count in each of them, which is not a difference any of
// those stores was wrong to have — nothing here told them.
//
// What it covers, and what it deliberately excludes:
//
//   - From, To and Type, because they are what an edge is.
//   - Key, because it is the only thing that can tell two parallel edges
//     apart, and empty is the honest default that means "this producer cannot
//     tell its edges apart" — which is exactly the identity keyed on the ends
//     and the type that everything here has always assumed.
//   - Not Attributes. They are the thing a conflict compares (see
//     verify.attributeConflicts), so promoting them to identity would make
//     every disagreement a different edge and the disagreement would never be
//     found.
//   - Not Provenance. Two sources asserting one edge is corroboration, and it
//     is the reason Counts can say 890 deterministic and 290 inferred out of
//     1180 edges rather than 1180 out of 1180.
//   - Not position. A slice index is a property of the message a record
//     arrived in, and §8.4 pages a large result: the same edge is at a
//     different offset in a differently-paged stream of the same graph.
//
// What it is not: the identity of an *assertion*. Two records asserting one
// edge are one edge here, which is the right answer for a store that holds one
// row per edge and the wrong one for a store that can hold both claims — a
// property graph that merged them would leave the second unable to name its
// producer, its chunk or its model, which is exactly the guarantee §5b sells.
// Such a store keys its rows on the assertion and groups them by this; the two
// are different questions and both have answers.
//
// It is directed, and that is a real difference from verify's conflict key,
// which is undirected on purpose because a reversal is the question that pass
// exists to find. A store writes the arrow it was given; a verifier asks
// whether two records drew it the same way. Keying a store the undirected way
// would write A→B and B→A as one row and lose the half §7.3 wants a person to
// look at.
//
// The algorithm, stated so another language can reproduce it: SHA-256 over the
// concatenation of five fields — the domain string above, then From, Type, To,
// Key — each written as an unsigned 64-bit big-endian byte length followed by
// the field's UTF-8 bytes; the digest rendered lower-case hex. The framing is
// length-prefixed rather than delimited because entity IDs are folded document
// text and contain every byte sooner or later, so any separator can be forged
// out of the values themselves; a length is unforgeable because the boundaries
// are stated rather than inferred. SHA-256 and big-endian lengths are both
// byte-exact specifications, so the identity a node computes on arm64 today
// equals the one another node computes on amd64 tomorrow — which is the whole
// point when two nodes run halves of one job (§8.3) and two stores load the
// same result.
func (r Relation) Identity() string {
	h := sha256.New()
	for _, field := range []string{identityDomain, r.From, r.Type, r.To, r.Key} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(field)))
		h.Write(n[:])
		h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Held reports the conflicts nobody has answered yet, which is §7.3's rule as
// a question a consumer can ask of a result it is holding.
//
// It is here rather than in pkg/review because it is a classification of a
// declared field and not a step of a review. Every consumer of a Result must
// agree about it or the design's one non-optional refusal is optional after
// all: a job that found a conflict does not finish, whether or not the caller
// asked for review, and a sink that writes a held graph has produced the
// confident wrong answer with a citation that this whole document exists to
// prevent. Four sinks each had to reach for it and each had to import a review
// queue to do so; a fifth that forgets breaks §7.3 and nothing would catch it.
//
// The rule, for a consumer that is not reading Go: a conflict is open when
// neither its left claim's provenance nor its right claim's carries a
// reviewed_by. That is one sentence and it is the whole of it.
//
// Either side is enough. Requiring both would leave a job held by a conflict
// whose losing claim was deleted along with the record that made it — a queue
// nobody could empty.
//
// Violations are deliberately not here. §7.3 puts them on the other side of
// the line: one source saying something the ontology forbids is attributable
// and excludable, and the rest of the graph is usable without it.
func (r Result) Held() []Conflict {
	var open []Conflict
	for _, c := range r.Conflicts {
		if c.Left.Provenance.ReviewedBy == "" && c.Right.Provenance.ReviewedBy == "" {
			open = append(open, c)
		}
	}
	return open
}

// Derivable is the Counts a result implies, computed from the slices beside
// it, and it is the answer to a consumer that cannot check a claim.
//
// §5 makes this block the obligation that justifies the release: "every
// returned graph is accompanied by the numbers needed to distrust it". The
// numbers arrived as a claim no reader could test, and all four stores wrote
// them down verbatim because there was nothing else to do with them — one of
// them wrote its own tally beside them, because the two could disagree and it
// had no way to say which was right.
//
// Eleven of the thirteen fields are a function of the slices and are filled
// here.
// ChunksEmpty and Dropped are not, by construction: a chunk that produced
// nothing and a record a standing rule removed both leave nothing behind to
// count, which is the whole reason those two numbers are reported at all. They
// are left zero rather than guessed, so a caller comparing this against
// Result.Counts compares the fields that can be compared and nothing else. See
// pkg/preflight, which is where that comparison is written down once instead
// of four times.
//
// It is deliberately not a Verify: a mismatch is a finding about a result, and
// findings in this design are lists somebody can read rather than a boolean.
func (r Result) Derivable() Counts {
	c := Counts{
		Entities:   len(r.Entities),
		Relations:  len(r.Relations),
		Chunks:     len(r.Chunks),
		Vectors:    len(r.Vectors),
		Violations: len(r.Violations),
		Conflicts:  len(r.Conflicts),
		Duplicates: len(r.Duplicates),
		Guesses:    len(r.Guesses),
		// ChunksUnread is the length of Unread rather than a second tally, so
		// the number and the list a reader checks it against are one fact.
		ChunksUnread: len(r.Unread),
	}
	for _, rel := range r.Relations {
		if rel.Provenance.Producer.Deterministic() {
			c.Deterministic++
		}
	}
	// Inferred is the remainder rather than a second tally, so that §5b's
	// 890 + 290 = 1180 is arithmetic rather than a coincidence two counters
	// have to keep agreeing on.
	c.Inferred = c.Relations - c.Deterministic
	return c
}

// Fingerprint is deliberately absent, and this comment is the decision rather
// than an oversight, because two of the four stores wrote one independently
// and a third had no choice but to derive an id from something.
//
// A content fingerprint over a Result answers "are these the same bytes",
// which is not the question anybody asked. The question is "is this the same
// run", and the producer already knows the answer: it is Result.Job, which the
// service had all along and dropped on the floor. A fingerprint is what a sink
// reaches for when the producer refuses to say.
//
// And a digest over the whole struct is an identity that changes whenever the
// struct changes. This type has grown Duplicates, RuleSets, RuledBy and Job
// while the design was being written, and it will grow again; every one of
// those additions would have orphaned every previously-loaded corpus, because
// the same graph would fingerprint differently after an upgrade nobody
// consulted the sink about. That is a worse failure than the one it fixes: it
// is silent, it happens on a version bump rather than on an import, and the
// corpus it orphans is the customer's.
//
// A sink that needs a fixed-width derived id — a Qdrant point ID must be a
// UUID or a uint64, so it must be derived from something — should derive it
// from the pieces that are stable under this type growing a field:
// Result.Job for the run, Entity.ID for a node, Relation.Identity for an edge,
// and Chunk.Index for a span. All four are identities somebody stated rather
// than digests of everything present, so a field added next month leaves every
// loaded corpus exactly where it was.
