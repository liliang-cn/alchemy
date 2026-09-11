// Package recallconform is the suite every recall.Reader passes, including
// yours.
//
// WRITING A CONNECTOR'S READ SIDE. Implement recall.Reader beside your
// sink.Sink, then add one test:
//
//	func TestRecallConformance(t *testing.T) {
//		recallconform.Run(t, func(t *testing.T) recallconform.Store {
//			return NewMyStore(t)          // a store nobody else is using
//		})
//	}
//
// The suite loads one fixture through your sink and then asks the eight
// primitives about it, so it tests the round trip rather than a reader over a
// graph somebody else wrote. The factory is called once per case and must hand
// back a store the case can have to itself.
//
// WHY A SUITE AND NOT THE INTERFACE'S DOC COMMENTS. Every case below is a wrong
// answer that an agent actually gave, in this repository, before the primitive
// existed or before it behaved this way:
//
//   - A page that did not say it was a page. An anchor search matched fourteen
//     entities and returned twelve; the one the question was about was
//     thirteenth. Two agent runtimes were handed the truncated list with no
//     sign it was one, and in seven runs of eight went on to invent an id.
//   - "all" as a sentinel. Unanswered takes an empty string as everything, and
//     a tool description told a model to pass "all". Across thirty runs it
//     passed "all" twenty-nine times, was told nothing matched, and wrote
//     "there are no unresolved identity questions" while the store held
//     thirteen.
//   - A chunk-less record refused as a broken citation. DESIGN.md §5b ranks a
//     machine reading something that already asserted a fact ABOVE a model
//     reading prose, so those are the strongest records in the store — and
//     seven of thirteen citation attempts were refused with the sentence
//     reserved for a citation that does not resolve.
//   - A claim rendered with names the caller could not walk from. An agent
//     spent eight of thirteen calls turning a name it had just been given back
//     into the id the next call needed.
//   - A node two sources had silently been merged into, reported as though one
//     document described it. Six runs out of six stated it as settled.
//
// None of those is a bug the compiler can find, and none would be caught by
// reading recall.Reader's comments. That is what this suite is for.
//
// See also sinkconform, the write side, and contributions, the fold
// Contributions must use.
package recallconform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// Store is a connector that both writes and reads. A store that only writes has
// sinkconform; this suite needs both halves because it checks a round trip.
type Store interface {
	sink.Sink
	recall.Reader
}

// Run asks the eight primitives about one loaded graph.
func Run(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			// A fresh load name per run, not one derived from the case.
			//
			// A stable name is unique only where the factory hands back a store
			// nobody else has — a private database, a private prefix — and not
			// every store can offer one: a server that holds a single dataset
			// makes the second run meet its own first load and be refused with
			// "already written from a different result", which is the connector
			// doing exactly what it promises. Every load in production has a
			// name of its own, so the suite gives itself one too.
			load := "rc-" + c.name + "-" + stamp()
			if _, err := sink.Load(context.Background(), s, fixture(), sink.Options{Load: load}); err != nil {
				t.Fatalf("loading the fixture: %v", err)
			}
			c.check(t, s, load)
		})
	}
}

// stamp is eight random hex characters, enough that two runs against one store
// do not collide and short enough to read in a failure message.
func stamp() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A clock is a poor unique name and a good fallback: this only runs
		// when the OS refuses entropy, and a test that stopped there would be
		// failing for a reason that is not the store's.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

type testCase struct {
	name  string
	check func(t *testing.T, s Store, load string)
}

