package qdrant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// The eight questions, asked of the store that could only be written to.
//
// A buyer who chose Qdrant got the write side of the product and none of the
// read side, which is where a context pack is built — the reason any of it is
// worth buying. The fixture is the one the other three stores are held to: two
// producers, an edge asserted by a named person on a dated message, a chunkless
// claim, an entity carrying a window in its attributes, and an identity
// question nobody has ruled on.
func readable() alchemy.Result {
	team := alchemy.Provenance{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport, Ontology: "sds@3"}
	prose := alchemy.Provenance{
		Source: "profile.pdf", Chunk: 14, Producer: alchemy.ProducerLLMExtract,
		Model: "gemini-3.6-flash-high", Ontology: "sds@3", Confidence: 0.82,
	}
	said := alchemy.Provenance{
		Source: "slack/#general", Chunk: -1, Producer: alchemy.ProducerHuman,
		By: "joel.c@halcyon.com", At: "2026-08-31T18:35:00Z", Ontology: "sds@3",
	}
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "product:ledger", Type: "Product", Name: "Ledger", Provenance: team},
			{ID: "person:mira", Type: "Person", Name: "Mira", Provenance: prose},
			{ID: "person:nadia", Type: "Person", Name: "Nadia", Provenance: team},
			{ID: "absence:1", Type: "Absence", Name: "Joel C parental leave",
				Aliases: []string{"Joel parental leave"},
				Attributes: map[string]any{
					"from": "2026-10-05", "to": "2026-11-05", "start_confirmed": false,
					"cover": map[string]any{"team": "Ledger"},
				}, Provenance: said},
		},
		Relations: []alchemy.Relation{
			{From: "person:mira", To: "product:ledger", Type: "DEVELOPS", Provenance: team},
			{From: "person:nadia", To: "product:ledger", Type: "DEVELOPS", Provenance: prose},
			{From: "absence:1", To: "person:mira", Type: "ABSENCE_OF", Provenance: said},
		},
		Chunks: []alchemy.Chunk{
			{Index: 14, Source: "profile.pdf", Text: "Mira works on Ledger.", Strategy: "heading", Start: 100, End: 119},
		},
		Duplicates: []alchemy.Duplicate{{
			Signal: alchemy.DuplicateNameAffix, Subject: "Nadia ~ Nadia Okonkwo",
			Detail: "one name is the other with a word added",
			Left:   alchemy.DuplicateSide{ID: "person:nadia", Type: "Person", Name: "Nadia", Provenance: team},
			Right:  alchemy.DuplicateSide{ID: "person:pr", Type: "Person", Name: "Nadia Okonkwo", Provenance: prose},
		}},
	}
	res.Counts = res.Derivable()
	return res
}

