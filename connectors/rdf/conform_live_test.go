package rdf

import (
	"testing"

	"github.com/liliang-cn/alchemy/connectors/internal/sinkconform"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// TestTheSharedConformanceSuitePasses is the fifth store answering the same
// questions as the other four.
//
// sinkconform's own doc says why it has to exist: the connectors were written
// apart, they agreed about the envelope and disagreed about everything under
// it, and a suite only the connector's own author runs would let the agreement
// decay without anybody noticing. This one was written after the interface
// rather than before it, which makes it the first real test of whether §4.1's
// line was drawn in the right place — and it passes unchanged, including the
// two cases that are about identity rather than about writing: a result that
// retires a record it does not contain, and a result that differs from another
// only in what it retires.
//
// Each case gets its own IRI namespace inside one repository, rather than its
// own repository. A load is a named graph and a namespace is a different set of
// graphs, so the cases cannot see each other — and creating eight repositories
// to prove it would be testing GraphDB's administration API eight times.
func TestTheSharedConformanceSuitePasses(t *testing.T) {
	target := liveTarget(t, Options{})
	endpoint, repo := target.Endpoint, target.Repository
	sinkconform.Run(t, func(t *testing.T) sink.Sink {
		return New(Options{
			Endpoint: endpoint, Repository: repo, Protocol: target.Protocol,
			// A private base per case: two cases both loading "load-1" write
			// two different graph IRIs, so the second does not converge on the
			// first's marker.
			Base: "http://alchemy.example/" + randomName(t) + "/",
		})
	})
}
