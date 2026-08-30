package neo4j

import (
	"testing"

	"github.com/liliang-cn/alchemy/connectors/internal/sinkconform"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// §4 deferred a Sink interface until two real consumers existed. Four do, and
// §4.1 draws the line: the envelope, the identity and the pre-flight are one
// thing written four times, so they move above it; MERGE-on-(run, id), the
// label grouping Cypher forces, quoteIdent and the reserved prefix stay here
// because they are a property graph's and nothing else's.
func TestTheLoaderIsASink(t *testing.T) {
	sinkconform.Run(t, func(t *testing.T) sink.Sink {
		return liveLoader(t, Options{})
	})
}