func loadedForRead(t *testing.T) (*Loader, string) {
	t.Helper()
	f := newFixture(t)
	l := f.open(t, Config{})
	if _, err := l.Load(context.Background(), readable(), LoadOptions{ID: "rd"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	return l, "rd"
}

func TestTheAnchorSearchIsASubstringMatchAndSaysHowManyMatched(t *testing.T) {
	l, load := loadedForRead(t)
	ctx := context.Background()

	// Case-insensitive substring, which is what Qdrant has no condition for and
	// why this one is a scan: neither whole-value nor full-text token matching
	// answers "contains this text".
	found, err := l.Find(ctx, load, "LAR", 10)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(found.Nodes) != 1 || found.Nodes[0].ID != "person:mira" {
		t.Fatalf("Find(%q) = %+v, want the one name containing it", "LAR", found.Nodes)
	}
	if found.Nodes[0].Type != "Person" || found.Nodes[0].Name != "Mira" {
		t.Errorf("the anchor lost its type or name: %+v", found.Nodes[0])
	}
	// A page says it is one.
	small, err := l.Find(ctx, load, "", 2)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if small.Total != 4 || len(small.Nodes) != 2 || !small.Truncated() {
		t.Errorf("Find with a limit of 2 = %d of %d, truncated %v", len(small.Nodes), small.Total, small.Truncated())
	}
	if _, err := l.Find(ctx, load, "x", 0); err == nil {
		t.Error("Find with limit 0 succeeded; there is no everything value")
	}
	// A load that is not here is an ordinary empty answer for this one.
	if got, err := l.Find(ctx, "no-such-load", "Mira", 5); err != nil || len(got.Nodes) != 0 {
		t.Errorf("Find in an absent load = %+v, %v; want an empty page and no error", got, err)
	}
}

func TestTheVocabularyAndOneClassAgree(t *testing.T) {
	l, load := loadedForRead(t)
	ctx := context.Background()

	types, err := l.Types(ctx, load)
	if err != nil {
		t.Fatalf("Types: %v", err)
	}
	total := 0
	for i, tc := range types {
		if i > 0 && types[i-1].Type >= tc.Type {
			t.Fatalf("types are not ordered: %v", types)
		}
		total += tc.Count
		got, err := l.OfType(ctx, load, tc.Type, tc.Count)
		if err != nil {
			t.Fatalf("OfType(%q): %v", tc.Type, err)
		}
		if got.Total != tc.Count || len(got.Nodes) != tc.Count {
			t.Errorf("OfType(%q) returned %d of %d, Types said %d", tc.Type, len(got.Nodes), got.Total, tc.Count)
		}
		for _, n := range got.Nodes {
			if n.Type != tc.Type {
				t.Errorf("OfType(%q) returned a %s; a type is matched exactly", tc.Type, n.Type)
			}
		}
	}
	if total != 4 {
		t.Errorf("the types sum to %d, want the 4 entities loaded", total)
	}
	// Not folded: a type is declared by an ontology, not typed by a person.
	if got, err := l.OfType(ctx, load, "person", 10); err != nil || len(got.Nodes) != 0 {
		t.Errorf("OfType(%q) = %+v, %v; want nothing", "person", got, err)
	}
}

func TestAWalkCarriesTheEdgesOwnProvenanceAndTheIDsToContinueWith(t *testing.T) {
	l, load := loadedForRead(t)
	ctx := context.Background()

	claims, err := l.Claims(ctx, load, "person:mira")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("Claims about Mira = %v, want the edge out and the edge in", claims)
	}
	var absence, develops recall.Claim
	for _, c := range claims {
		switch c.Type {
		case "ABSENCE_OF":
			absence = c
		case "DEVELOPS":
			develops = c
		}
	}
	// The provenance of the EDGE, not of its subject: Mira was named by
	// profile.pdf and this edge was asserted in Slack by a named person.
	if absence.Source != "slack/#general" || absence.By != "joel.c@halcyon.com" {
		t.Errorf("the edge carries %+v, want its own asserter", absence)
	}
	if absence.At != "2026-08-31T18:35:00Z" {
		t.Errorf("the assertion date did not survive: %+v", absence)
	}
	if !develops.Stated || develops.Producer != alchemy.ProducerGraphImport {
		t.Errorf("stated/inferred is wrong on %+v", develops)
	}
	// Names to read it in, ids to walk it by.
	if develops.From != "Mira" || develops.To != "Ledger" {
		t.Errorf("the claim does not read as a sentence: %s", develops)
	}
	if develops.FromID != "person:mira" || develops.ToID != "product:ledger" {
		t.Errorf("the claim carries no ids to continue from: %+v", develops)
	}
	onward, err := l.Claims(ctx, load, develops.ToID)
	if err != nil || len(onward) == 0 {
		t.Errorf("Claims(%q) from the id the first hop gave = %v, %v", develops.ToID, onward, err)
	}
}

func TestEverythingASourceSaidAboutANodeComesBack(t *testing.T) {
	l, load := loadedForRead(t)
	ctx := context.Background()

	got, err := l.Describe(ctx, load, "absence:1")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	for k, want := range map[string]any{"from": "2026-10-05", "to": "2026-11-05", "start_confirmed": false} {
		if got.Attributes[k] != want {
			t.Errorf("attribute %q = %#v, want %#v", k, got.Attributes[k], want)
		}
	}
	if nested, _ := got.Attributes["cover"].(map[string]any); nested["team"] != "Ledger" {
		t.Errorf("a nested attribute came back as %#v", got.Attributes["cover"])
	}
	// Written by build.go and read by nothing until this method asked.
	if len(got.Aliases) != 1 || got.Aliases[0] != "Joel parental leave" {
		t.Errorf("aliases = %v, want the one the source gave", got.Aliases)
	}
	if got.Provenance.By != "joel.c@halcyon.com" || got.Provenance.At != "2026-08-31T18:35:00Z" {
		t.Errorf("provenance = %+v, want the asserter and the date", got.Provenance)
	}
	if d, err := l.Describe(ctx, load, "nobody"); err != nil || d.ID != "" {
		t.Errorf("Describe of an absent id = %+v, %v; want a zero value and no error", d, err)
	}
	if _, err := l.Describe(ctx, "no-such-load", "absence:1"); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Describe in an unknown load = %v, want ErrNoLoad", err)
	}
}

func TestACitationHasThreeOutcomes(t *testing.T) {
	l, load := loadedForRead(t)
	ctx := context.Background()

	got, err := l.Cite(ctx, load, "profile.pdf", 14)
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	if got.Text != "Mira works on Ledger." || got.Start != 100 || got.End != 119 {
		t.Errorf("Cite = %+v, want the text and its place in the file", got)
	}
	// A claim whose producer worked in no chunks. Not a failure.
	if _, err := l.Cite(ctx, load, "team.json", -1); !errors.Is(err, recall.ErrNoChunk) {
		t.Errorf("Cite of a chunkless claim = %v, want ErrNoChunk", err)
	} else if errors.Is(err, recall.ErrNoCitation) {
		t.Error("a chunkless claim was refused as unverifiable")
	}
	// A chunk this load does not hold. This one IS a failure.
	if _, err := l.Cite(ctx, load, "profile.pdf", 99); !errors.Is(err, recall.ErrNoCitation) {
		t.Errorf("Cite of a missing chunk = %v, want ErrNoCitation", err)
	}
	// The right number under the wrong file.
	if _, err := l.Cite(ctx, load, "team.json", 14); !errors.Is(err, recall.ErrNoCitation) {
		t.Errorf("Cite of the right chunk under the wrong source = %v, want ErrNoCitation", err)
	}
	if _, err := l.Cite(ctx, "no-such-load", "profile.pdf", 14); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Cite in an unknown load = %v, want ErrNoLoad", err)
	}
}

