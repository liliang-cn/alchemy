package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// postJSON sends body to the client's endpoint and decodes the reply into out.
//
// ctx reaches the transport rather than only the dial, which is what makes a
// cancelled job stop paying for a call that is still streaming: net/http aborts
// the body read too when the request's context is done, so the decode below is
// cancellable and not just the handshake.
//
// There is no retry here and there must not be one. §8.2 coordinates backoff
// through the budget's lease so that every worker on an endpoint waits
// together; a retry loop inside this function is one node deciding
// independently, which is the retry storm the budget exists to prevent.
func (c *client) postJSON(ctx context.Context, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("model %q: encoding the request: %w", c.name, err)
	}
	url := c.baseURL + c.path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("model %q: building the request: %w", c.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// A call the caller itself cancelled is not worth trying again: the
		// job is going away. A refused connection or an expired client
		// timeout is, which is why the two are separated here, where the
		// caller's context is still in hand.
		return &TransportError{Model: c.name, URL: url, Err: err, retryable: ctx.Err() == nil}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.statusError(resp, url)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		// The context is checked first so that a body the caller cancelled
		// mid-read is reported as a cancel rather than as a provider that
		// sends malformed JSON.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return &TransportError{Model: c.name, URL: url, Err: ctxErr, retryable: false}
		}
		return &TransportError{Model: c.name, URL: url, Err: fmt.Errorf("decoding the reply: %w", err), retryable: false}
	}
	return nil
}

// statusError turns a non-2xx reply into an APIError, reading only as much of
// the body as is worth keeping.
func (c *client) statusError(resp *http.Response, url string) error {
	// One byte past the limit is read on purpose: it is the only way to know
	// the body was longer than what was kept, and a truncated message that
	// does not say so reads as the provider's whole answer.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	truncated := len(body) > maxBodyBytes
	if truncated {
		body = body[:maxBodyBytes]
	}
	return &APIError{
		Status:     resp.StatusCode,
		Model:      c.name,
		URL:        url,
		Body:       redact(strings.TrimSpace(string(body)), c.apiKey),
		Truncated:  truncated,
		retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
}
