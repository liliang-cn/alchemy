package review

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// This file is how a rule, and a set of rules, is referred to from outside
// this package — in a record's provenance, in a result's own account of what
// the model was told, and by a person comparing two runs.
//
// It exists because the obvious answer does not survive volume. A record used
// to carry the shapes of every rule in force, spelled out; that is a correct
// answer to "what had the model been told when this was proposed" and an
// untenable one, because the answer is identical on every record and §8's
// import has four hundred thousand of them. So a record carries a name and the
// result carries the set once.

// Name is the rule's identity as a reader meets it: its origin, then its shape.
//
// The shape is the rule's identity to this package — Covers is equality on it,
// so two rules with one shape answer the same class — and the origin is the
// warrant. They are prefixed rather than kept apart because the two are
// different claims about the same suppression and this string is where a
// reader meets them. "reviewed" says a person looked at this exact finding and
// generalised from it; "authored" says a person declared it in advance, having
// seen no instance at all. The second is the weaker warrant, and a name that
// rendered them identically would let it be read as the stronger.
//
// Both are marked rather than only the new one. A scheme where silence meant
// "reviewed" would make an authored rule that lost its marker indistinguishable
// from a reviewer's — the failure this naming exists to prevent — and it is the
// same argument this package makes for not treating a confidence of zero as a
// model expressing doubt. An unprefixed entry in an old graph is then visibly
// an older run rather than a claim about who decided.
func (r Rule) Name() string {
	if r.Shape == "" {
		return ""
	}
	return string(r.origin()) + ":" + r.Shape
}

// origin spells out the origin a rule carries, including the zero value.
// Origin's zero is a reviewer's rule — every rule that could exist before the
// field did was minted from a decision — and writing that out here is what
// keeps a rendered name from having a meaning nobody stated.
func (r Rule) origin() Origin {
	if r.Authored() {
		return OriginAuthored
	}
	return OriginReviewed
}

// Told is this rule as a sentence the model is shown, and as a later reader
// reads it back.
//
// Because is the sentence the reviewer was looking at when they decided, which
// §5c put on the rule precisely so it could be read after the item it came from
// is gone. It is what a model needs too: "entity W1 has type Widget, which
// sds@3 does not declare" says more about what to stop doing than any
// paraphrase of the shape string would.
func (r Rule) Told() string {
	parts := []string{r.Because}
	// A rejection is the one standing answer whose sentence does not say what
	// to do. "--flag is a switch, not an entity" is a reason; the instruction
	// that follows from it — stop proposing these — is what the model needs,
	// and leaving it to be inferred from a reason wastes the cheap half of
	// §6's promise. The filter still runs either way; telling is the nudge and
	// not believing it is the guarantee.
	if r.From.Verb == VerbReject {
		parts = append(parts, "do not propose these at all")
	}
	if ed := r.From.Edit; ed != nil {
		if ed.Type != "" {
			parts = append(parts, fmt.Sprintf("use the type %q for these instead", ed.Type))
		}
		if ed.Name != "" {
			parts = append(parts, fmt.Sprintf("write the name %q for these instead", ed.Name))
		}
	}
	if r.From.Note != "" {
		parts = append(parts, r.From.Note)
	}
	if r.From.By != "" {
		// "declared by" rather than "decided by" for an authored rule, because
		// the model is being told the truth about the sentence's standing: one
		// came from somebody reading this corpus, the other from somebody
		// stating policy about corpora like it.
		parts = append(parts, r.verbOfOrigin()+" by "+r.From.By)
	}
	return strings.Join(parts, "; ")
}

func (r Rule) verbOfOrigin() string {
	if r.Authored() {
		return "declared"
	}
	return "decided"
}