func TestTheIdentityQuestionsCanBeAskedAndAreNotClaims(t *testing.T) {
	l, load := loadedForRead(t)
	ctx := context.Background()

	all, err := l.Unanswered(ctx, load, "")
	if err != nil {
		t.Fatalf("Unanswered: %v", err)
	}
	if len(all) != 1 || all[0].Signal != alchemy.DuplicateNameAffix {
		t.Fatalf("Unanswered = %+v, want the one open question", all)
	}
	if all[0].Left != "Nadia" || all[0].Right != "Nadia Okonkwo" {
		t.Errorf("the pair did not survive: %+v", all[0])
	}
	// Narrowed, and "all" is a search term rather than a sentinel.
	if got, _ := l.Unanswered(ctx, load, "Okonkwo"); len(got) != 1 {
		t.Errorf("narrowing to a name returned %d", len(got))
	}
	if got, _ := l.Unanswered(ctx, load, "all"); len(got) != 0 {
		t.Errorf(`"all" matched %d questions; it is a substring, not a sentinel`, len(got))
	}
	// Nothing here is an edge between the two nodes: a traversable "may be the
	// same as" is a claim, and nobody has ruled.
	for _, c := range mustClaims(t, l, load, "person:nadia") {
		if strings.Contains(strings.ToUpper(c.Type), "SAME") {
			t.Errorf("a duplicate became a walkable edge: %s", c)
		}
	}
}

func TestContributionsShowsTheJoinTheStoreMade(t *testing.T) {
	l, load := loadedForRead(t)
	ctx := context.Background()

	got, err := l.Contributions(ctx, load, "person:mira")
	if err != nil {
		t.Fatalf("Contributions: %v", err)
	}
	// Named by profile.pdf, and two other sources asserted edges touching it.
	if !got.Joined() {
		t.Errorf("Contributions = %+v, want more than one source", got)
	}
	sources := map[string]bool{}
	var named []string
	for _, c := range got.Contributors {
		sources[c.Source] = true
		if c.Name != "" {
			named = append(named, c.Name)
		}
	}
	for _, want := range []string{"profile.pdf", "team.json", "slack/#general"} {
		if !sources[want] {
			t.Errorf("%q had a hand in this node and is not reported: %+v", want, got)
		}
	}
	// Only the record that created the node contributes a name. Copying the
	// node's name onto every contributor would report every join as unanimous.
	if len(named) != 1 || named[0] != "Mira" {
		t.Errorf("named = %v, want only the creating record's name", named)
	}
	if _, err := l.Contributions(ctx, "no-such-load", "person:mira"); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Contributions in an unknown load = %v, want ErrNoLoad", err)
	}
}

// A half-written load must answer nothing at all. Filter.Loads takes a caller
// at their word, which is right for the query surface and wrong for this one.
func TestAnUnfinishedLoadAnswersNothing(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()

	res := readable()
	tx, err := l.Begin(ctx, sink.Ident{Load: "half", Digest: sink.Digest(res)})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := tx.Entities(ctx, res.Entities); err != nil {
		t.Fatalf("Entities: %v", err)
	}
	// No Commit: the load marker stays incomplete.

	if got, err := l.Find(ctx, "half", "Mira", 5); err != nil || len(got.Nodes) != 0 {
		t.Errorf("Find on an unfinished load = %+v, %v; half a graph must not answer", got, err)
	}
	if types, err := l.Types(ctx, "half"); err != nil || len(types) != 0 {
		t.Errorf("Types on an unfinished load = %v, %v", types, err)
	}
	if _, err := l.Describe(ctx, "half", "person:mira"); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Describe on an unfinished load = %v, want ErrNoLoad", err)
	}
	if _, err := l.Cite(ctx, "half", "profile.pdf", 14); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Cite on an unfinished load = %v, want ErrNoLoad", err)
	}
}

func mustClaims(t *testing.T, l *Loader, load, id string) []recall.Claim {
	t.Helper()
	cs, err := l.Claims(context.Background(), load, id)
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	return cs
}

// The query surface had the same hole one field over.
//
// build.go writes Entity.Aliases onto the payload and readEntity did not take
// them off again, so every alchemy.Entity Records returned came back with none
// while the store held them. Nothing asked for an alias until Describe did.
func TestRecordsReturnsTheAliasesTheStoreHolds(t *testing.T) {
	l, load := loadedForRead(t)
	got, err := l.Records(context.Background(), Filter{Loads: []string{load}, Kinds: []string{"entity"}}, 0)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	for _, e := range got.Entities {
		if e.ID != "absence:1" {
			continue
		}
		if len(e.Aliases) != 1 || e.Aliases[0] != "Joel parental leave" {
			t.Fatalf("Records returned aliases %v, want the one the source gave", e.Aliases)
		}
		return
	}
	t.Fatal("the entity under test was not returned")
}
