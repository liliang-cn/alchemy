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
		c := NewClaim("a", "USES", "b", alchemy.Provenance{Producer: p})
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
		claim: NewClaim("SuperAI", "USES", "CortexDB", alchemy.Provenance{
			Source: "architecture.pdf", Chunk: 14, Producer: alchemy.ProducerLLMExtract,
		}),
		want: "SuperAI -[USES]-> CortexDB (inferred, llm-extract) [architecture.pdf#14]",
	}, {
		// -1 is alchemy's "the producer did not work in chunks", so there is
		// no chunk to cite and printing #-1 would hand an agent a citation it
		// would then try, and fail, to resolve.
		name: "a producer that worked in no chunks cites the file and no chunk",
		claim: NewClaim("orders", "REFERENCES", "customers", alchemy.Provenance{
			Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL,
		}),
		want: "orders -[REFERENCES]-> customers (stated, ddl) [schema.sql]",
	}, {
		name: "no source at all is no marker rather than an empty one",
		claim: NewClaim("a", "R", "b", alchemy.Provenance{
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
