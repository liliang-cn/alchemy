package extract

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/cache"
)

// refusingLLM is a model that cannot be called. It is how a test asserts "no
// model calls at all" without counting calls on a mock: a count is a claim
// about an implementation, whereas an endpoint that fails every request makes
// the assertion about the result — if anything reached the model, the chunk
// comes back unread and the two runs differ.
type refusingLLM struct {
	name string
	t    *testing.T
}

func (r *refusingLLM) Name() string { return r.name }

func (r *refusingLLM) Complete(context.Context, alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	r.t.Helper()
	r.t.Error("the model was called for a chunk the cache already held")
	return alchemy.LLMResponse{}, context.Canceled
}

// cachedChunks is the corpus the tests in this file share: two chunks, where
// the second names a thing the first introduced without typing it. That
// cross-chunk end is the case worth pinning — its resolution depends on
// entities from a chunk the cached one never saw.
func cachedChunks() []alchemy.Chunk {
	return testChunks("SuperAI is a cluster in eu.", "node-a runs it.")
}

func cachedReplies() map[int]string {
	return map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI","attributes":{"region":"eu"},"confidence":0.82}],"relations":[]}`,
		1: `{"entities":[{"type":"Node","name":"node-a","confidence":0.5}],
		     "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","to":"node-a","to_type":"Node","confidence":0.7}]}`,
	}
}

// TestAResumedJobDoesNotRebuyWhatItAlreadyHas is DESIGN.md §8.2's sentence in
// executable form: "paying twice for the identical call after a crash is a
// bug". The second run is given a model that fails every call, so the only way
// it can produce the first run's graph is by not calling one.
func TestAResumedJobDoesNotRebuyWhatItAlreadyHas(t *testing.T) {
	const model = "gemini-3.6-flash-high"
	c := cache.NewMemory(16)

	opts := testOptions(&fakeLLM{name: model, replies: cachedReplies(), tokens: 11})
	opts.Cache = c
	first, err := Extract(context.Background(), cachedChunks(), opts)
	if err != nil {
		t.Fatalf("first Extract: %v", err)
	}

	resumed := testOptions(&refusingLLM{name: model, t: t})
	resumed.Cache = c
	second, err := Extract(context.Background(), cachedChunks(), resumed)
	if err != nil {
		t.Fatalf("resumed Extract: %v", err)
	}

	if !reflect.DeepEqual(first.Entities, second.Entities) {
		t.Errorf("entities differ between a fresh run and a resumed one:\n%#v\n%#v", first.Entities, second.Entities)
	}
	if !reflect.DeepEqual(first.Relations, second.Relations) {
		t.Errorf("relations differ between a fresh run and a resumed one:\n%#v\n%#v", first.Relations, second.Relations)
	}
	if len(second.Unread) != 0 {
		t.Errorf("the resumed run read nothing from the cache: %#v", second.Unread)
	}
	if second.ChunksEmpty != first.ChunksEmpty {
		t.Errorf("ChunksEmpty = %d, want %d", second.ChunksEmpty, first.ChunksEmpty)
	}
}

// poison is an entry no model would ever have produced. Planting one under a
// deliberately wrong address and then asserting it is not returned is how these
// tests observe a miss: a hit is visible in the graph, so a miss is too.
func poison() cache.Entry {
	return cache.Entry{
		Entities: []alchemy.Entity{{
			ID: "cluster:poison", Type: "Cluster", Name: "POISON",
			Provenance: alchemy.Provenance{Producer: alchemy.ProducerLLMExtract},
		}},
		Tokens: 999,
	}
}

