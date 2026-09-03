package dgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Talking to Dgraph, and the one thing that makes it different from every
// other store in this module.
//
// DGRAPH ANSWERS HTTP 200 WHEN IT REFUSES. Measured on a live alpha, on both
// endpoints:
//
//	POST /mutate  "this is not rdf at all"   -> 200 {"errors":[{"message":"while lexing …"}]}
//	POST /query   "{ q(func: nonsense(x)) }" -> 200 {"errors":[{"message":"… is not valid."}]}
//
// Every other connector here can trust a status code. This one cannot, and the
// consequence of forgetting is not a crash: it is a load that writes nothing,
// reads nothing and reports success — for a whole corpus, with no error
// anywhere. So there is exactly one function that speaks HTTP, it always
// decodes the body, and a non-empty errors array is ErrRefused regardless of
// what the status line said.
//
// The message is carried into the error rather than dropped. Dgraph names the
// line and column of a bad N-Quad, and a connector that replaced that with
// "the store refused a request" would leave an operator holding a half-written
// graph and nothing to look at.

// envelope is the shape both endpoints answer in.
type envelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// do sends one request and returns the `data` member, or the reason it was
// refused.
func (l *Loader) do(ctx context.Context, path, contentType, body string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.opts.Endpoint+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if l.opts.Token != "" {
		req.Header.Set("X-Dgraph-AccessToken", l.opts.Token)
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: POST %s: %w", ErrRefused, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A megabyte cap on the error path only would be wrong here: this is also
	// how every query result arrives. Sixteen is chosen against a batch of
	// chunks coming back with their text.
	out, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: POST %s: reading the answer: %w", ErrRefused, path, err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		// Not JSON at all. This is where a genuinely broken status code shows
		// up — an HTML error page from a proxy in front of the alpha — so the
		// status goes in the message even though it is not what decides.
		return nil, fmt.Errorf("%w: POST %s: %s: the answer is not Dgraph JSON: %s",
			ErrRefused, path, resp.Status, truncate(string(out)))
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("%w: POST %s: %s", ErrRefused, path, strings.Join(msgs, "; "))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Reached only when Dgraph fails without saying why, which the alpha
		// does for at least one case: a request body larger than its limit.
		// Checked after the errors array rather than before, because the array
		// is the one that carries a reason.
		return nil, fmt.Errorf("%w: POST %s: %s: %s", ErrRefused, path, resp.Status, truncate(string(out)))
	}
	return env.Data, nil
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return s[:500] + "…"
	}
	return s
}

// query runs one DQL query and returns its data member.
func (l *Loader) query(ctx context.Context, dql string) (json.RawMessage, error) {
	return l.do(ctx, "/query", "application/dql", dql)
}

// queryInto runs a query and decodes the data member into v.
func (l *Loader) queryInto(ctx context.Context, dql string, v any) error {
	data, err := l.query(ctx, dql)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

// mutate runs one RDF mutation and commits it.
//
// commitNow because a load is a stream of independent batches and there is no
// transaction spanning them: §8.4 says a large result does not fit in one
// write, and a Dgraph transaction held open across a four-hundred-thousand
// record import is a conflict-detection structure growing for the length of the
// load. What that costs is that a load interrupted halfway leaves what it had
// written, which is the same bargain every other connector in this module makes
// and the reason a re-load has to converge rather than duplicate.
func (l *Loader) mutate(ctx context.Context, rdf string) error {
	if strings.TrimSpace(rdf) == "" {
		return nil
	}
	_, err := l.do(ctx, "/mutate?commitNow=true", "application/rdf", rdf)
	return err
}

// alter sends a schema fragment.
func (l *Loader) alter(ctx context.Context, schema string) error {
	body, err := json.Marshal(map[string]string{"schema": schema})
	if err != nil {
		return err
	}
	_, err = l.do(ctx, "/alter", "application/json", string(body))
	return err
}

// set wraps statements in a plain mutation block.
func set(stmts string) string {
	if strings.TrimSpace(stmts) == "" {
		return ""
	}
	return "{\n set {\n" + stmts + " }\n}\n"
}

// upsert wraps statements in an upsert block whose query binds `v` to the node
// with this xid.
//
// It is what makes a re-load converge instead of duplicating: uid(v) resolves
// to the existing node when the query matched and mints one when it did not.
// Measured — the same upsert twice returns the same uid and creates nothing.
func (l *Loader) upsert(xid, stmts string) string {
	if strings.TrimSpace(stmts) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("upsert {\n query { v as var(func: eq(")
	b.WriteString(l.pred(keyXID))
	b.WriteString(", ")
	b.WriteString(literal(xid))
	b.WriteString(")) }\n mutation { set {\n")
	b.WriteString(stmts)
	b.WriteString(" } }\n}\n")
	return b.String()
}
