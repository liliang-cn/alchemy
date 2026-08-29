package model

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Endpoint.Options is the escape hatch that keeps §6 honest: the endpoint
// belongs to the buyer, and the knobs it needs cannot all be fields on a
// struct in this repository. The convention is flat string keys, because the
// job that carries them arrived as protobuf and will often have been written
// by hand in YAML.
//
// Recognised everywhere:
//
//	path            the path appended to BaseURL, e.g. "/openai/v1/chat" —
//	                must start with "/". For gateways that do not mount the
//	                API where OpenAI does.
//	timeout         a Go duration, e.g. "90s". Default defaultTimeout.
//	header.<Name>   sends the request header <Name> with this value, e.g.
//	                "header.X-Tenant": "acme". Any number of them. It cannot
//	                overwrite Authorization or Content-Type.
//
// Chat models (NewLLM, NewOCR):
//
//	temperature     a float, e.g. "0.2"
//	max_tokens      an integer, e.g. "4096"
//
// Embedding models (NewEmbedder):
//
//	dimensions      an integer, e.g. "768"
//
// Anything else is an error at construction, and so is a knob offered to the
// wrong kind of model — "dimensions" on a chat endpoint changes nothing, and
// an option that silently changes nothing is §2.1's three-month fuse: the
// config says one thing, the endpoint does another, and the gap is found in
// the week somebody wonders why the temperature never mattered.
const (
	optPath        = "path"
	optTimeout     = "timeout"
	optTemperature = "temperature"
	optMaxTokens   = "max_tokens"
	optDimensions  = "dimensions"
	headerPrefix   = "header."
)

// settings is everything Options can say, already parsed. The scalar knobs are
// pointers so that "unset" survives: an endpoint given no temperature must be
// sent no temperature field, because a gateway's own default is a better
// answer than a zero this package invented.
type settings struct {
	path        string
	timeout     time.Duration
	headers     map[string]string
	temperature *float64
	maxTokens   *int
	dimensions  *int
}

// parseOptions reads opts, accepting only the keys allowed for this kind of
// model. defaultPath is used when no path override is given.
func parseOptions(opts map[string]string, defaultPath string, allowed map[string]bool) (settings, error) {
	s := settings{path: defaultPath, timeout: defaultTimeout}

	// Sorted, so that an endpoint with two bad options always reports the same
	// one first. A validation error that changes between runs of the same
	// config is a bug report nobody can reproduce.
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := opts[k]
		if name, ok := strings.CutPrefix(k, headerPrefix); ok {
			name = strings.TrimSpace(name)
			if name == "" {
				return settings{}, configErrorf("option %q: a header option needs a header name after %q", k, headerPrefix)
			}
			canonical := http.CanonicalHeaderKey(name)
			// Authorization and Content-Type are this client's own contract
			// with the endpoint. Letting an option overwrite them turns a
			// config typo into a call that sends the key somewhere else or
			// posts JSON labelled as something it is not.
			if canonical == "Authorization" || canonical == "Content-Type" {
				return settings{}, configErrorf("option %q: %s is set by this client and cannot be overridden", k, canonical)
			}
			if s.headers == nil {
				s.headers = map[string]string{}
			}
			s.headers[canonical] = v
			continue
		}
		if !allowed[k] {
			return settings{}, configErrorf("unknown option %q (this endpoint accepts %s, and header.<Name>)", k, strings.Join(sortedKeys(allowed), ", "))
		}
		var err error
		switch k {
		case optPath:
			if !strings.HasPrefix(v, "/") {
				return settings{}, configErrorf("option %q: %q must start with %q; it is joined to the base URL", k, v, "/")
			}
			s.path = v
		case optTimeout:
			var d time.Duration
			if d, err = time.ParseDuration(v); err != nil {
				return settings{}, configErrorf("option %q: %q is not a duration such as %q", k, v, "90s")
			}
			if d <= 0 {
				return settings{}, configErrorf("option %q: %q is not a positive duration; a call with no bound holds a budget slot forever", k, v)
			}
			s.timeout = d
		case optTemperature:
			var f float64
			if f, err = strconv.ParseFloat(v, 64); err != nil {
				return settings{}, configErrorf("option %q: %q is not a number", k, v)
			}
			s.temperature = &f
		case optMaxTokens:
			var n int
			if n, err = strconv.Atoi(v); err != nil {
				return settings{}, configErrorf("option %q: %q is not an integer", k, v)
			}
			if n <= 0 {
				return settings{}, configErrorf("option %q: %q must be positive", k, v)
			}
			s.maxTokens = &n
		case optDimensions:
			var n int
			if n, err = strconv.Atoi(v); err != nil {
				return settings{}, configErrorf("option %q: %q is not an integer", k, v)
			}
			if n <= 0 {
				return settings{}, configErrorf("option %q: %q must be positive", k, v)
			}
			s.dimensions = &n
		}
	}
	return s, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// chatOptions and embedOptions are the per-kind allow lists. They are the
// reason "dimensions" on a chat endpoint is an error rather than a line that
// does nothing.
var (
	chatOptions  = map[string]bool{optPath: true, optTimeout: true, optTemperature: true, optMaxTokens: true}
	embedOptions = map[string]bool{optPath: true, optTimeout: true, optDimensions: true}
)

// defaultTimeout bounds a call that was configured with none.
//
// It exists because a hung call is worse than a failed one: it holds a
// pkg/budget slot (§8.2) for as long as the endpoint stays silent, so one
// unresponsive gateway drains the cluster's concurrency for every job pointed
// at it. Generous rather than tight, because a vision model transcribing a
// dense page legitimately takes minutes, and a timeout that fires on healthy
// work is a corpus that cannot be read.
const defaultTimeout = 5 * time.Minute
