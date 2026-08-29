package budget_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/budget"
)

// A stage is budgeted by being handed a wrapped model, not by importing this
// package.
func ExampleWrapLLM() {
	b, err := budget.NewLocal(budget.Config{
		Limit:    20, // what the endpoint permits, shared by every worker
		PerModel: map[string]int{"text-embedding-4": 100},
	})
	if err != nil {
		panic(err)
	}

	var caller alchemy.LLM = &fakeLLM{name: "gemini-3.6-flash-high"}
	model := budget.WrapLLM(caller, b)

	// The name is unchanged, so provenance and the cost report still agree.
	fmt.Println(model.Name())
	resp, _ := model.Complete(context.Background(), alchemy.LLMRequest{Prompt: "hello"})
	fmt.Println(resp.Text, resp.Tokens)
	// Output:
	// gemini-3.6-flash-high
	// hello 7
}

// httpModelError is the shape an adapter for an HTTP provider already has. It
// tells the budget about a 429 by answering two questions, without importing
// pkg/budget at all — the detection is structural, never a match on the text of
// the message.
type httpModelError struct {
	status int
	header http.Header
}

func (e httpModelError) Error() string     { return fmt.Sprintf("model endpoint: HTTP %d", e.status) }
func (e httpModelError) RateLimited() bool { return e.status == http.StatusTooManyRequests }

func (e httpModelError) RetryAfter() time.Duration {
	// Whatever the endpoint said, or 0 when it said nothing.
	if v := e.header.Get("Retry-After"); v != "" {
		if secs, err := time.ParseDuration(v + "s"); err == nil {
			return secs
		}
	}
	return 0
}

func ExampleIsRateLimit() {
	err := httpModelError{status: 429, header: http.Header{"Retry-After": {"12"}}}
	after, ok := budget.IsRateLimit(err)
	fmt.Println(ok, after)

	// Callers who would rather not implement anything can wrap instead.
	after, ok = budget.IsRateLimit(budget.TooFast(err, 0))
	fmt.Println(ok, after)

	// And nothing else is treated as a rate limit, whatever it says.
	_, ok = budget.IsRateLimit(fmt.Errorf("429 too many requests"))
	fmt.Println(ok)
	// Output:
	// true 12s
	// true 0s
	// false
}
