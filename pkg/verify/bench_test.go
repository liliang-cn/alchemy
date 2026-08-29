package verify_test

import (
	"fmt"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

// benchInput builds a job of n entities and n relations that is deliberately
// hostile to a pairwise scan: the records crowd onto few identities (fifty
// records per entity ID and per edge), which is the shape a real import has
// when one table and one manual describe the same fifty things over and over.
// A pairwise implementation is quadratic *within a group*, so this fixture
// makes the difference show up in the ns/element column below rather than
// hiding it behind a wide key space.
func benchInput(n int) verify.Input {
	keys := n / 50
	if keys < 1 {
		keys = 1
	}
	in := verify.Input{Vocabulary: vocab(), OntologyID: "sds@3",
		Entities:  make([]alchemy.Entity, 0, n+keys),
		Relations: make([]alchemy.Relation, 0, n),
	}
	types := []string{"Cluster", "StoragePool"}
	regions := []string{"eu-west", "us-east"}
	cards := []string{"1:n", "1:1"}

	for i := 0; i < n; i++ {
		// k is which of the fifty-times-repeated identities this record is
		// about; m is which repetition. Everything that varies has to vary with
		// m rather than with i, or the fifty records under one key are fifty
		// copies of one claim and the detector is never asked a question — which
		// is what the guard in the benchmark caught the first time this fixture
		// was written.
		k, m := i%keys, i/keys
		id := fmt.Sprintf("c%d", k)
		prov := fromPDF
		if m%2 == 0 {
			prov = fromSchema
		}
		in.Entities = append(in.Entities, alchemy.Entity{
			ID: id, Type: types[m%len(types)], Name: "prod",
			Attributes: map[string]any{"region": regions[(m/2)%len(regions)], "version": "3.1"},
			Provenance: prov,
		})
		from, to := id, fmt.Sprintf("n%d", k)
		if m%3 == 0 {
			from, to = to, from // both directions of the same edge, in one job.
		}
		in.Relations = append(in.Relations, alchemy.Relation{
			From: from, To: to, Type: "MENTIONS",
			Attributes: map[string]any{"card": cards[(m/2)%len(cards)]},
			Provenance: prov,
		})
	}
	for k := 0; k < keys; k++ {
		in.Entities = append(in.Entities, alchemy.Entity{ID: fmt.Sprintf("n%d", k), Type: "Node"})
	}
	return in
}

// BenchmarkCheck measures the claim §8.1 makes rather than asserting it: cost
// per element must stay flat as the job grows, because "conflict detection is
// keyed by entity identity rather than compared pairwise — an O(n²) scan is a
// plausible-looking implementation that dies at the volume this section is
// about".
func BenchmarkCheck(b *testing.B) {
	for _, n := range []int{12500, 25000, 50000, 100000, 200000} {
		in := benchInput(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			var got verify.Report
			for i := 0; i < b.N; i++ {
				got = verify.Check(in)
			}
			// Guard rather than decoration: a fixture that stopped producing one
			// of the kinds would leave that code path unmeasured while the
			// numbers below still looked convincing.
			kinds := map[alchemy.ConflictKind]bool{}
			for _, c := range got.Conflicts {
				kinds[c.Kind] = true
			}
			for _, want := range []alchemy.ConflictKind{
				alchemy.ConflictEntityType, alchemy.ConflictEntityAttributes,
				alchemy.ConflictRelationDirection, alchemy.ConflictContradiction,
				alchemy.ConflictRelationAttributes,
			} {
				if !kinds[want] {
					b.Fatalf("fixture produced no %q conflict; that detector is not being measured", want)
				}
			}
			// ns per (entity + relation), the number that must not grow with n.
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(2*n), "ns/element")
		})
	}
}