// TestAChangedKeyIsAMiss is DESIGN.md §8.2's reason for keying on the prompt
// version: "a cache that survives a prompt change is a cache that returns the
// old prompt's opinion." The same holds for the model and for the ontology
// version, so all three are varied here — and the unvaried case is run too,
// because a test that only ever asserts a miss would also pass against a cache
// that never hits at all.
func TestAChangedKeyIsAMiss(t *testing.T) {
	const model = "gemini-3.6-flash-high"
	const ontologyID = "sds@3"
	text := "SuperAI is a cluster in eu."

	cases := []struct {
		name    string
		planted cache.Key
		wantHit bool
		why     string
	}{
		{
			name:    "the address the run will compute",
			planted: cache.Key{Chunk: text, Model: model, Ontology: ontologyID, Prompt: PromptVersion},
			wantHit: true,
			why:     "the control: if this does not hit, the misses below prove nothing",
		},
		{
			name:    "another prompt version",
			planted: cache.Key{Chunk: text, Model: model, Ontology: ontologyID, Prompt: "extract/0"},
			why:     "a cache that survives a prompt change returns the old prompt's opinion",
		},
		{
			name:    "another ontology version",
			planted: cache.Key{Chunk: text, Model: model, Ontology: "sds@2", Prompt: PromptVersion},
			why:     "the vocabulary constrains the extraction, so it constrains the answer",
		},
		{
			name:    "another model",
			planted: cache.Key{Chunk: text, Model: "gemini-3.6-pro", Ontology: ontologyID, Prompt: PromptVersion},
			why:     "another model answers differently, and provenance records which",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := cache.NewMemory(8)
			if err := c.Put(context.Background(), tc.planted, poison()); err != nil {
				t.Fatalf("Put: %v", err)
			}
			opts := testOptions(&fakeLLM{name: model, replies: cachedReplies()})
			opts.Cache = c
			got, err := Extract(context.Background(), testChunks(text), opts)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if len(got.Entities) != 1 {
				t.Fatalf("want exactly one entity, got %#v", got.Entities)
			}
			if hit := got.Entities[0].Name == "POISON"; hit != tc.wantHit {
				t.Fatalf("cache hit = %v, want %v (%s); the run returned %q",
					hit, tc.wantHit, tc.why, got.Entities[0].Name)
			}
		})
	}
}

// brokenCache is the shared store of §8.3 with the network gone: every call
// fails, in both directions.
type brokenCache struct{ err error }

func (b brokenCache) Get(context.Context, cache.Key) (cache.Entry, bool, error) {
	return cache.Entry{}, false, b.err
}

func (b brokenCache) Put(context.Context, cache.Key, cache.Entry) error { return b.err }

// TestABrokenCacheDoesNotBreakTheJob. cache.Fetch already contracts this — "the
// worst a broken cache may do is make the job cost what it would have cost
// without one" — and this is that contract observed from the far end, because a
// contract kept by a helper nobody reached is not a contract kept. An
// unreachable store must cost money, never a corpus.
func TestABrokenCacheDoesNotBreakTheJob(t *testing.T) {
	llm := &fakeLLM{name: "gemini-3.6-flash-high", replies: cachedReplies(), tokens: 7}
	opts := testOptions(llm)
	opts.Cache = brokenCache{err: errors.New("cache store: connection refused")}

	got, err := Extract(context.Background(), cachedChunks(), opts)
	if err != nil {
		t.Fatalf("a broken cache failed the job: %v", err)
	}

	withoutCache := testOptions(&fakeLLM{name: "gemini-3.6-flash-high", replies: cachedReplies(), tokens: 7})
	want, err := Extract(context.Background(), cachedChunks(), withoutCache)
	if err != nil {
		t.Fatalf("uncached Extract: %v", err)
	}
	if !reflect.DeepEqual(got.Entities, want.Entities) {
		t.Errorf("entities differ from the same run with no cache:\n%#v\n%#v", got.Entities, want.Entities)
	}
	if !reflect.DeepEqual(got.Relations, want.Relations) {
		t.Errorf("relations differ from the same run with no cache:\n%#v\n%#v", got.Relations, want.Relations)
	}
	// The whole cost of a broken cache: the calls it did not save.
	if !reflect.DeepEqual(got.ModelCalls, want.ModelCalls) {
		t.Errorf("ModelCalls = %#v, want %#v", got.ModelCalls, want.ModelCalls)
	}
	if len(got.Unread) != 0 {
		t.Errorf("a cache error was reported as an unreadable chunk: %#v", got.Unread)
	}
}

