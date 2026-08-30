package sink_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// The rest of this package's tests check Digest against itself: that it does
// not depend on arrival order, that it covers everything a store writes, that
// two different results differ. Every one of them would still pass if the
// encoding changed, because both sides of the comparison would change
// together. So would connectors/internal/sinkconform, which asks four stores
// to agree with each other — four stores computing the same wrong address
// agree perfectly.
//
// A consistency check cannot detect a consistent error. What follows is the
// other kind: the expected value did not come from running this code.

// TestTheDigestOfAKnownResultIsAFixedValue pins the digest of one small result
// as a literal computed outside Go.
//
// Digest's own comment ends "stated so a consumer in another language can
// reproduce it", and until now nothing tested that promise — the prose could
// have described a different algorithm from the one below it and every test in
// this package would still have been green. The constant was produced by an
// independent implementation of that paragraph, in Python, reading only the
// comment:
//
//	def framed(*fields):
//	    h = hashlib.sha256()
//	    for f in fields:
//	        b = f.encode('utf-8')
//	        h.update(struct.pack('>Q', len(b)))
//	        h.update(b)
//	    return h.hexdigest()
//
//	lines = ["E\0n:alpha\0Node\0alpha\0null\0" + prov,
//	         "R\0n:alpha\0n:beta\0CONNECTS\0fk_alpha_beta\0null\0" + prov,
//	         "N\0" + counts]
//	lines.sort(key=lambda s: s.encode('utf-8'))
//	framed("alchemy/sink/digest/1", *lines)
//
// where prov and counts are the JSON below, written out by hand from the
// struct tags rather than marshalled. That it matches is evidence the digest
// is a property of the specification and not of this process: a map iteration
// order, a Go-specific float rendering, or a sort that compared runes instead
// of bytes would all pass every other test in this file and fail this one.
//
// WHEN THIS FAILS. Two of the inputs are not obviously inputs.
//
// The canonical() rendering of Provenance and Counts is json.Marshal of the
// struct, so a field ADDED to alchemy.Counts changes this value — and with it
// the digest of every result ever computed, because Counts has twelve fields
// without omitempty and is hashed whole. That is precisely the orphaning that
// alchemy.Fingerprint's comment declined to accept ("every one of those
// additions would have orphaned every previously-loaded corpus"), and Digest
// is exposed to it through this one line. It is not wrong — a result whose
// counts changed is a different import — but it is the reason a Counts field
// is not a free addition, and this test is where that gets noticed.
//
// A deliberate encoding change is made by bumping digestDomain and this
// constant together, never by editing the constant alone.
func TestTheDigestOfAKnownResultIsAFixedValue(t *testing.T) {
	prov := alchemy.Provenance{Source: "a.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	res := alchemy.Result{
		Entities: []alchemy.Entity{{
			ID: "n:alpha", Type: "Node", Name: "alpha", Provenance: prov,
		}},
		Relations: []alchemy.Relation{{
			From: "n:alpha", To: "n:beta", Type: "CONNECTS",
			Key: "fk_alpha_beta", Provenance: prov,
		}},
		Counts: alchemy.Counts{Entities: 1, Relations: 1, Deterministic: 1},
	}

	const want = "f39d6669f9cd3bb0de8ce39988a2ebb77d54a2f48ac6348f51506153213b3458"
	if got := sink.Digest(res); got != want {
		t.Fatalf("Digest of the pinned result = %s, want %s\n"+
			"The constant was computed outside Go from Digest's own comment. A mismatch means "+
			"one of three things: the encoding changed (bump digestDomain with it), a field was "+
			"added to alchemy.Counts or alchemy.Provenance (which re-addresses every result ever "+
			"loaded), or the comment and the code have drifted apart and a consumer in another "+
			"language is now computing a different answer from the documented one.", got, want)
	}
}
