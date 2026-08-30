package neo4j

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// digest is the identity of everything a Load writes.
//
// It is what makes a second Load under the same RunID answerable: the same
// digest is a replay (of a retry, or of a crash halfway through) and must
// converge on the same graph; a different digest under the same name is the
// caller telling the store two different things about one import, which is
// refused rather than merged. Without it, "load it again" and "load something
// else and call it the same run" are the same call.
//
// It covers provenance as well as content, because §5b's guarantee is about
// attribution: the same edges re-extracted by a different model are not the
// same import, and a run that replayed as unchanged would leave the graph
// claiming the first model's word for the second model's work.
//
// It is order-independent — the per-record lines are sorted — because a result
// that arrived paged (§8.4) can be reassembled in a different order than it
// was produced in, and a run identity that flipped on reassembly would refuse
// every legitimate retry.
func digest(res alchemy.Result) string {
	lines := make([]string, 0, len(res.Entities)+len(res.Relations))
	for _, e := range res.Entities {
		lines = append(lines, "E\x00"+e.ID+"\x00"+e.Type+"\x00"+e.Name+"\x00"+canonical(e.Attributes)+"\x00"+canonical(e.Provenance))
	}
	for _, r := range res.Relations {
		lines = append(lines, "R\x00"+r.From+"\x00"+r.To+"\x00"+r.Type+"\x00"+canonical(r.Attributes)+"\x00"+canonical(r.Provenance))
	}
	// The findings are in the digest because they are loaded (findings.go).
	// Two results that agree about the graph and disagree about what is wrong
	// with it are two different imports, and a run identity that could not
	// tell them apart would let the second one replay as the first — leaving
	// the graph carrying a clean bill of health it no longer has.
	for _, v := range res.Violations {
		lines = append(lines, "V\x00"+string(v.Kind)+"\x00"+v.Subject+"\x00"+v.Detail+"\x00"+canonical(v.Provenance))
	}
	for _, d := range res.Duplicates {
		lines = append(lines, "D\x00"+string(d.Signal)+"\x00"+d.Subject+"\x00"+d.Left.ID+"\x00"+d.Right.ID)
	}
	for _, g := range res.Guesses {
		lines = append(lines, "G\x00"+g.Field+"\x00"+g.ChosenAs+"\x00"+canonical(g.Alternatives)+"\x00"+g.Reason)
	}
	for _, u := range res.Unread {
		lines = append(lines, "U\x00"+u.Source+"\x00"+u.Locator+"\x00"+u.Reason)
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		// Length-prefixed so that two records cannot be rearranged into the
		// same byte stream: without it, a name ending in a separator could
		// impersonate the start of the next field.
		fmt.Fprintf(h, "%d:%s", len(l), l)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// relationKey is the identity of one edge, and it is the identity of the
// *assertion* rather than of the pair it connects.
//
// Two chunks that both say "SuperAI USES CortexDB" are two pieces of evidence
// with two provenances. Merging them onto one edge keyed by (from, type, to)
// would leave the second one unable to name its producer, its chunk or its
// model — which is exactly the guarantee §5b sells. So the key covers the
// attributes and the provenance too: replaying the same result is a no-op, and
// two genuinely separate assertions remain two edges a buyer can count.
func relationKey(r alchemy.Relation) string {
	h := sha256.Sum256([]byte(r.From + "\x00" + r.To + "\x00" + r.Type + "\x00" + canonical(r.Attributes) + "\x00" + canonical(r.Provenance)))
	return hex.EncodeToString(h[:])
}

// canonical renders a value for hashing. json.Marshal sorts map keys, which is
// what makes an Attributes map hash the same twice; a value that cannot be
// marshalled is rendered with its Go syntax rather than dropped, so that two
// records differing only in an unmarshallable field still differ here.
func canonical(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}
