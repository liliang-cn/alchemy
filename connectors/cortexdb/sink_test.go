package cortexdb

import (
	"testing"

	"github.com/liliang-cn/alchemy/connectors/internal/sinkconform"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// §4 deferred a Sink interface until two real consumers existed. Four do, and
// §4.1 draws the line: the envelope, the identity and the pre-flight are one
// thing written four times, so they move above it; CortexDB's own edge identity
// of (from, to, type, document), its reserved property names, its document
// shape and its ontology guards stay here because they are the store's rule and
// a copy of them would go stale in silence.
func TestTheLoaderIsASink(t *testing.T) {
	sinkconform.Run(t, func(t *testing.T) sink.Sink {
		return openLocal(t, Options{})
	})
}