// TestACachedChunkIsNotReportedAsSpend. §7.2 makes the call count the one
// number a caller is promised is honest, and a cache can make it dishonest in
// the direction nobody looks for: a run that re-reported the call its cached
// answer once cost would bill a caller for money this job did not spend, and
// the cost report §7.2 exists for would overstate rather than understate.
//
// The other half of the assertion matters as much. A partially cached run still
// reports the calls it did make, so "cheaper than last time" never becomes
// "free", which would be the same lie told the other way.
func TestACachedChunkIsNotReportedAsSpend(t *testing.T) {
	const model = "gemini-3.6-flash-high"
	c := cache.NewMemory(16)
	chunks := cachedChunks()

	// The first run buys chunk 0 only, which is the crash §8.2 describes: a job
	// that got partway and stopped.
	first := testOptions(&fakeLLM{name: model, replies: cachedReplies(), tokens: 30})
	first.Cache = c
	if _, err := Extract(context.Background(), chunks[:1], first); err != nil {
		t.Fatalf("first Extract: %v", err)
	}

	// The resumed run is given a model that can answer chunk 1 and nothing
	// else: asking it for chunk 0 fails, so a re-bought chunk 0 shows up as an
	// unread rather than as a silently duplicated call.
	resumed := testOptions(&fakeLLM{
		name:    model,
		replies: map[int]string{1: cachedReplies()[1]},
		tokens:  30,
	})
	resumed.Cache = c
	got, err := Extract(context.Background(), chunks, resumed)
	if err != nil {
		t.Fatalf("resumed Extract: %v", err)
	}
	if len(got.Unread) != 0 {
		t.Fatalf("the resumed run re-bought a cached chunk: %#v", got.Unread)
	}

	want := []alchemy.ModelCall{{Model: model, Stage: "extract", Calls: 1, Tokens: 30}}
	if !reflect.DeepEqual(got.ModelCalls, want) {
		t.Errorf("ModelCalls = %#v, want %#v (chunk 0 came from the cache and cost nothing)", got.ModelCalls, want)
	}

	// And a run that bought nothing reports nothing at all, rather than a
	// zero-call entry that reads as a model having been involved.
	fully := testOptions(&refusingLLM{name: model, t: t})
	fully.Cache = c
	all, err := Extract(context.Background(), chunks, fully)
	if err != nil {
		t.Fatalf("fully cached Extract: %v", err)
	}
	if len(all.ModelCalls) != 0 {
		t.Errorf("ModelCalls = %#v, want none: this run called no model", all.ModelCalls)
	}
}

// TestACachedRelationResolvesItsEndsInTheJobItIsUsedIn.
//
// This is the one place the cache does not sit where it looks like it should.
// A chunk's entities are a function of that chunk, so they can be stored under
// its address unchanged. A chunk's relations are not: an end the model wrote
// without a type is matched by name against every entity the whole job found,
// so the same paragraph resolves to a Cluster in one corpus and to a Node in
// another. Storing the finished IDs would freeze the first corpus's answer into
// the address and hand it to the second — a resumed job quietly producing a
// different graph from a fresh one, which is the failure a cache exists to
// prevent rather than to cause.
//
// So what is stored is the resolution the chunk alone justifies, and the
// job-wide half is redone on the way out.
func TestACachedRelationResolvesItsEndsInTheJobItIsUsedIn(t *testing.T) {
	const model = "gemini-3.6-flash-high"
	const shared = "SuperAI mentions node-a."
	mentions := `{"entities":[],"relations":[{"type":"MENTIONS","from":"SuperAI","to":"node-a","to_type":"Node"}]}`

	// The first corpus types SuperAI as a Cluster, and pays for both chunks.
	c := cache.NewMemory(16)
	asCluster := testOptions(&fakeLLM{name: model, replies: map[int]string{
		0: mentions,
		1: `{"entities":[{"type":"Cluster","name":"SuperAI"}],"relations":[]}`,
	}})
	asCluster.Cache = c
	if _, err := Extract(context.Background(), testChunks(shared, "SuperAI is a cluster."), asCluster); err != nil {
		t.Fatalf("first corpus: %v", err)
	}

	// The second corpus contains the same paragraph and types SuperAI as a
	// Node. Chunk 0 is a cache hit; chunk 1 is bought.
	second := testChunks(shared, "SuperAI is a node.")
	asNode := func(cc cache.Cache) Options {
		o := testOptions(&fakeLLM{name: model, replies: map[int]string{
			0: mentions,
			1: `{"entities":[{"type":"Node","name":"SuperAI"}],"relations":[]}`,
		}})
		o.Cache = cc
		return o
	}

	cached, err := Extract(context.Background(), second, asNode(c))
	if err != nil {
		t.Fatalf("second corpus, cached: %v", err)
	}
	fresh, err := Extract(context.Background(), second, asNode(nil))
	if err != nil {
		t.Fatalf("second corpus, uncached: %v", err)
	}

	if !reflect.DeepEqual(cached.Relations, fresh.Relations) {
		t.Fatalf("a cached chunk produced a different edge from a fresh one:\ncached %#v\nfresh  %#v",
			cached.Relations, fresh.Relations)
	}
	if len(cached.Relations) != 1 {
		t.Fatalf("want one relation, got %#v", cached.Relations)
	}
	// Stated as a literal as well as by comparison: two runs that agree on the
	// wrong ID would satisfy the comparison and nothing else.
	if got := cached.Relations[0].From; got != "node:superai" {
		t.Errorf("relation starts at %q, want %q — the end was resolved against the corpus that paid for the chunk rather than the one reading it",
			got, "node:superai")
	}
}

