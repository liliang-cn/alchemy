package sink

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// digestDomain prefixes every digest so a value computed here can never be
// mistaken for one computed over some other structure that happens to hash the
// same bytes, and so a change to the encoding is told from this one by bumping
// the number rather than by silently returning different digests under the same
// name. It is the discipline pkg/cache's address, pkg/review's set name and
// alchemy.Relation.Identity are all under.
const digestDomain = "alchemy/sink/digest/1"

// Digest is the content address of a whole result: the one answer to "is the
// thing already under this name this graph, or another one".
//
// It replaces four. Two connectors demanded a run ID from their caller and then
// wrote their own digest to compare against; two wrote a domain-separated
// SHA-256 over json.Marshal of the result. All four were solving the same
// problem — alchemy.Entity.ID is stable within one result and says nothing
// across runs, so nothing in a graph says whether two of them are the same
// import — and none of them could have known the others existed.
//
// Two properties, and both were split between the four:
//
// It is order-independent. The per-record lines are sorted, so a result
// reassembled from §8.4's pages in a different order than it was produced in is
// the same load. The two connectors that hashed json.Marshal got this wrong and
// one of them argued the point — "a reordered result is a different load either
// way" — which is true of a result somebody edited and false of the one case
// that actually happens, which is a paged stream being put back together.
//
// It covers everything a store writes, which is more than the graph. A result
// whose chunk text changed, or whose counts changed, or whose standing policy
// changed, is a different import even when its entities and edges are identical:
// the store would hold different chunk rows, a different quality report and a
// different policy, and a digest that could not tell would let the second replay
// as the first — leaving the graph carrying a clean bill of health it no longer
// has. One of the four had this right and one was blind to chunks, vectors and
// counts, all three of which it wrote.
//
// A supersession is that same difference in its sharpest form. Two results can
// agree about every entity, edge, chunk and count and disagree only about which
// record is over: the graph is identical and what the store would hold is not,
// and a digest that could not tell would converge the correction onto the thing
// it corrects — writing nothing, and leaving the store holding a fact somebody
// had just said was finished, with no record that anybody said it.
//
// It deliberately does not cover ModelCalls. What a job spent is a fact about
// the job rather than about the graph, two runs that bought the same answer
// from a cache spent differently (§8.2), and a digest that flipped on it would
// refuse the resumed run the cache exists to make cheap.
//
// The algorithm, stated so a consumer in another language can reproduce it:
// build one line per record as below — one per entity, relation, chunk, vector,
// violation, duplicate, guess, unread page, conflict, rule set and supersession,
// plus the single counts line — sort the lines as byte strings, then
// SHA-256 the domain tag followed by every line, each written as an unsigned
// 64-bit big-endian byte length followed by its UTF-8 bytes, and render the
// digest lower-case hex. The framing is length-prefixed rather than delimited
// because a record's fields are arbitrary document text and any separator can
// be forged out of them.
func Digest(res alchemy.Result) string {
	lines := make([]string, 0,
		len(res.Entities)+len(res.Relations)+len(res.Chunks)+len(res.Vectors)+1)

	for _, e := range res.Entities {
		// The aliases are covered because they are a claim about identity that
		// a store writes: a result which learned a node also goes by another
		// name is a different import, and a digest that could not tell would
		// let the second replay as the first with the claim missing.
		//
		// APPENDED, and only when there are any. Inserting a field into this
		// line would have re-addressed every entity in every corpus ever
		// loaded — the orphaning alchemy.Fingerprint's comment declined, and
		// the exact failure alchemy.Counts is exposed to. An entity with no
		// aliases produces the line it has always produced, byte for byte.
		//
		// It cannot collide with the shorter form. The longer line is the
		// shorter one plus a NUL and a JSON array; the shorter one ends in a
		// provenance rendered by encoding/json, which cannot contain a raw
		// NUL, so no five-field line is ever a six-field line's prefix in a
		// way that matters.
		line := join("E", e.ID, e.Type, e.Name, canonical(e.Attributes), canonical(e.Provenance))
		if len(e.Aliases) > 0 {
			line = join(line, canonical(e.Aliases))
		}
		lines = append(lines, line)
	}
	for _, r := range res.Relations {
		// The producer's key is in the line because it is what makes two
		// parallel edges two edges (alchemy.Relation.Key), so a schema whose
		// only correction was a constraint name is a different import.
		lines = append(lines, join("R", r.From, r.To, r.Type, r.Key, canonical(r.Attributes), canonical(r.Provenance)))
	}
	for _, c := range res.Chunks {
		lines = append(lines, join("C", strconv.Itoa(c.Index), c.Source, c.Strategy, c.Heading, c.Text))
	}
	for _, v := range res.Vectors {
		lines = append(lines, join("V", strconv.Itoa(v.Chunk), v.Model, canonical(v.Values)))
	}
	for _, v := range res.Violations {
		lines = append(lines, join("X", string(v.Kind), v.Subject, v.Detail, canonical(v.About), canonical(v.Provenance)))
	}
	for _, d := range res.Duplicates {
		lines = append(lines, join("D", string(d.Signal), d.Subject, d.Left.ID, d.Right.ID, d.Detail))
	}
	for _, g := range res.Guesses {
		lines = append(lines, join("G", g.Field, g.ChosenAs, canonical(g.Alternatives), g.Reason, canonical(g.Provenance)))
	}
	for _, u := range res.Unread {
		lines = append(lines, join("U", u.Source, u.Locator, u.Reason))
	}
	for _, c := range res.Conflicts {
		lines = append(lines, join("F", string(c.Kind), c.Subject, c.Detail, canonical(c.Left), canonical(c.Right)))
	}
	for _, s := range res.RuleSets {
		lines = append(lines, join("S", s.Name, canonical(s.Rules)))
	}
	for _, s := range res.Supersessions {
		// P rather than S, which the rule set has. The whole claim is on the
		// line — what is retired, what replaces it, why, and who says so —
		// because a store writes all four, and two corrections retiring one
		// record for two reasons on two people's word are two statements a
		// reader has to be able to tell apart.
		lines = append(lines, join("P", s.Retires, canonical(s.By), s.Reason, canonical(s.Provenance)))
	}
	// The counts are one line rather than one per field, because they are one
	// claim: §5's block is read whole and a store writes it whole.
	lines = append(lines, join("N", canonical(res.Counts)))

	sort.Strings(lines)
	h := sha256.New()
	write(h, digestDomain)
	for _, l := range lines {
		write(h, l)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// join renders one record as a line. The separator is a NUL, which is safe here
// and would not be at the top level: two records can be rearranged into one
// another's bytes only if the *lines* are concatenated without framing, and
// they are not — Digest length-prefixes each one.
func join(parts ...string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		out += "\x00" + p
	}
	return out
}

// canonical renders a value for hashing. json.Marshal sorts map keys, which is
// what makes an attribute map hash the same twice; a value it cannot handle is
// rendered with its Go syntax rather than dropped, so that two records
// differing only in an unmarshallable field still differ here. pkg/preflight
// refuses those values anyway, and a digest that silently agreed about two
// results it could not read would be the wrong direction to fail in.
func canonical(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}

func write(h interface{ Write([]byte) (int, error) }, field string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(field)))
	h.Write(n[:])
	h.Write([]byte(field))
}
