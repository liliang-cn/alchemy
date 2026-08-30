package verify

import (
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// duplicates finds pairs of nodes that may be one node.
//
// It is here rather than in pkg/extract for §8.1's reason: an extractor sees
// one source, and two spellings of one thing arrive from two sources as
// readily as from two chunks of one. This stage is the first that holds the
// whole job, so it is the first that can notice.
//
// Nothing it finds is resolved. §2.1's second lesson is the whole constraint —
// a rule that stripped a trailing type word would turn "document package" into
// "document" correctly and "language model" into "language" catastrophically,
// and both would run just as cleanly. So the evidence is reported with both
// nodes intact and a person or a written rule decides. §5b: a node that
// silently absorbed another explains nothing.
//
// The signals, and what each will wrongly join:
//
//	name_affix — under one type, one folded name is the other with whole words
//	added at the front or the back. It fires on "document"/"document package"
//	and "SQL"/"SQL dumps", which is the shape a per-chunk extractor produces
//	because each chunk is a separate call that cannot see how the others named
//	the thing. It will wrongly join a name that was qualified because it means
//	something narrower — "language"/"language model", "Ada"/"Ada Lovelace",
//	"order"/"order line" — which is exactly why it reports rather than merges.
//
//	name_across_producers — under one type, one folded name under two ids that
//	two different producers made. It is the narrow half of the third rejected
//	signal below, and the producers are what narrows it: an extractor's two
//	spellings are name_affix's business and a schema's two same-named tables
//	are nobody's, so what is left is an id somebody else chose meeting an id
//	this pipeline minted. It will wrongly join two things that two sources
//	happened to name alike — two customers' files each holding an "Acme" is
//	one name and two companies — which is again why it reports rather than
//	merges.
//
// Three signals were considered and rejected, and the reasons are worth
// keeping because each looks obviously right until it is aimed at real output.
// The third is rejected in the shape it was proposed in and implemented in a
// narrower one, and the argument is what draws the line between them:
//
//   - Identical edge neighbourhood — same type, same edges to the same targets.
//     Under a four-type ontology, siblings all have the same neighbourhood: a
//     "Model language model" and a "Model embedding model" that are both
//     PART_OF one System and nothing else are indistinguishable to it, and it
//     would propose merging the two things a reader most needs kept apart.
//     Every corpus with a taxonomy in it is full of them.
//
//   - Identical attributes — same type, same non-empty attribute map. Prose
//     extraction produces sparse, repetitive attributes: every entity out of
//     one section carries {"section": "2"}, so this joins a section rather than
//     a thing. It also finds none of the real cases, which came from different
//     chunks that stated different things.
//
//   - Same name under one type with different ids, whoever made them. Two ids
//     for one folded name is the strongest evidence there is, and half of the
//     argument against it has held up and half has not.
//
//     The half that holds is the one that rejects it: it fires on exactly what
//     a schema deterministically stated — public.users and audit.users are two
//     tables a CREATE TABLE declared, both named "users" — and asking a person
//     to confirm they are two is §5c's last row, the one it says teaches people
//     to click Approve without reading. Nothing about that has changed and this
//     file must never ask it.
//
//     The half that does not is "it fires on nothing an extractor can produce,
//     because entityID is a function of type and name". That is true of the ids
//     this pipeline mints and false of the ids a source supplies: a graph
//     import brings the document's own ids and an assertion brings the
//     asserter's, so one company arrives as `org:northgate` from one and
//     `organization:northgate` from the other, and no signal in this file could
//     see it. Requiring two producers keeps both halves — the two `users`
//     tables came from one DDL reader and do not fire, and neither do two
//     spellings out of one per-chunk extractor, which is what name_affix is
//     for. See alchemy.DuplicateNameAcrossProducers.
//
// Also rejected: any measure of lexical similarity — edit distance, stemming,
// a shared prefix of characters rather than words. "gpt-4" and "gpt-3" are one
// character apart and are two models; "extract" and "extracts" and "extractor"
// need a threshold nobody can defend, and a threshold is §2.1's guess that does
// not announce itself wearing a number.
//
// Not built, and deliberately: asking the model whether a pair is one thing.
//
// It is the obvious next step and it is a bad trade three times over. The model
// being asked is the model that produced the pair — it was already told to use
// one spelling and could not, because a chunk is a separate call that cannot
// see the others, and asking it again about two names with the chunks gone
// gives it less to go on than it had the first time. What comes back is
// inference, so it would have to arrive as an alchemy.Guess with alternatives
// and a confidence, and a guess about identity that anything acts on
// automatically is §2.1's silent merge with a number attached; a guess nothing
// acts on has bought a call and a line in ModelCalls to produce the pair this
// function already produced for free. And the cost lands exactly where §8 says
// it hurts: one call per candidate, on a corpus where one node in six was a
// candidate, to answer a class of question a person answers once per pair with
// `always`. An unbuilt option is cheaper than a half-built one. If it is ever
// wanted, the shape is settled — a Guess, off by default, counted in
// ModelCalls under its own stage — and nothing here has to move to allow it.
//
// The scan is keyed rather than pairwise. §8.1 names the O(n²) version as the
// one that looks fine and dies at volume: each entity looks up the handful of
// whole-word affixes of its own name, so a job costs its total word count and
// not the square of its node count.
func duplicates(entities []alchemy.Entity) []alchemy.Duplicate {
	// First writer wins per key, the way canonicalise and the conflict slots
	// already resolve it. A corpus that somehow holds a thousand nodes under
	// one name asks one question about each newcomer, not half a million.
	first := make(map[string]int, len(entities))
	for i, e := range entities {
		k := nameKey(e)
		if _, seen := first[k]; !seen {
			first[k] = i
		}
	}

	var out []alchemy.Duplicate
	// Reported once per pair. A stem that is both the prefix and the suffix of
	// the other name — "package document package" — would otherwise arrive as
	// the same question twice.
	said := make(map[string]bool)
	for _, e := range entities {
		typ := foldKey(e.Type)
		// The exact name is looked up in the same map the affixes are, because
		// the two signals differ in what they compare and not in how they find
		// it: one folding, one index, one first-writer-wins rule. A second fold
		// written for this would be the copy that drifts.
		//
		// A name nobody stated is not a name two nodes share. The affix side
		// never had to say so — a nameless entity has no affixes to look up —
		// but an equality test reads two blanks as a match and would ask about
		// every pair of unnamed nodes two producers happened to type the same.
		if name := foldKey(e.Name); name != "" {
			// Two ids under one name are two nodes only if two producers made
			// them: one producer stating a name twice is either a schema that
			// meant two things — public.users and audit.users — or a chunked
			// extractor, which is name_affix's business below.
			if j, ok := first[typ+"\x00"+name]; ok {
				held := entities[j]
				if held.ID != e.ID && held.Provenance.Producer != e.Provenance.Producer && !said[held.ID+"\x00"+e.ID] {
					said[held.ID+"\x00"+e.ID] = true
					out = append(out, sameName(held, e))
				}
			}
		}
		for _, stem := range affixes(foldKey(e.Name)) {
			j, ok := first[typ+"\x00"+stem]
			if !ok {
				continue
			}
			short := entities[j]
			// The same id is already one node, whatever two records call it;
			// two records under one id disagreeing about a name is a conflict
			// and is reported as one.
			if short.ID == e.ID || said[short.ID+"\x00"+e.ID] {
				continue
			}
			said[short.ID+"\x00"+e.ID] = true
			out = append(out, candidate(short, e, stem))
		}
	}
	return out
}

// affixes lists the proper whole-word prefixes and suffixes of a folded name,
// longest first within each family.
//
// Whole words, and only at the ends. A match in the middle would mean words
// were added on both sides, which is a rewrite rather than a qualification —
// "core" inside "core dump analysis tool" is a different thing, not the same
// thing said longer — and admitting it widens the signal by far more than it
// finds. A match on a character boundary rather than a word boundary would
// join "Model gpt-4" to "Model gpt-40", which is the failure this whole
// finding exists to avoid making silently.
func affixes(name string) []string {
	words := strings.Fields(name)
	if len(words) < 2 {
		// A one-word name has no proper affix. It can still be the short side
		// of a pair; the longer name is what looks it up.
		return nil
	}
	out := make([]string, 0, 2*(len(words)-1))
	for i := len(words) - 1; i > 0; i-- {
		out = append(out, strings.Join(words[:i], " "))
	}
	for i := 1; i < len(words); i++ {
		out = append(out, strings.Join(words[i:], " "))
	}
	return out
}

// nameKey is the type and name a duplicate is looked up by. It is folded the
// way an extracted id is, so that "SQL" and "sql" are one stem here as they
// are one node there; it is built for comparison only and is never written
// into the graph, which is what keeps this package from owning another's
// identity scheme.
func nameKey(e alchemy.Entity) string {
	return foldKey(e.Type) + "\x00" + foldKey(e.Name)
}

func foldKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// candidate renders the pair in the words of the person who has to answer it:
// which two nodes, what each chunk called it, and what the extra words were.
func candidate(short, long alchemy.Entity, stem string) alchemy.Duplicate {
	return alchemy.Duplicate{
		Signal:  alchemy.DuplicateNameAffix,
		Subject: short.ID + " ~ " + long.ID,
		Detail: fmt.Sprintf("%s %q per %s and %s %q per %s differ only by %s; they may be one node, and nothing joined them",
			short.Type, short.Name, where(short.Provenance),
			long.Type, long.Name, where(long.Provenance),
			addedWords(foldKey(long.Name), stem)),
		Left:  side(short),
		Right: side(long),
	}
}

// sameName renders the pair the way the person who has to answer it will read
// it: one name, one type, two ids, and which producer chose each — because the
// producers are the whole of why this is a question, and knowing that one id
// came out of somebody else's graph is what tells a reviewer which of the two
// the rest of their corpus points at.
func sameName(held, later alchemy.Entity) alchemy.Duplicate {
	return alchemy.Duplicate{
		Signal:  alchemy.DuplicateNameAcrossProducers,
		Subject: held.ID + " ~ " + later.ID,
		Detail: fmt.Sprintf("%s %q is %q per %s and %q per %s; one name under one type with two ids because two producers made them, and nothing joined them",
			held.Type, held.Name, held.ID, where(held.Provenance), later.ID, where(later.Provenance)),
		Left:  side(held),
		Right: side(later),
	}
}

// addedWords says what the longer name has that the shorter does not, and at
// which end. The end is half the answer: a type word on the end is the mistake
// a chunked extractor makes, and a qualifier on the front is very often a
// second thing.
func addedWords(long, stem string) string {
	if extra, cut := strings.CutPrefix(long, stem+" "); cut {
		return fmt.Sprintf("the trailing %q", extra)
	}
	extra := strings.TrimSuffix(long, " "+stem)
	return fmt.Sprintf("the leading %q", extra)
}

func side(e alchemy.Entity) alchemy.DuplicateSide {
	return alchemy.DuplicateSide{ID: e.ID, Type: e.Type, Name: e.Name, Provenance: e.Provenance}
}