// fixture is the graph every case below asks about.
//
// Each part of it is here because a case needs it, and the shapes matter more
// than the names:
//
//   - Two entities of one type and one of another, so Types has something to
//     group and OfType has something to exclude.
//   - Names that differ in case from what a search will ask for, and one name
//     that CONTAINS another's search term in the middle rather than at the
//     start, so a prefix match cannot pass for a substring match.
//   - An entity whose attributes are not strings, so a store that can only hold
//     text has to say what it did.
//   - Two relations with DIFFERENT provenance from the nodes they touch, and in
//     opposite directions through one node, so a walk that reported the node's
//     provenance or dropped a direction fails.
//   - One record from a producer that works in chunks and one from a producer
//     that does not, so Cite has both of its answers to give.
//   - A duplicate finding, which is the only finding recall reads back.
func fixture() alchemy.Result {
	prose := alchemy.Provenance{
		Source: "profile.pdf", Chunk: 20, Producer: alchemy.ProducerLLMExtract,
		Model: "m-1", Ontology: "org@1", Chunking: "heading", Confidence: 0.81,
		ReviewedBy: "ada@example.com", RuleSet: "rs-7",
	}
	imported := alchemy.Provenance{
		Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport, Ontology: "org@1",
	}
	return alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "p1", Type: "Person", Name: "Maurice Ravel",
				Aliases:    []string{"M. Ravel"},
				Attributes: map[string]any{"born": 1875.0, "living": false, "city": "Ciboure"},
				Provenance: prose},
			{ID: "c1", Type: "Company", Name: "Halcyon", Provenance: imported},
			{ID: "d1", Type: "Product", Name: "Ledger", Provenance: imported},
		},
		Relations: []alchemy.Relation{
			// Out of p1, and inferred.
			{From: "p1", To: "c1", Type: "WORKS_FOR", Provenance: prose},
			// Into p1, and stated — a different producer from the node's, so a
			// walk that reported the subject's provenance answers wrongly.
			{From: "c1", To: "d1", Type: "DEVELOPS", Provenance: imported},
		},
		Chunks: []alchemy.Chunk{{
			Index: 20, Text: "Maurice Ravel works for Halcyon.", Source: "profile.pdf",
			Strategy: "heading", Heading: "People", Start: 400, End: 432,
		}},
		// NO VECTOR, deliberately.
		//
		// A result carries chunks so that a citation can be resolved, and
		// whether it also carries embeddings is a separate decision — an import
		// meant for citation and review has no reason to pay for them. So a
		// store that can only keep chunk text next to an embedding keeps none
		// of it here, and Cite then answers "this citation does not resolve"
		// about text the result contained and the connector was handed.
		//
		// That is what this suite found on its first run, and the fixture
		// asserts it rather than avoiding it: one store filed the text as a
		// document instead, because neither of its two obvious homes will take
		// a row with no vector and inventing one would put a point at the
		// origin into somebody's similarity search.
		Duplicates: []alchemy.Duplicate{{
			Signal: alchemy.DuplicateNameAffix, Subject: "c1 ~ c2",
			Detail: "Halcyon and Halcyon Systems differ only by a trailing word",
			Left:   alchemy.DuplicateSide{ID: "c1", Type: "Company", Name: "Halcyon", Provenance: imported},
			Right:  alchemy.DuplicateSide{ID: "p1", Type: "Person", Name: "Maurice Ravel", Provenance: prose},
		}},
		Counts: alchemy.Counts{Entities: 3, Relations: 2, Deterministic: 3, Inferred: 2, Duplicates: 1},
	}
}

// ids renders a Found as the ids it holds, for a failure message somebody can
// act on: "got [c1 d1], want [p1]" beats "got 2, want 1".
func ids(f recall.Found) []string {
	out := make([]string, 0, len(f.Nodes))
	for _, n := range f.Nodes {
		out = append(out, n.ID)
	}
	return out
}