// InForce is the set of standing rules as a result records it: every rule
// named, with the sentence the model was shown for it, and the whole set
// named once.
//
// One function rather than three because the three answers have to agree.
// extract.Settled makes the same argument for the same reason — everything
// about one chunk has to be decided from one reading, or a chunk ends up whose
// provenance names a policy its prompt never carried. Here it is one step
// stronger: the set's name is computed from exactly the members returned
// beside it, so a reader who does not trust the result can recompute the name
// from the list and check.
//
// An empty set is the zero RuleSet, with no name. A digest over nothing would
// be a name, and a name on every record of every unattended import is the
// repetition this file exists to remove, in miniature; more importantly,
// "nobody had decided anything" is a fact a reader needs, and it is spelled by
// saying nothing.
//
// Rules with no shape are dropped rather than named. Covers refuses an empty
// shape, so such a rule matches nothing and would sit in a set claiming a
// chunk was extracted under a policy that never applied.
func InForce(rules []Rule) alchemy.RuleSet {
	members := make([]alchemy.StandingRule, 0, len(rules))
	seen := make(map[string]bool, len(rules))
	for _, r := range rules {
		name := r.Name()
		if name == "" || seen[name] {
			// Two rules with one name are two people who wrote down the same
			// policy, and ruleFor takes the first — so the second adds no
			// coverage and would only make one set look like two.
			continue
		}
		seen[name] = true
		members = append(members, alchemy.StandingRule{Name: name, Told: r.Told()})
	}
	if len(members) == 0 {
		return alchemy.RuleSet{}
	}
	// Sorted, so the name is a property of the set and not of the order the
	// rules were collected in. A job resumed on another node (§8.3) reads the
	// same policy from the same store and must arrive at the same name; the
	// order two nodes happen to assemble it in is not something either of them
	// can promise.
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	return alchemy.RuleSet{Name: setName(members), Rules: members}
}

// setDomain prefixes every rule-set name so that a digest computed here can
// never be mistaken for one computed over some other structure that happens to
// hash the same bytes, and so that a future encoding change can be told from
// this one by bumping the number rather than by silently returning different
// names under the same scheme. It is pkg/cache's addressDomain, doing the same
// job one package over.
const setDomain = "alchemy/review/ruleset/1"

// setNameLength is how much of the digest a name carries, in hex characters.
//
// Sixty-four bits, because the field it lands in is on every record of a
// four-hundred-thousand-record import and the full digest would be four times
// the size for a distinction nobody can use: what a name has to separate is
// the handful of policies one job runs under, and two of those colliding is
// not a thing that happens. The bytes saved are the entire point of naming a
// set instead of listing it, so spending them on headroom against a birthday
// bound that would need billions of policies would be spending them on nothing.
const setNameLength = 16

// setName is the digest, computed the way pkg/cache computes an address and
// for the same reason.
//
// The framing is length-prefixed rather than concatenated or delimited. A
// digest over name+told+name+told cannot tell {"a","bc"} from {"ab","c"} — the
// same bytes reach the hash — so two different policies would share a name and
// two records asked under different rules would become indistinguishable,
// which is the one property this replacement is not allowed to lose. A
// delimiter only moves the problem to whatever byte was chosen, and a rule's
// sentence is prose somebody typed. A length prefix is unforgeable because the
// boundaries are stated, not inferred.
//
// The digest is SHA-256 and the lengths are big-endian through
// encoding/binary. Both are byte-exact specifications, so the name a node
// computes on arm64 today equals the one another node computes on amd64
// tomorrow — which is the entire point when one job can be taken over by
// another node (§8.3). Go's map hash and maphash are process-seeded, and a
// pointer or a fmt of a struct is neither stable nor meaningful across
// processes; any of them would make two halves of one clustered job look like
// two different policies.
func setName(members []alchemy.StandingRule) string {
	h := sha256.New()
	// The domain tag is framed too, so it cannot be absorbed into the first
	// field by a rule whose name begins with the same text.
	write(h, setDomain)
	for _, m := range members {
		write(h, m.Name)
		write(h, m.Told)
	}
	return hex.EncodeToString(h.Sum(nil))[:setNameLength]
}

func write(h interface{ Write([]byte) (int, error) }, field string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(field)))
	h.Write(n[:])
	h.Write([]byte(field))
}
