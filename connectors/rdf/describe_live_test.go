package rdf

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Everything a source said about a node, read back.
//
// This is the gap the leave experiment found, and it is not about time. An
// absence was recorded the best way the model allows -- an entity carrying
// `from`, `to`, `start_confirmed` and the announcement verbatim, asserted by a
// named person on a dated message -- and asked about four months after it
// ended. Six fields were in the store. The reader could return three: id, type
// and name. The agent went looking for the dates, re-read the node three times,
// tried twice to cite the announcement, then answered from the node's NAME and
// dropped a developer from a contact list over a leave sixteen months past.
//
// So the test is the comparison the experiment made: what went in against what
// comes back.
func TestEverythingASourceSaidAboutANodeCanBeReadBack(t *testing.T) {
	l := liveLoader(t, Options{})
	ctx := context.Background()
	if _, err := l.Load(ctx, describable()); err != nil {
		t.Fatalf("load: %v", err)
	}
	load := l.opts.RunID

	got, err := l.Describe(ctx, load, "absence:1")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got.ID != "absence:1" || got.Type != "Absence" || got.Name != "Joel C parental leave" {
		t.Errorf("Describe = %+v, want the record it was asked for", got)
	}

	// The attributes, which no primitive could return before this one.
	for k, want := range map[string]any{
		"from": "2026-10-05", "to": "2026-11-05", "start_confirmed": false,
	} {
		if got.Attributes[k] != want {
			t.Errorf("attribute %q = %#v, want %#v; the window is what answers the question",
				k, got.Attributes[k], want)
		}
	}
	// A nested value has to survive the encoding each store had to invent for
	// it, or an attribute map is only usable for flat records.
	nested, _ := got.Attributes["cover"].(map[string]any)
	if nested["team"] != "Ledger" {
		t.Errorf("nested attribute came back as %#v", got.Attributes["cover"])
	}

	// The aliases, write-only until now for the same reason.
	if len(got.Aliases) != 1 || got.Aliases[0] != "Joel parental leave" {
		t.Errorf("aliases = %v, want the one the source gave", got.Aliases)
	}

	// And the whole provenance, including the two fields that say a named
	// person asserted this and when. An agent told "human" knows somebody can
	// be asked; these two say who, and how long ago.
	if got.Provenance.By != "joel.c@halcyon.com" || got.Provenance.At != "2026-08-31T18:35:00Z" {
		t.Errorf("provenance = %+v, want the asserter and the date", got.Provenance)
	}
	if got.Provenance.Source != "slack/#general" || got.Provenance.Ontology != "sds@3" {
		t.Errorf("provenance lost fields: %+v", got.Provenance)
	}

	// The same two fields on the EDGE, which is a different record and is dated
	// separately. A walk that read the node's would date every claim about an
	// entity by whatever first named it.
	claims, err := l.Claims(ctx, load, "absence:1")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if len(claims) == 0 {
		t.Fatal("no claims about the absence")
	}
	if claims[0].By != "joel.c@halcyon.com" || claims[0].At != "2026-08-31T18:35:00Z" {
		t.Errorf("claim = %+v, want the asserter and the date on the edge", claims[0])
	}
	// And they reach the line a model actually reads.
	if s := claims[0].String(); !strings.Contains(s, "asserted 2026-08-31T18:35:00Z by joel.c@halcyon.com") {
		t.Errorf("the rendered claim does not say when or by whom: %q", s)
	}

	// A record that nobody asserted must not grow an empty clause; a reader
	// trained to skip it would skip it on the lines that have one.
	plain, err := l.Claims(ctx, load, "person:joel-c")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	for _, c := range plain {
		if c.Type == "DEVELOPS" && strings.Contains(c.String(), "asserted") {
			t.Errorf("an extracted claim carries an assertion clause: %q", c.String())
		}
	}

	// An id the load does not hold is an ordinary answer, and an unknown load is
	// the caller's mistake -- the asymmetry Contributions draws.
	if d, err := l.Describe(ctx, load, "nobody:here"); err != nil || d.ID != "" {
		t.Errorf("Describe of an absent id = %+v, %v; want a zero value and no error", d, err)
	}
	if _, err := l.Describe(ctx, load+"-typo", "absence:1"); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Describe in an unknown load = %v, want ErrNoLoad", err)
	}
}

// describable is the leave experiment as a fixture: an absence with its window
// in attributes, an alias, a nested value, and a human assertion carrying an
// author and a date.
func describable() alchemy.Result {
	team := alchemy.Provenance{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport, Ontology: "sds@3"}
	said := alchemy.Provenance{
		Source: "slack/#general", Chunk: -1, Producer: alchemy.ProducerHuman,
		By: "joel.c@halcyon.com", At: "2026-08-31T18:35:00Z", Ontology: "sds@3",
	}
	res := alchemy.Result{
		Entities: []alchemy.Entity{
			{ID: "product:ledger", Type: "Product", Name: "Ledger", Provenance: team},
			{ID: "person:joel-c", Type: "Person", Name: "Joel C", Provenance: team},
			{ID: "absence:1", Type: "Absence", Name: "Joel C parental leave",
				Aliases: []string{"Joel parental leave"},
				Attributes: map[string]any{
					"from": "2026-10-05", "to": "2026-11-05", "start_confirmed": false,
					"announced": "starting some time in the coming 5 weeks",
					"cover":     map[string]any{"team": "Ledger"},
				}, Provenance: said},
		},
		Relations: []alchemy.Relation{
			{From: "person:joel-c", To: "product:ledger", Type: "DEVELOPS", Provenance: team},
			{From: "absence:1", To: "person:joel-c", Type: "ABSENCE_OF", Provenance: said},
		},
	}
	res.Counts = res.Derivable()
	return res
}
