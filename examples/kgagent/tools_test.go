package kgagent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/examples/kgagent"
)

// Every test in this file is a defect that reached production and was found by
// an agent giving a confident wrong answer through these tools. None of them
// needs a database or a model: each one was a defect in the tool layer, and
// each was invisible from inside pkg/recall, which answered exactly what it was
// asked every time.

func tools(t *testing.T) (map[string]kgagent.Tool, *kgagent.Graph) {
	t.Helper()
	g := &kgagent.Graph{Reader: graph(), Load: "ld-1"}
	byName := map[string]kgagent.Tool{}
	for _, tool := range g.Tools() {
		byName[tool.Name] = tool
	}
	return byName, g
}

func call(t *testing.T, tool kgagent.Tool, args map[string]any) string {
	t.Helper()
	out, err := tool.Do(context.Background(), args)
	if err != nil {
		t.Fatalf("%s(%v): %v", tool.Name, args, err)
	}
	s, ok := out.(string)
	if !ok {
		t.Fatalf("%s returned %T, want the text a model reads", tool.Name, out)
	}
	return s
}

// The description said to pass "all" and recall.Unanswered reads "all" as a
// literal substring, so it matched nothing and the tool answered "no unresolved
// identity questions touch that". Twenty-nine runs in thirty wrote that
// sentence into the answer over a store holding thirteen of them, and every one
// was false.
func TestAskingForEveryOpenQuestionDoesNotRequireASentinelWord(t *testing.T) {
	byName, _ := tools(t)
	tool := byName["graph_open_questions"]

	// The schema must not require the filter, or the model has to send one.
	if req, ok := tool.Schema["required"]; ok {
		t.Fatalf("graph_open_questions requires %v; a tool that demands a filter to see everything "+
			"has told the model to invent one", req)
	}
	if !strings.Contains(tool.Description, "OMIT") {
		t.Error("the description does not tell the model to omit the argument, which is the only way " +
			"to ask for all of them")
	}

	// Omitted entirely, which is what the description now says to do.
	if got := call(t, tool, map[string]any{}); !strings.Contains(got, "Nadia Okonkwo") {
		t.Errorf("with no argument the tool answered %q; it must return every open question", got)
	}
	// And the literal that used to be the sentinel is now just a search term
	// that matches nothing, which is honest rather than silently empty.
	if got := call(t, tool, map[string]any{"about": "Nadia"}); !strings.Contains(got, "Nadia Okonkwo") {
		t.Errorf("narrowing to a name returned %q", got)
	}
}

// recall.Mark renders a claim from a producer that did not work in chunks as
// "[team.json]", with no number. graph_cite REQUIRED a chunk, so a model
// holding that marker had nothing to send and a schema insisting on something.
func TestCitingAChunklessMarkerNeedsNoChunkNumber(t *testing.T) {
	byName, _ := tools(t)
	tool := byName["graph_cite"]

	req, _ := tool.Schema["required"].([]string)
	if len(req) != 1 || req[0] != "source" {
		t.Fatalf("graph_cite requires %v; the chunk is not always in the marker, so demanding it "+
			"tells the model to guess a number", req)
	}

	// The measured failure: with the chunk omitted, fmt.Sscanf left the index
	// at zero, and chunk 0 is a real chunk. The model was handed the wrong
	// passage as though it had asked for it.
	got := call(t, tool, map[string]any{"source": "profile.pdf"})
	if strings.Contains(got, "Chapter one") {
		t.Fatal("omitting the chunk resolved chunk 0 and returned its text; a missing argument " +
			"became a legal index, and nothing about the answer looked wrong")
	}

	// And the answer for a claim that never had a chunk must not be the sentence
	// reserved for a citation that does not check out. §5b ranks these records
	// ABOVE a model reading prose; seven of thirteen citation attempts were
	// refused this way, and all seven were false alarms.
	if strings.Contains(got, "do not treat it as evidence") {
		t.Errorf("a chunkless claim was refused as unverifiable: %q", got)
	}
	if !strings.Contains(got, "nothing to quote") || !strings.Contains(got, "do not report it as uncited") {
		t.Errorf("graph_cite on a chunkless claim said %q; it has to say the claim stands", got)
	}
}

