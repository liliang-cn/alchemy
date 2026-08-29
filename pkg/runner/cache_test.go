package runner

import (
	"context"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/cache"
	"github.com/liliang-cn/alchemy/pkg/extract"
	"github.com/liliang-cn/alchemy/pkg/service"
)

// recordingCache is a cache that stores nothing and remembers every address it
// was asked about. A memory cache would answer the question "does a second run
// cost less" and this one answers the question this package is responsible for:
// did the store the binary built reach the stage that needs it, under the key
// §8.2 specifies.
type recordingCache struct {
	mu   sync.Mutex
	keys []cache.Key
}

func (r *recordingCache) Get(_ context.Context, k cache.Key) (cache.Entry, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, k)
	return cache.Entry{}, false, nil
}

func (r *recordingCache) Put(context.Context, cache.Key, cache.Entry) error { return nil }

func (r *recordingCache) seen() []cache.Key {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]cache.Key(nil), r.keys...)
}

// TestTheConfiguredCacheReachesTheExtraction. §8.2's cache is a property of the
// deployment — a shared store in a cluster, an in-process one on a single node
// — so the binary builds it and this package carries it in. A Config field that
// went no further would be an operator's setting that changes nothing, which is
// worse than no setting at all: the bill stays the same and the configuration
// says it should not have.
func TestTheConfiguredCacheReachesTheExtraction(t *testing.T) {
	c := &recordingCache{}
	r, err := New(Config{Factory: &recordingFactory{}, Cache: c})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	spec := service.JobSpec{
		Sources:  []service.Source{spool(t, alchemy.SourceDocument, "architecture.md", "# SuperAI\n\nSuperAI is a cluster.\n")},
		Ontology: proseOntology,
		Models:   service.Models{LLM: service.Model{Name: "gpt"}},
	}

	events, finish := collect(t)
	if _, err := r.Run(context.Background(), "job-cache", spec, events, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	finish()

	keys := c.seen()
	if len(keys) == 0 {
		t.Fatal("the extraction never consulted the configured cache")
	}
	k := keys[0]
	if k.Chunk == "" {
		t.Errorf("the address does not include the chunk text: %+v", k)
	}
	if k.Model != "gpt" || k.Ontology != "sds@1" || k.Prompt != extract.PromptVersion {
		t.Errorf("address = %+v, want the job's model, its ontology version and %q",
			k, extract.PromptVersion)
	}
}
