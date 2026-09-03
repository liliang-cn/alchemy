package dgraph

import (
	"testing"

	"github.com/liliang-cn/alchemy/connectors/internal/sinkconform"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// TestTheSharedConformanceSuitePasses is the sixth store answering the same
// questions as the other five.
//
// It is the only test here that decides whether this connector belongs in the
// module at all. sinkconform was written after the interface rather than before
// it, and it holds the agreement the five connectors reached about what a load
// IS — a name, a digest, three states of a name that already exists, and what
// a retirement means. A sixth store that needed the suite relaxed would be
// telling us §4.1's line was drawn in the wrong place.
//
// Each case gets its own predicate prefix AND its own run namespace, because
// Dgraph collides on both: the data is kept apart by the run, and the schema —
// which is one namespace for the whole cluster — by the prefix.
func TestTheSharedConformanceSuitePasses(t *testing.T) {
	endpoint := liveEndpoint(t)
	sinkconform.Run(t, func(t *testing.T) sink.Sink {
		l := liveLoader(t, Options{Endpoint: endpoint})
		return l
	})
}
