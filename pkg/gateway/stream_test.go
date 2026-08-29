package gateway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/service"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// §6 chose a server stream over polling because "progress on a long job is a
// stream". Over HTTP the stream survives as newline-delimited JSON, which is
// grpc-gateway's default and is kept: one object per line, each wrapping the
// message in {"result": …}, so a caller reading with a line reader sees the
// events in the order the operator needed them in.
func TestWatchJobStreamsOneJSONObjectPerLine(t *testing.T) {
	f, release := servingAHeldJob(t)
	defer close(release)
	id := f.aDDLJob(t)

	resp := f.do(t, http.MethodGet, "/v1/jobs/"+id+"/events", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	lines := readLines(t, resp, 2)
	for i, line := range lines {
		var frame struct {
			Result map[string]any `json:"result"`
			Error  map[string]any `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("line %d is not one JSON object (%q): %v", i, line, err)
		}
		if frame.Result == nil {
			t.Fatalf("line %d carries no result: %q", i, line)
		}
		if _, ok := frame.Result["state"]; !ok {
			t.Errorf("line %d is not a JobEvent: %q", i, line)
		}
	}
	// §7.2: the running model-call total is what an operator watches, so it has
	// to survive the translation rather than being an absent key.
	var second struct {
		Result map[string]any `json:"result"`
	}
	_ = json.Unmarshal([]byte(lines[1]), &second)
	if _, ok := second.Result["model_calls"]; !ok {
		t.Errorf("the event carries no model_calls: %q", lines[1])
	}
}

// The same events, framed for EventSource. §6 called SSE "a stream pretending
// to be a response" and it still is — it is offered because a browser has no
// other way to read a stream, and §6's own reason for a gateway is that
// browsers exist. Nothing about the events changes: same order, same objects.
func TestWatchJobSpeaksServerSentEventsWhenAsked(t *testing.T) {
	f, release := servingAHeldJob(t)
	defer close(release)
	id := f.aDDLJob(t)

	resp := f.do(t, http.MethodGet, "/v1/jobs/"+id+"/events", testToken, nil,
		"Accept", "text/event-stream")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	// A proxy that buffers an SSE stream turns it back into a response, which
	// is the one failure this framing exists to avoid.
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-cache") {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}

	lines := readLines(t, resp, 4)
	// SSE frames are "data: {json}" followed by a blank line.
	if !strings.HasPrefix(lines[0], "data: ") {
		t.Fatalf("first line = %q, which is not an SSE data frame", lines[0])
	}
	if lines[1] != "" {
		t.Errorf("second line = %q, want the blank line that ends an SSE frame", lines[1])
	}
	var frame struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[0], "data: ")), &frame); err != nil {
		t.Fatalf("the frame's payload is not JSON (%q): %v", lines[0], err)
	}
	if frame.Result == nil {
		t.Errorf("the frame carries no result: %q", lines[0])
	}
}

// §8.4's paged result, over HTTP. page_size is a query parameter because the
// generated mapping binds every request field the path did not take, which is
// how a caller says "page it smaller" without a second route.
func TestStreamResultPagesOverHTTP(t *testing.T) {
	f := serve(t, harness{run: func(context.Context, string, service.JobSpec, chan<- service.Event, service.Inbox) (alchemy.Result, error) {
		return alchemy.Result{
			Entities: []alchemy.Entity{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}, {ID: "c", Name: "C"}},
			Counts:   alchemy.Counts{Entities: 3},
		}, nil
	}})
	id := f.aDDLJob(t)
	f.awaitState(t, id, alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	resp := f.do(t, http.MethodGet, "/v1/jobs/"+id+"/result:stream?page_size=1", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	lines := readLines(t, resp, 3)

	var first struct {
		Result struct {
			Page   float64        `json:"page"`
			Last   bool           `json:"last"`
			Counts map[string]any `json:"counts"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("page 0 (%q): %v", lines[0], err)
	}
	// §8.4: the summary rides on the first page, so an operator decides
	// whether to read a large graph before paying for it.
	if first.Result.Counts == nil {
		t.Errorf("page 0 carries no counts: %q", lines[0])
	}
	if first.Result.Last {
		t.Errorf("page 0 says it is the last of three")
	}
	if !strings.Contains(lines[2], `"last":true`) {
		t.Errorf("page 2 does not say it is the last: %q", lines[2])
	}
}

// servingAHeldJob is a fixture whose runner announces one event and then waits,
// so a stream has something to carry and does not end before it is read.
func servingAHeldJob(t *testing.T) (*fixture, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	f := serve(t, harness{run: func(ctx context.Context, _ string, _ service.JobSpec, events chan<- service.Event, _ service.Inbox) (alchemy.Result, error) {
		events <- service.Event{At: time.Now(), Stage: "read", ModelCalls: 3}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return alchemy.Result{}, nil
	}})
	return f, release
}

// readLines reads n lines off a live stream, failing rather than hanging if
// the server stops talking.
func readLines(t *testing.T, resp *http.Response, n int) []string {
	t.Helper()
	type line struct {
		text string
		err  error
	}
	ch := make(chan line, n)
	go func() {
		r := bufio.NewReader(resp.Body)
		for i := 0; i < n; i++ {
			s, err := r.ReadString('\n')
			ch <- line{strings.TrimRight(s, "\r\n"), err}
			if err != nil {
				return
			}
		}
	}()

	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		select {
		case l := <-ch:
			if l.err != nil {
				t.Fatalf("reading line %d: %v (got %q so far)", i, l.err, out)
			}
			out = append(out, l.text)
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out after %d of %d lines: %q", i, n, out)
		}
	}
	return out
}