// TestACachedChunkIsCitedToTheDocumentItWasReadIn.
//
// The address is a hash of the text (§8.2), so one paragraph that appears in
// two documents — a boilerplate section, a spec quoted in a report, the same
// file uploaded twice under two names — is one entry. What the model said about
// it is the same in both. Where it was read is not, and §5b promises every
// entity can name the source and the chunk it came from.
//
// Returning the stored provenance would answer that promise with a citation
// pointing at a real document and the wrong one, which is worse than no
// citation: it is checkable, and it checks out. So the model's opinion is what
// the cache keeps and the provenance is restated for the chunk in hand.
func TestACachedChunkIsCitedToTheDocumentItWasReadIn(t *testing.T) {
	const model = "gemini-3.6-flash-high"
	const shared = "SuperAI is a cluster in eu."
	reply := map[int]string{
		0: `{"entities":[{"type":"Cluster","name":"SuperAI","confidence":0.82}],
		     "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","from_type":"Cluster","to":"node-a","to_type":"Node"}]}`,
	}

	c := cache.NewMemory(8)
	first := testOptions(&fakeLLM{name: model, replies: reply})
	first.Cache = c
	inA := []alchemy.Chunk{{Index: 0, Text: shared, Source: "doc-a.md", Strategy: "heading"}}
	if _, err := Extract(context.Background(), inA, first); err != nil {
		t.Fatalf("first document: %v", err)
	}

	// The same paragraph, in another document, cut by another strategy, at
	// another index. Nothing here may reach the model.
	inB := []alchemy.Chunk{{Index: 3, Text: shared, Source: "doc-b.md", Strategy: "paragraph"}}
	second := testOptions(&refusingLLM{name: model, t: t})
	second.Cache = c
	got, err := Extract(context.Background(), inB, second)
	if err != nil {
		t.Fatalf("second document: %v", err)
	}

	want := alchemy.Provenance{
		Source:   "doc-b.md",
		Chunk:    3,
		Producer: alchemy.ProducerLLMExtract,
		Model:    model,
		Ontology: "sds@3",
		Chunking: "paragraph",
		// The model's own number, which is the one thing here it did say.
		Confidence: 0.82,
	}
	if len(got.Entities) != 1 {
		t.Fatalf("want one entity, got %#v", got.Entities)
	}
	if got.Entities[0].Provenance != want {
		t.Errorf("entity provenance =\n%#v\nwant\n%#v", got.Entities[0].Provenance, want)
	}
	if len(got.Relations) != 1 {
		t.Fatalf("want one relation, got %#v", got.Relations)
	}
	want.Confidence = 0
	if got.Relations[0].Provenance != want {
		t.Errorf("relation provenance =\n%#v\nwant\n%#v", got.Relations[0].Provenance, want)
	}
}
