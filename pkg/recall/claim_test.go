package recall

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The stated/inferred split is the one thing a reader of a context pack acts
// on, so it is tested against alchemy.Producer directly rather than against a
// list of names this package keeps. A producer added to alchemy with a
// Deterministic() of its own has to show up here without anybody editing this
// package, and a connector that stored the boolean at load time and read it
// back would be answering with last year's rule.
func TestAClaimTakesStatedOrInferredFromTheProducerRuleAndNotFromACopy(t *testing.T) {
	for _, p := range []alchemy.Producer{
		alchemy.ProducerDDL, alchemy.ProducerGraphImport, alchemy.ProducerTabular,
		alchemy.ProducerLLMExtract, alchemy.ProducerHuman, alchemy.Producer("something-new"),
	} {
		c := NewClaim(Endpoint{ID: "e1", Name: "a"}, Endpoint{ID: "e2", Name: "b"}, "USES", alchemy.Provenance{Producer: p})
		if c.Stated != p.Deterministic() {
			t.Errorf("NewClaim(producer %q).Stated = %v, alchemy says %v; "+
				"the rule lives in alchemy.Producer.Deterministic and nowhere else", p, c.Stated, p.Deterministic())
		}
		if c.Producer != p {
			t.Errorf("NewClaim(producer %q).Producer = %q, want the producer it was given", p, c.Producer)
		}
	}
}

func TestAClaimRendersAsOneLineAnAgentCanPutInAContextPack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		claim Claim
		want  string
	}{{
		name: "a model proposed it, so the line says inferred and cites a chunk",
		claim: NewClaim(Endpoint{ID: "e1", Name: "SuperAI"}, Endpoint{ID: "e2", Name: "CortexDB"}, "USES", alchemy.Provenance{
			Source: "architecture.pdf", Chunk: 14, Producer: alchemy.ProducerLLMExtract,
		}),
		want: "SuperAI -[USES]-> CortexDB (inferred, llm-extract) [architecture.pdf#14]",
	}, {
		// -1 is alchemy's "the producer did not work in chunks", so there is
		// no chunk to cite and printing #-1 would hand an agent a citation it
		// would then try, and fail, to resolve.
		name: "a producer that worked in no chunks cites the file and no chunk",
		claim: NewClaim(Endpoint{ID: "t1", Name: "orders"}, Endpoint{ID: "t2", Name: "customers"}, "REFERENCES", alchemy.Provenance{
			Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL,
		}),
		want: "orders -[REFERENCES]-> customers (stated, ddl) [schema.sql]",
	}, {
		name: "no source at all is no marker rather than an empty one",
		claim: NewClaim(Endpoint{ID: "e1", Name: "a"}, Endpoint{ID: "e2", Name: "b"}, "R", alchemy.Provenance{
			Chunk: -1, Producer: alchemy.ProducerHuman,
		}),
		want: "a -[R]-> b (stated, human)",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.claim.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestACitationMarkerNamesTheFileAndTheChunkAClaimCameFrom(t *testing.T) {
	for _, tc := range []struct {
		source string
		chunk  int
		want   string
	}{
		{"northgate-profile.pdf", 12, "[northgate-profile.pdf#12]"},
		{"northgate-profile.pdf", 0, "[northgate-profile.pdf#0]"},
		{"schema.sql", -1, "[schema.sql]"},
		{"", 4, ""},
	} {
		if got := Mark(tc.source, tc.chunk); got != tc.want {
			t.Errorf("Mark(%q, %d) = %q, want %q", tc.source, tc.chunk, got, tc.want)
		}
	}
}

// A page that does not say it is a page is the defect this type exists for.
//
// Measured on a real corpus: an anchor search for the name the whole graph was
// about matched 14 entities and returned 12, and the entity the question was
// actually about was thirteenth. Two agents on two different runtimes were
// handed the truncated list with no sign it was truncated, and in seven runs
// out of eight went on to invent an id or answer from their own prior
// knowledge -- under a prompt whose first rule was to use only what the tools
// returned.
func TestAPageSaysWhenItIsOne(t *testing.T) {
	for _, tc := range []struct {
		name  string
		found Found
		want  bool
	}{
		{"more matched than shown", Found{Nodes: make([]Node, 12), Total: 14}, true},
		{"everything that matched", Found{Nodes: make([]Node, 3), Total: 3}, false},
		{"nothing matched", Found{}, false},
		// Total below the page cannot happen from a correct implementation,
		// and the honest answer to an impossible pair is the safe one: do not
		// tell a reader something is missing when nothing is.
		{"a total that disagrees downward", Found{Nodes: make([]Node, 5), Total: 2}, false},
	} {
		if got := tc.found.Truncated(); got != tc.want {
			t.Errorf("%s: Truncated() = %v, want %v (%d of %d)",
				tc.name, got, tc.want, len(tc.found.Nodes), tc.found.Total)
		}
	}
}

// The names are what a claim reads as and the IDs are what it walks by, and a
// renderer that leaked one into the other would put "e17 -[USES]-> e04" in front
// of a person. String is where that would show up first.
func TestTheWalkableIDsAreCarriedWithoutReachingTheRenderedLine(t *testing.T) {
	c := NewClaim(Endpoint{ID: "person:mira", Name: "Mira"},
		Endpoint{ID: "product:ledger", Name: "Ledger"}, "DEVELOPS",
		alchemy.Provenance{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport})
	if c.FromID != "person:mira" || c.ToID != "product:ledger" {
		t.Errorf("the IDs did not survive: %+v", c)
	}
	if c.From != "Mira" || c.To != "Ledger" {
		t.Errorf("the names did not survive: %+v", c)
	}
	if got, want := c.String(), "Mira -[DEVELOPS]-> Ledger (stated, graph-import) [team.json]"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
