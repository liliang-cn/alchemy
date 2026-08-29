package pipeline

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/cache"
)

// refusingLLM answers nothing and reports the attempt. It is named to match
// the fake that filled the cache, because the model name is part of the
// content address (§8.2): a refusal under a different name would be a miss,
// and the test would then be proving that a changed key misses rather than
// that a resumed job does not re-buy.
type refusingLLM struct {
	name string
	t    *testing.T
}

func (r *refusingLLM) Name() string { return r.name }

func (r *refusingLLM) Complete(context.Context, alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	r.t.Helper()
	r.t.Error("the pipeline re-bought a chunk the cache already held")
	return alchemy.LLMResponse{}, fmt.Errorf("must not be called")
}

// TestTheJobsCacheReachesTheExtractor. §8.2's cache is keyed inside the
// extractor, but the thing that owns a job — and therefore the thing that
// knows a job is being resumed — is this package's caller. A Request field
// that never arrived would be a guarantee stated in the API and delivered
// nowhere, which is the failure this test exists for: the assertion is not
// that pkg/cache works, it is that the wire between here and there is
// connected.
func TestTheJobsCacheReachesTheExtractor(t *testing.T) {
	c := cache.NewMemory(64)

	req := regionRequest(t, doc("eu.md", docEU))
	req.Cache = c
	first, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}

	resumed := regionRequest(t, doc("eu.md", docEU))
	resumed.Cache = c
	resumed.Models.LLM = &refusingLLM{name: "fake-llm", t: t}
	second, err := Run(context.Background(), resumed, nil)
	if err != nil {
		t.Fatalf("resumed Run: %v", err)
	}

	if !reflect.DeepEqual(first.Entities, second.Entities) {
		t.Errorf("the resumed job produced a different graph:\n%#v\n%#v", first.Entities, second.Entities)
	}
	for _, call := range second.ModelCalls {
		if call.Stage == stageExtract {
			t.Errorf("the resumed job reported extraction spend it did not incur: %+v", call)
		}
	}
	if len(second.Unread) != 0 {
		t.Errorf("the resumed job read nothing from the cache: %#v", second.Unread)
	}
}