// A citation that genuinely does not resolve still has to be refused, or the
// fix above would have turned every bad citation into a good one.
func TestACitationThatDoesNotResolveIsStillRefused(t *testing.T) {
	byName, _ := tools(t)
	got := call(t, byName["graph_cite"], map[string]any{"source": "profile.pdf", "chunk": "99"})
	if !strings.Contains(got, "do not treat it as evidence") {
		t.Errorf("an unresolvable citation answered %q", got)
	}
}

// Claims returns names and Claims takes an id, so an agent asked which products
// a team's people work on spent eight of thirteen calls on Find/Claims pairs,
// turning names it had just been handed back into the identifiers it had just
// been denied. Find is a substring search over a page that can be truncated, so
// that round trip is where a walk can continue from the wrong node.
func TestAClaimGivesTheIDsToWalkOnWithoutASecondLookup(t *testing.T) {
	byName, _ := tools(t)
	got := call(t, byName["graph_claims"], map[string]any{"id": "person:mira"})

	if !strings.Contains(got, "product:ledger") {
		t.Errorf("the far end's id is not in the answer, so the next hop needs an anchor search: %q", got)
	}
	// The names still have to be what the sentence is read in. Ids in place of
	// names would hand a reader "e17 -[USES]-> e04".
	if !strings.Contains(got, "Mira -[DEVELOPS]-> Ledger") {
		t.Errorf("the claim does not read as a sentence: %q", got)
	}
	// And the split between stated and inferred survives to the model, which is
	// most of what this graph is for.
	if !strings.Contains(got, "stated") || !strings.Contains(got, "inferred") {
		t.Errorf("the answer does not say which claims were stated: %q", got)
	}
}

// A substring search is the only way to enumerate with, so an agent asked what
// a graph contained tried the alphabet: eighty-three calls, and a table right
// about the total and wrong in five places under it.
func TestTheGraphCanBeEnumeratedWithoutTryingTheAlphabet(t *testing.T) {
	byName, g := tools(t)

	got := call(t, byName["graph_types"], map[string]any{})
	if !strings.Contains(got, "Person  15") {
		t.Errorf("graph_types answered %q, want the kinds and their counts", got)
	}

	// The count graph_types gave is the limit that reads the class out whole.
	// graph_find's limit is fixed, and that is how "list every person" was
	// answered with thirteen of twenty-one.
	got = call(t, byName["graph_of_type"], map[string]any{"type": "Person", "limit": "15"})
	if strings.Contains(got, "shown") {
		t.Errorf("reading a class out with the count graph_types gave still truncated: %q", got)
	}
	if n := strings.Count(got, "id=person:"); n != 15 {
		t.Errorf("got %d people, want all 15", n)
	}

	// Two calls, where the alphabet took eighty-three.
	if calls := g.Calls(); len(calls) != 2 {
		t.Errorf("enumerating took %d calls: %v", len(calls), calls)
	}
}

// A truncated page used to report how many more existed and offer nothing to do
// about it. A tool that states a number its caller cannot act on has told them
// their answer is incomplete and left them to state it anyway.
func TestATruncatedAnchorPageNamesWhatToDoAboutIt(t *testing.T) {
	byName, _ := tools(t)
	// The empty search, which is what an agent with no enumeration tool
	// actually sent first, before graph_types existed.
	got := call(t, byName["graph_find"], map[string]any{"name": ""})
	if !strings.Contains(got, "more not shown") {
		t.Fatalf("the page did not report that it was one: %q", got)
	}
	if !strings.Contains(got, "graph_of_type") {
		t.Errorf("the truncation notice names no way to see the rest: %q", got)
	}
}

