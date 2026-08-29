// Package model is an HTTP client for OpenAI-compatible model endpoints.
//
// §6 is the whole reason it exists: models are supplied per job, not
// configured globally, because a buyer's LLM, embedding and OCR endpoints are
// their business and a service that hardcodes them only works in the
// environment it was built in. So this package hardcodes no host, no key and
// no model name; it takes an Endpoint and speaks the OpenAI wire shape, which
// is what a gateway, vLLM, Ollama, LiteLLM and the original all answer to.
//
// Two things this package deliberately does not do:
//
//   - It does not retry. §8.2 puts retry and backoff under the budget's
//     coordination, because ten nodes each retrying on their own is the retry
//     storm that a shared endpoint's 429 was warning about. A client that
//     retries privately is a node deciding independently. Do not add one here:
//     the correct place is pkg/budget, which can see every worker.
//   - It does not interpret the model's answer. A server that ignores
//     response_format and replies with a markdown fence is pkg/extract's
//     problem to unwrap; here it is simply the text that came back.
package model

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Endpoint is one model endpoint a job supplied.
type Endpoint struct {
	// Name is the model name. It goes on the wire as "model", it becomes
	// Name(), it lands in provenance (§7.2), and it is the key pkg/budget
	// leases on (§8.2) — those all have to be the same string.
	Name string
	// BaseURL is the OpenAI-compatible root, e.g. https://host/v1. The path
	// this client appends is joined to it, so a gateway that mounts the API
	// under a prefix works without special-casing.
	BaseURL string
	// APIKey is sent as a bearer token when set. It is optional: a local
	// Ollama has no key, and demanding one would make the self-hosted case
	// unreachable.
	APIKey string
	// Options are per-endpoint knobs; see the option tables in options.go. An
	// unknown key is an error at construction rather than an ignored line —
	// a typo that silently changes nothing is §2.1's three-month fuse.
	Options map[string]string
}

// client is what all three models share: where to post, what to send with it,
// and how to turn a non-2xx into an error a caller can act on.
type client struct {
	name    string
	baseURL string
	apiKey  string
	path    string
	headers map[string]string
	http    *http.Client
}

// errConfig is the class of every construction failure, so a caller wiring a
// job from a config file can tell "you configured this wrong" from "the
// endpoint is down" without reading the message.
var errConfig = errors.New("model: endpoint configuration")

// IsConfigError reports whether err is a misconfigured endpoint rather than a
// failing one. The distinction is worth having without reading the message: a
// job whose config is wrong will fail identically on every node and every
// retry, and telling a buyer "your gateway is down" when they typed a knob
// wrong sends them to the wrong team.
func IsConfigError(err error) bool { return errors.Is(err, errConfig) }

func configErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errConfig, fmt.Sprintf(format, args...))
}

// newClient validates the parts of an Endpoint that every model kind needs,
// and the options the given kind of model accepts.
func newClient(e Endpoint, defaultPath string, allowed map[string]bool) (*client, settings, error) {
	if strings.TrimSpace(e.Name) == "" {
		return nil, settings{}, configErrorf("model name is empty; it is what provenance records and what the budget leases on")
	}
	if strings.TrimSpace(e.BaseURL) == "" {
		return nil, settings{}, configErrorf("base URL is empty for model %q", e.Name)
	}
	s, err := parseOptions(e.Options, defaultPath, allowed)
	if err != nil {
		return nil, settings{}, err
	}
	return &client{
		name:    e.Name,
		baseURL: strings.TrimRight(strings.TrimSpace(e.BaseURL), "/"),
		apiKey:  e.APIKey,
		path:    s.path,
		headers: s.headers,
		http:    &http.Client{Timeout: s.timeout},
	}, s, nil
}

func (c *client) Name() string { return c.name }
