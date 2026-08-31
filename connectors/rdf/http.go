package rdf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This file is the whole of what this connector needs from GraphDB, and it is
// three requests.
//
// GraphDB speaks the RDF4J HTTP protocol, which is a W3C-adjacent standard
// rather than a vendor API, so what is here is portable to any RDF4J server —
// and that is the reason for using it over GraphDB's own JSON REST endpoints.
// The one place the two diverge is repository creation, where the JSON API
// answers 400 (`Missing parameter Default namespaces for imports`) and the
// RDF4J route works; that route is exercised by the live tests, which are the
// only thing in this repository that creates a repository at all.
//
// net/http and encoding/json, and no RDF library. A writer is small and
// completely specified (see turtle.go); a parser is not, and nothing here needs
// one, because every answer comes back as SPARQL results in JSON.

// defaultTimeout bounds one request. It is generous rather than tight because
// a batch of two hundred and fifty chunks of a page each is a real request,
// and a load that dies at batch nine on a timeout leaves a half-written graph
// for no reason. It exists at all because http.DefaultClient has no timeout,
// and a load hung on a socket is a load nothing will ever finish or abort.
const defaultTimeout = 2 * time.Minute

func (l *Loader) httpClient() *http.Client {
	if l.opts.HTTPClient != nil {
		return l.opts.HTTPClient
	}
	return &http.Client{Timeout: defaultTimeout}
}

// repoURL is the SPARQL endpoint of the configured repository.
func (l *Loader) repoURL() string {
	return strings.TrimSuffix(l.opts.Endpoint, "/") + "/repositories/" + url.PathEscape(l.opts.Repository)
}

// do sends one request and reads the answer, or the reason it was refused.
//
// The body of a failure is carried into the error rather than dropped. GraphDB
// reports a Turtle parse error with a line and a column, and a connector that
// replaced that with "400 Bad Request" would leave an operator holding a
// half-written graph and a status code — which is exactly the moment the detail
// is worth having.
func (l *Loader) do(ctx context.Context, method, u, contentType, accept, body string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rdf: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if l.opts.User != "" {
		req.SetBasicAuth(l.opts.User, l.opts.Password)
	}
	resp, err := l.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("rdf: %s %s: %w", method, u, err)
	}
	defer resp.Body.Close()
	// Bounded, because an error page from a proxy in front of the store can be
	// a megabyte of HTML and it would all end up in one error string.
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("rdf: reading the answer to %s %s: %w", method, u, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %s %s: %s: %s", ErrEndpoint, method, u, resp.Status, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// addTurtle writes statements into one named graph.
//
// The graph is a query parameter rather than part of the document, because
// Turtle has no way to say which graph a statement belongs to — that is TriG,
// which would be a second serialisation to write and to escape. The RDF4J
// protocol's `context` parameter says it once for the whole request, which is
// what a load wants anyway: every statement in a batch belongs to the same
// load.
//
// POST rather than PUT: PUT on this endpoint replaces the graph's contents, so
// the second batch of a load would delete the first.
func (l *Loader) addTurtle(ctx context.Context, graph, body string) error {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	u := l.repoURL() + "/statements?context=" + url.QueryEscape("<"+graph+">")
	// Turtle-star: the annotation syntax this connector's whole design rests
	// on. GraphDB parses it under text/turtle, which is what the media type
	// registration says it should — RDF-star is part of Turtle in RDF 1.2 and
	// GraphDB has accepted it since 10.
	_, err := l.do(ctx, http.MethodPost, u, "text/turtle", "", body)
	return err
}

// update runs a SPARQL Update. It is used for exactly two things — dropping a
// load's graph and flipping the completion marker — and both are named at their
// call sites, because an update is the only kind of request in this package
// that can remove something.
func (l *Loader) update(ctx context.Context, sparql string) error {
	_, err := l.do(ctx, http.MethodPost, l.repoURL()+"/statements", "application/sparql-update", "", sparql)
	return err
}

// binding is one cell of a SPARQL result.
//
// Value is the lexical form, which is what every decoder in this package wants:
// an integer arrives as "20" with a datatype beside it, and re-parsing the
// lexical form is the same work as trusting a typed unmarshal would have been.
// Type is kept because it is the only way to tell an IRI from a literal that
// happens to look like one, which the walk needs when a subject is a quoted
// triple.
type binding struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	Datatype string `json:"datatype,omitempty"`
	Lang     string `json:"xml:lang,omitempty"`
}

type sparqlResults struct {
	Results struct {
		Bindings []map[string]binding `json:"bindings"`
	} `json:"results"`
}

// query runs one SPARQL SELECT and returns its rows.
//
// The query is sent as the request body with content type
// application/sparql-query rather than as a URL parameter, because a query in
// a URL is subject to whatever length limit a proxy in front of the store
// happens to have — and the anchor query carries the search text twice.
//
// Every SPARQL statement in this package is built by a function in recall.go
// with its literals rendered by turtle.go's lit and its IRIs by iri, which is
// the same escaping the writer uses. There is no second escaping scheme and no
// place where a caller's string is pasted in raw.
func (l *Loader) query(ctx context.Context, sparql string) ([]map[string]binding, error) {
	out, err := l.do(ctx, http.MethodPost, l.repoURL(),
		"application/sparql-query", "application/sparql-results+json", sparql)
	if err != nil {
		return nil, err
	}
	var res sparqlResults
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("rdf: the store's answer is not SPARQL results JSON: %w", err)
	}
	return res.Results.Bindings, nil
}

// Ping checks that the repository is there and answering.
//
// It is called by Open before a Loader is handed back, for the reason neo4j
// verifies connectivity: a constructor that fails lazily means the first error
// a caller sees arrives in the middle of a load and is attributed to their
// data.
func (l *Loader) Ping(ctx context.Context) error {
	if _, err := l.query(ctx, "SELECT * WHERE { ?s ?p ?o } LIMIT 1"); err != nil {
		return fmt.Errorf("rdf: cannot reach repository %q at %s: %w", l.opts.Repository, l.opts.Endpoint, err)
	}
	return nil
}