// The limit is the model's, but a model that sends nonsense must not be handed
// an error or an empty page instead of an answer.
func TestAnUnreadableLimitFallsBackRatherThanBecomingZero(t *testing.T) {
	byName, _ := tools(t)
	for _, limit := range []string{"", "lots", "0", "-3"} {
		got := call(t, byName["graph_of_type"], map[string]any{"type": "Person", "limit": limit})
		if !strings.Contains(got, "id=person:") {
			t.Errorf("limit %q returned no entities: %q", limit, got)
		}
	}
}

// Nothing found is an ordinary answer and has to read as one, or a model treats
// an empty page as a failure and tries again with worse arguments.
func TestNothingFoundReadsAsAnAnswerAndNotAnError(t *testing.T) {
	byName, _ := tools(t)
	for _, tc := range []struct{ tool, arg, key, want string }{
		{"graph_find", "zzz", "name", "no entity in the graph has that in its name"},
		{"graph_claims", "person:nobody", "id", "no neighbours"},
		{"graph_of_type", "Sandwich", "type", "the type names are what graph_types returns"},
	} {
		got := call(t, byName[tc.tool], map[string]any{tc.key: tc.arg})
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s(%q) = %q, want it to contain %q", tc.tool, tc.arg, got, tc.want)
		}
	}
}

// The trace is the only place a bad run is visible when the answer is right,
// and every defect above was found by reading one.
func TestTheTraceRecordsWhatWasAskedInOrder(t *testing.T) {
	byName, g := tools(t)
	call(t, byName["graph_find"], map[string]any{"name": "Mira"})
	call(t, byName["graph_claims"], map[string]any{"id": "person:mira"})
	call(t, byName["graph_cite"], map[string]any{"source": "profile.pdf", "chunk": "20"})

	want := []string{"graph_find(Mira)", "graph_claims(person:mira)", "graph_cite([profile.pdf#20])"}
	got := g.Calls()
	if len(got) != len(want) {
		t.Fatalf("trace = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i+1, got[i], want[i])
		}
	}
}

// The gap the leave experiment found: an entity's own fields were write-only.
// Every store kept the attributes and the aliases and the asserter and the
// date; no reader could return any of them, so an agent looking for a window it
// could see must exist re-read the node three times, tried twice to cite the
// announcement, and answered from the node's name.
func TestAnEntitysOwnFieldsCanBeRead(t *testing.T) {
	byName, _ := tools(t)
	got := call(t, byName["graph_describe"], map[string]any{"id": "absence:1"})

	// The window, which is the whole of what answers "is this still true".
	for _, want := range []string{"from: 2026-10-05", "to: 2026-11-05", "start_confirmed: false"} {
		if !strings.Contains(got, want) {
			t.Errorf("the record does not carry %q: %q", want, got)
		}
	}
	// The asserter and the date. "human" alone says somebody can be asked;
	// these say who, and how long ago they said it.
	if !strings.Contains(got, "2026-08-31") || !strings.Contains(got, "joel.c@halcyon.com") {
		t.Errorf("the record does not say who recorded it or when: %q", got)
	}
	if !strings.Contains(got, "Joel parental leave") {
		t.Errorf("the aliases were dropped: %q", got)
	}

	// Attributes in a fixed order, so one question asked twice builds the same
	// pack. Every other method in pkg/recall orders its results for this.
	if strings.Index(got, "from:") > strings.Index(got, "start_confirmed:") {
		t.Errorf("attributes are not ordered: %q", got)
	}
}

// A record with no extra fields must not grow empty lines, and an id that is
// not there is an ordinary answer rather than an error.
func TestDescribingAPlainRecordAndAMissingOne(t *testing.T) {
	byName, _ := tools(t)
	got := call(t, byName["graph_describe"], map[string]any{"id": "person:mira"})
	if strings.Contains(got, "also called") || strings.Contains(got, "asserted by") {
		t.Errorf("a record with no aliases and no asserter grew a clause: %q", got)
	}
	if got := call(t, byName["graph_describe"], map[string]any{"id": "nobody"}); !strings.Contains(got, "no entity") {
		t.Errorf("a missing id answered %q", got)
	}
}