func cases() []testCase {
	ctx := context.Background()
	return []testCase{{
		// A SUBSTRING, not a prefix and not a token. "aur" sits inside
		// "Maurice" and matches nothing at a word boundary, so a store that
		// reached for its full-text index instead of a substring scan passes
		// every other case and fails this one.
		name: "find_matches_a_case_insensitive_substring",
		check: func(t *testing.T, s Store, load string) {
			for _, needle := range []string{"ravel", "RAVEL", "aur"} {
				got, err := s.Find(ctx, load, needle, 10)
				if err != nil {
					t.Fatalf("Find(%q): %v", needle, err)
				}
				if got.Total != 1 || len(got.Nodes) != 1 || got.Nodes[0].ID != "p1" {
					t.Errorf("Find(%q) = %v (total %d), want exactly [p1]", needle, ids(got), got.Total)
				}
			}
			if got, err := s.Find(ctx, load, "nobodyhere", 10); err != nil || got.Total != 0 {
				t.Errorf("Find on no match = %v, %v; no match is an empty answer, not an error", ids(got), err)
			}
		},
	}, {
		// The measured one. A page that does not say it is a page asks a reader
		// to trust a list that is not the list, and an agent handed one invents
		// the rest.
		name: "find_reports_what_the_page_left_out",
		check: func(t *testing.T, s Store, load string) {
			got, err := s.Find(ctx, load, "", 2)
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if got.Total != 3 {
				t.Fatalf("Found.Total = %d, want 3: the total is the match set, not the page", got.Total)
			}
			if len(got.Nodes) != 2 || !got.Truncated() {
				t.Fatalf("Find = %v, Truncated=%v; want 2 shown and Truncated", ids(got), got.Truncated())
			}
			// Ordered by name then id, so a limit cuts the same place twice.
			// Halcyon, Ledger, Maurice Ravel.
			if got.Nodes[0].Name != "Halcyon" || got.Nodes[1].Name != "Ledger" {
				t.Errorf("page = %q/%q, want the first two by name",
					got.Nodes[0].Name, got.Nodes[1].Name)
			}
			again, err := s.Find(ctx, load, "", 2)
			if err != nil {
				t.Fatalf("Find again: %v", err)
			}
			for i := range got.Nodes {
				if got.Nodes[i].ID != again.Nodes[i].ID {
					t.Fatalf("row %d was %s then %s; an unordered page cannot be paged through",
						i, got.Nodes[i].ID, again.Nodes[i].ID)
				}
			}
		},
	}, {
		// The vocabulary, and only the entities. A store that counted its own
		// chunk rows or load marker would report a type no ontology declares
		// and a total nobody can reconcile with the graph they were shown.
		name: "types_counts_the_loads_entities_and_nothing_else",
		check: func(t *testing.T, s Store, load string) {
			got, err := s.Types(ctx, load)
			if err != nil {
				t.Fatalf("Types: %v", err)
			}
			want := map[string]int{"Person": 1, "Company": 1, "Product": 1}
			if len(got) != len(want) {
				t.Fatalf("Types = %+v, want %v", got, want)
			}
			for _, tc := range got {
				n, declared := want[tc.Type]
				if !declared {
					t.Errorf("Types reports %q, which no entity in this load carries", tc.Type)
					continue
				}
				if tc.Count != n {
					t.Errorf("Types says %d %s, want %d", tc.Count, tc.Type, n)
				}
			}
			// Ordered by type, so two reads produce the same document.
			for i := 1; i < len(got); i++ {
				if got[i-1].Type > got[i].Type {
					t.Errorf("Types is not ordered by type: %+v", got)
					break
				}
			}
		},
	}, {
		// Exactly, and the contrast with Find is the point: Find takes what
		// somebody typed, this takes what Types returned. A type is declared by
		// an ontology, so folding case here would report a vocabulary the load
		// does not have.
		name: "of_type_is_exact_where_find_is_not",
		check: func(t *testing.T, s Store, load string) {
			got, err := s.OfType(ctx, load, "Person", 10)
			if err != nil {
				t.Fatalf("OfType: %v", err)
			}
			if got.Total != 1 || len(got.Nodes) != 1 || got.Nodes[0].ID != "p1" {
				t.Fatalf("OfType(Person) = %v, want [p1]", ids(got))
			}
			if got.Nodes[0].Type != "Person" || got.Nodes[0].Name != "Maurice Ravel" {
				t.Errorf("anchor = %+v, want the declared type and the name", got.Nodes[0])
			}
			lower, err := s.OfType(ctx, load, "person", 10)
			if err != nil {
				t.Fatalf("OfType lowercase: %v", err)
			}
			if lower.Total != 0 {
				t.Errorf(`OfType("person") matched %d; folding case here reports a vocabulary `+
					`the ontology does not declare`, lower.Total)
			}
		},
	}, {
		// Both directions in one answer, because an agent asking what is known
		// about a thing does not care which way the extractor wrote the edge —
		// and each claim carrying ITS OWN provenance, not its subject's. Those
		// are different sentences in this fixture on purpose.
		name: "claims_come_back_both_ways_with_their_own_provenance",
		check: func(t *testing.T, s Store, load string) {
			out, err := s.Claims(ctx, load, "c1")
			if err != nil {
				t.Fatalf("Claims: %v", err)
			}
			if len(out) != 2 {
				t.Fatalf("Claims about c1 = %d, want 2 (one in, one out): %+v", len(out), out)
			}
			by := map[string]recall.Claim{}
			for _, c := range out {
				by[c.Type] = c
			}
			in, ok := by["WORKS_FOR"]
			if !ok {
				t.Fatalf("the incoming WORKS_FOR edge is missing: %+v", out)
			}
			if in.FromID != "p1" || in.ToID != "c1" {
				t.Errorf("WORKS_FOR = %s -> %s, want p1 -> c1: an incoming edge is the same "+
					"assertion read from the other end, not a reversed one", in.FromID, in.ToID)
			}
			if in.Stated || in.Producer != alchemy.ProducerLLMExtract || in.Chunk != 20 {
				t.Errorf("WORKS_FOR = %+v, want the edge's own inferred provenance at chunk 20", in)
			}
			out2, ok := by["DEVELOPS"]
			if !ok {
				t.Fatalf("the outgoing DEVELOPS edge is missing: %+v", out)
			}
			if !out2.Stated || out2.Producer != alchemy.ProducerGraphImport {
				t.Errorf("DEVELOPS = %+v, want the edge's own stated provenance", out2)
			}
		},
	}, {
		// The ids are what a walk continues with. Measured: without them an
		// agent spent eight of thirteen calls turning a name it had just been
		// given back into the id the next call needs.
		name: "claims_carry_the_ids_a_walk_continues_with",
		check: func(t *testing.T, s Store, load string) {
			first, err := s.Claims(ctx, load, "p1")
			if err != nil {
				t.Fatalf("Claims: %v", err)
			}
			if len(first) == 0 {
				t.Fatal("p1 has an edge; Claims returned none")
			}
			c := first[0]
			if c.From == "" || c.To == "" {
				t.Errorf("claim %+v has an empty endpoint name; the rendering is what a reader weighs", c)
			}
			next := c.ToID
			if next == "p1" {
				next = c.FromID
			}
			if next == "" {
				t.Fatalf("claim %+v carries no id for the other end", c)
			}
			// The id from one call has to work as the argument of the next.
			if _, err := s.Claims(ctx, load, next); err != nil {
				t.Fatalf("walking to %q, the id the last claim gave: %v", next, err)
			}
		},
	}, {
		// The one call that returns a record rather than an answer, and the
		// only way an entity's own fields are reachable at all.
		name: "describe_returns_the_record_whole",
		check: func(t *testing.T, s Store, load string) {
			d, err := s.Describe(ctx, load, "p1")
			if err != nil {
				t.Fatalf("Describe: %v", err)
			}
			if d.ID != "p1" || d.Name != "Maurice Ravel" || d.Type != "Person" {
				t.Fatalf("Describe = %+v", d)
			}
			if len(d.Aliases) != 1 || d.Aliases[0] != "M. Ravel" {
				t.Errorf("Aliases = %v, want the one the source gave", d.Aliases)
			}
			if d.Attributes["city"] != "Ciboure" {
				t.Errorf("Attributes[city] = %#v, want the string the source stated", d.Attributes["city"])
			}
			// The whole provenance, which is the only place these are reachable.
			p := d.Provenance
			if p.Model != "m-1" || p.Confidence != 0.81 || p.ReviewedBy != "ada@example.com" ||
				p.Ontology != "org@1" || p.RuleSet != "rs-7" {
				t.Errorf("Provenance = %+v, want the whole of it", p)
			}
			// A record whose producer worked in no chunks says so rather than
			// citing the first chunk of its file.
			d2, err := s.Describe(ctx, load, "c1")
			if err != nil {
				t.Fatalf("Describe c1: %v", err)
			}
			if d2.Provenance.Chunk != -1 {
				t.Errorf("c1 chunk = %d, want -1: its producer does not work in chunks", d2.Provenance.Chunk)
			}
		},
	}, {
		// A chunk index is unique across a job, so the index alone would
		// resolve — and a caller who passed the wrong file with the right
		// number would be handed the other file's text with nothing about the
		// answer looking wrong.
		name: "cite_wants_both_halves_of_the_marker",
		check: func(t *testing.T, s Store, load string) {
			c, err := s.Cite(ctx, load, "profile.pdf", 20)
			if err != nil {
				t.Fatalf("Cite: %v", err)
			}
			if c.Text != "Maurice Ravel works for Halcyon." {
				t.Errorf("Cite text = %q", c.Text)
			}
			if c.Start != 400 || c.End != 432 {
				t.Errorf("Cite offsets = %d-%d, want 400-432: they are what make it evidence "+
					"rather than a quotation", c.Start, c.End)
			}
			if _, err := s.Cite(ctx, load, "team.json", 20); !errors.Is(err, recall.ErrNoCitation) {
				t.Errorf("Cite with the right index and the wrong file = %v, want ErrNoCitation", err)
			}
		},
	}, {
		// Measured: with only two outcomes, every chunk-less record was refused
		// with the sentence reserved for a citation that does not resolve —
		// seven of thirteen attempts — and an agent was told to distrust the
		// most trustworthy records in the store.
		name: "cite_says_a_chunkless_record_is_not_a_broken_one",
		check: func(t *testing.T, s Store, load string) {
			_, err := s.Cite(ctx, load, "team.json", -1)
			if errors.Is(err, recall.ErrNoCitation) {
				t.Fatalf("Cite(-1) = ErrNoCitation; a record from a producer that does not work "+
					"in chunks is the strongest kind here, not a broken citation: %v", err)
			}
			if !errors.Is(err, recall.ErrNoChunk) {
				t.Fatalf("Cite(-1) = %v, want ErrNoChunk", err)
			}
		},
	}, {
		// The thirty-run defect. An empty about is everything; "all" is a
		// search term like any other, because a sentinel that is also a legal
		// search term is a filter that silently stops filtering for one input.
		name: "unanswered_filters_without_a_sentinel",
		check: func(t *testing.T, s Store, load string) {
			all, err := s.Unanswered(ctx, load, "")
			if err != nil {
				t.Fatalf("Unanswered: %v", err)
			}
			if len(all) != 1 {
				t.Fatalf("Unanswered(\"\") = %d, want the fixture's one duplicate: %+v", len(all), all)
			}
			q := all[0]
			if q.Signal != alchemy.DuplicateNameAffix {
				t.Errorf("Signal = %q, want the signal the finding carried", q.Signal)
			}
			if q.Detail == "" {
				t.Error("Detail is empty; it is what lets a person answer without opening the source")
			}
			lit, err := s.Unanswered(ctx, load, "all")
			if err != nil {
				t.Fatalf("Unanswered(\"all\"): %v", err)
			}
			if len(lit) != 0 {
				t.Errorf(`Unanswered("all") returned %d; "all" is a search term here, not everything`,
					len(lit))
			}
			hit, err := s.Unanswered(ctx, load, "halcyon")
			if err != nil {
				t.Fatalf("Unanswered(\"halcyon\"): %v", err)
			}
			if len(hit) != 1 {
				t.Errorf("Unanswered(\"halcyon\") = %d, want 1: the filter reads both names, "+
					"the subject and the detail", len(hit))
			}
		},
	}, {
		// The Lars defect. The graph reported the join it REFUSED and said
		// nothing about the one it made, so only half of identity was visible —
		// the wrong half, because the other one has already been acted on.
		name: "contributions_see_the_edges_as_well_as_the_node",
		check: func(t *testing.T, s Store, load string) {
			got, err := s.Contributions(ctx, load, "c1")
			if err != nil {
				t.Fatalf("Contributions: %v", err)
			}
			if got.ID != "c1" || got.Type != "Company" {
				t.Fatalf("Contributions = %+v", got)
			}
			// c1's own record came from team.json; the WORKS_FOR edge touching
			// it came from profile.pdf. Two sources had a hand in this node.
			if !got.Joined() {
				t.Fatalf("c1 is named by team.json and touched by an edge from profile.pdf; "+
					"Joined() should say so: %+v", got.Contributors)
			}
			named := 0
			for _, c := range got.Contributors {
				if c.Name != "" {
					named++
				}
			}
			if named != 1 {
				t.Errorf("%d contributors carry a name, want exactly the node's own record. "+
					"Copying the node's name onto every contributor reports that all of them "+
					"agreed on it, which is the one signal this primitive exists to give: %+v",
					named, got.Contributors)
			}
		},
	}, {
		// The asymmetry, and it is deliberate: a load that is not there is the
		// caller naming the wrong import, and an id that is not there is an
		// ordinary answer.
		name: "an_unknown_load_is_told_apart_from_an_empty_answer",
		check: func(t *testing.T, s Store, load string) {
			const gone = "rc-never-ran"
			for name, call := range map[string]func() error{
				"Describe":      func() error { _, err := s.Describe(ctx, gone, "p1"); return err },
				"Contributions": func() error { _, err := s.Contributions(ctx, gone, "p1"); return err },
				"Cite":          func() error { _, err := s.Cite(ctx, gone, "profile.pdf", 20); return err },
			} {
				if err := call(); !errors.Is(err, recall.ErrNoLoad) {
					t.Errorf("%s on an unknown load = %v, want ErrNoLoad", name, err)
				}
			}
			// An id the load does not hold is an ordinary answer, not an error.
			d, err := s.Describe(ctx, load, "no-such-entity")
			if err != nil || d.ID != "" {
				t.Errorf("Describe of an absent id = %+v, %v; want a zero Description and no error", d, err)
			}
			if f, err := s.Find(ctx, gone, "x", 5); err != nil || f.Total != 0 {
				t.Errorf("Find on an unknown load = %v, %v; no match is an empty answer", ids(f), err)
			}
		},
	}}
}
