package pgvector

import (
	"testing"

	"github.com/liliang-cn/alchemy/connectors/internal/sinkconform"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// §4 deferred a Sink interface until two real consumers existed. Four do, and
// §4.1 draws the line: the envelope, the identity and the pre-flight are one
// thing written four times, so they move above it; batching, dimension
// binding, index policy and the query surface stay here because the four
// answered them differently and were each right to.
//
// This is the evidence that this store is on the near side of that line.
func TestTheLoaderIsASink(t *testing.T) {
	sinkconform.Run(t, func(t *testing.T) sink.Sink {
		return newFixture(t).open(t, Config{})
	})
}
