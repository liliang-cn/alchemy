package extract

import (
	"strings"
	"testing"
)

// Real models do all of these. Each shape here was chosen because it is a way a
// reply can be wrong that a naive json.Unmarshal turns into "this chunk was
// empty" — the one outcome §5 forbids.
func TestParseReplyRepairsWhatCanBeRepaired(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"clean", `{"entities":[{"type":"Cluster","name":"SuperAI"}],"relations":[]}`},
		{"markdown fence", "```json\n{\"entities\":[{\"type\":\"Cluster\",\"name\":\"SuperAI\"}],\"relations\":[]}\n```"},
		{"bare fence", "```\n{\"entities\":[{\"type\":\"Cluster\",\"name\":\"SuperAI\"}],\"relations\":[]}\n```"},
		{"prose before", "Here is the JSON:\n{\"entities\":[{\"type\":\"Cluster\",\"name\":\"SuperAI\"}],\"relations\":[]}"},
		{"prose after", "{\"entities\":[{\"type\":\"Cluster\",\"name\":\"SuperAI\"}],\"relations\":[]}\n\nI hope this helps!"},
		{"prose both sides", "Sure! Here is the JSON:\n```json\n{\"entities\":[{\"type\":\"Cluster\",\"name\":\"SuperAI\"}],\"relations\":[]}\n```\nLet me know if you need more."},
		{"single quotes", `{'entities':[{'type':'Cluster','name':'SuperAI'}],'relations':[]}`},
		{"trailing commas", `{"entities":[{"type":"Cluster","name":"SuperAI",},],"relations":[],}`},
		{"leading whitespace and BOM", "\ufeff  \n{\"entities\":[{\"type\":\"Cluster\",\"name\":\"SuperAI\"}],\"relations\":[]}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReply(tc.raw)
			if err != nil {
				t.Fatalf("parseReply: %v", err)
			}
			if len(got.Entities) != 1 || got.Entities[0].Name != "SuperAI" || got.Entities[0].Type != "Cluster" {
				t.Fatalf("got %#v, want the one SuperAI/Cluster entity", got.Entities)
			}
		})
	}
}

// A brace inside a string must not end the object: the scan that finds the JSON
// in a sea of prose has to understand strings, or a name containing "}"
// truncates the reply and the rest of the chunk is lost.
func TestParseReplySurvivesBracesInsideStrings(t *testing.T) {
	raw := `Here you go: {"entities":[{"type":"Node","name":"node-} {a"}],"relations":[]} done`
	got, err := parseReply(raw)
	if err != nil {
		t.Fatalf("parseReply: %v", err)
	}
	if len(got.Entities) != 1 || got.Entities[0].Name != "node-} {a" {
		t.Fatalf("got %#v", got.Entities)
	}
}

// An apostrophe inside a double-quoted string must survive the single-quote
// repair, or fixing one model's habit corrupts another model's correct reply.
func TestParseReplyDoesNotCorruptApostrophesInValidJSON(t *testing.T) {
	raw := `{"entities":[{"type":"Person","name":"O'Brien"}],"relations":[]}`
	got, err := parseReply(raw)
	if err != nil {
		t.Fatalf("parseReply: %v", err)
	}
	if got.Entities[0].Name != "O'Brien" {
		t.Fatalf("name = %q, want O'Brien", got.Entities[0].Name)
	}
}

// What cannot be repaired must be loud. A truncated reply is the dangerous one:
// the entities before the cut are real, and half a graph delivered as if it
// were whole is exactly the failure that looks like a success.
func TestParseReplyFailsLoudlyOnWhatItCannotRepair(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"truncated mid object", `{"entities":[{"type":"Cluster","name":"Sup`},
		{"truncated after fence", "```json\n{\"entities\":[{\"type\":\"Cluster\",\"na"},
		{"refusal", "I'm sorry, I can't help with that."},
		{"empty", ""},
		{"whitespace only", "   \n\t "},
		{"prose only", "The chunk describes a deployment topology across three regions."},
		{"json but not an object", `["SuperAI", "node-a"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReply(tc.raw)
			if err == nil {
				t.Fatalf("want an error, got %#v", got)
			}
			if strings.TrimSpace(err.Error()) == "" {
				t.Error("the error says nothing a person could act on")
			}
		})
	}
}

// An explicit empty reply is a correct answer, not a failure.
func TestParseReplyAcceptsAnHonestlyEmptyReply(t *testing.T) {
	for _, raw := range []string{`{"entities":[],"relations":[]}`, `{}`, `{"entities":null,"relations":null}`} {
		got, err := parseReply(raw)
		if err != nil {
			t.Fatalf("parseReply(%q): %v", raw, err)
		}
		if len(got.Entities) != 0 || len(got.Relations) != 0 {
			t.Fatalf("parseReply(%q) = %#v, want empty", raw, got)
		}
	}
}
