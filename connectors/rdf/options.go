package rdf

import (
	"errors"
	"net/http"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// Defaults chosen so that a caller who fills in only the endpoint, the
// repository and the run gets a graph that is safe rather than fast.
const (
	// defaultBase is where the data IRIs hang when a buyer does not say.
	//
	// example.* is reserved by RFC 2606 and can never be registered. That is
	// the whole reason for the choice: an IRI is a name and not an address, but
	// somebody will paste one into a browser, and a default that one day
	// resolves to a real company's web page would make this store's subjects
	// look like claims about them.
	defaultBase = "http://alchemy.example/"
	// defaultBatchSize is how many records go in one HTTP request. Smaller
	// than the property store's thousand because a batch here is a Turtle
	// document held in memory on both ends, and a chunk batch carries the
	// text: a thousand chunks of a page each is a request nobody meant to send.
	defaultBatchSize = 250
)

var (
	// ErrHeld is returned for a result carrying unanswered conflicts. §7.3: a
	// graph that contradicts itself is worse than no graph.
	ErrHeld = errors.New("rdf: result is held — a conflict is unanswered")
	// ErrNoRunID is returned when the caller did not name the run. There is no
	// default, for the reason neo4j's Options.RunID gives: a generated one
	// makes a retry indistinguishable from a second import.
	ErrNoRunID = errors.New("rdf: Options.RunID is required")
	// ErrRunExists is returned when the named load is already in the store and
	// was written from a different result.
	ErrRunExists = errors.New("rdf: load already written from a different result")
	// ErrEndpoint is returned when the store answered with something other
	// than success. The body is in the message, because GraphDB says useful
	// things in it and a connector that swallowed them would leave an operator
	// with a status code.
	ErrEndpoint = errors.New("rdf: the store refused a request")
)

// Options configures one Loader.
type Options struct {
	// Endpoint is the GraphDB base URL, e.g. http://localhost:47200. The
	// RDF4J-compatible paths are built from it; nothing here assumes the
	// Workbench.
	Endpoint string
	// Repository is the repository ID to write into. It is required and has no
	// default: a connector that invented one would write a customer's graph
	// into a repository nobody asked for and nobody knows to back up.
	Repository string

	// RunID names the import. Required, no default — see neo4j's Options.RunID
	// for the argument, which is this connector's too: alchemy.Entity.ID is
	// stable within one result and says nothing across runs, so a load needs a
	// name of its own and only the caller has it.
	RunID string

	// Base is the IRI prefix every data IRI hangs off. It is the buyer's own,
	// because these IRIs name their entities and a vendor's domain in the
	// middle of a customer's graph is a name they cannot use. A missing
	// trailing slash is added rather than refused.
	//
	// alchemy's own vocabulary is deliberately NOT derived from it; see alNS.
	Base string

	// BatchSize is how many records go in one HTTP request.
	BatchSize int

	// Overwrite drops a load that is already present before writing, instead of
	// refusing it. Off by default because the same gesture made accidentally is
	// how an import silently rewrites a graph somebody is already querying.
	Overwrite bool

	// SkipChunks leaves Result.Chunks out. The text is usually the largest
	// thing in a result, and it is the largest thing here by a wider margin
	// than anywhere else: every chunk is a Turtle literal in an HTTP body.
	//
	// A load written with it holds no citations, so recall.Cite answers
	// ErrNoCitation for every marker in it. That is the buyer's decision to
	// make and this connector will not make it for them, but it is worth
	// knowing before an agent is pointed at the result.
	SkipChunks bool

	// SkipFindings leaves violations, duplicates, guesses and unread material
	// out. A load written with it answers nothing from recall.Unanswered,
	// which reads as "nothing is in doubt" — the load marker's count
	// predicates are how a reader tells that apart from "the doubts were not
	// imported".
	SkipFindings bool

	// Ontology is the vocabulary this load was extracted under, written into
	// the load's graph as RDFS and OWL when it is supplied.
	//
	// It is optional and nil is the ordinary case, because sink.Sink never
	// carries one: a result names its ontology in every record's provenance
	// and does not carry the document. A caller who has it gets a store whose
	// classes have comments and whose predicates declare their cardinality,
	// which is most of what putting a graph in a triple store is for. A caller
	// who does not gets the classes the data implies and nothing invented.
	//
	// See ontology.go, and in particular why `from` and `to` are not
	// rdfs:domain and rdfs:range.
	Ontology *ontology.Ontology
	// OntologyPart is which part of that vocabulary this load used. Empty
	// means ontology.PartProse, which is what an LLM extraction is.
	OntologyPart ontology.Part

	// User and Password are HTTP basic auth, sent only when User is set. A
	// GraphDB with security disabled needs neither.
	User, Password string

	// HTTPClient is the client every request goes through. Nil takes a default
	// with a timeout — never http.DefaultClient, which has none: a load that
	// hung on one batch would hang forever, holding a half-written graph open
	// with nothing to cancel.
	HTTPClient *http.Client
}

// withDefaults fills the blanks and returns a copy, so an Options a caller
// reuses for a second Loader has not been mutated by the first.
func (o Options) withDefaults() Options {
	if o.Base == "" {
		o.Base = defaultBase
	}
	// A base that does not end in a separator would concatenate straight onto
	// the first path segment — "http://x/basetype/System" — which is a
	// different IRI from the one the buyer meant and a valid one, so nothing
	// downstream could tell.
	if !strings.HasSuffix(o.Base, "/") && !strings.HasSuffix(o.Base, "#") {
		o.Base += "/"
	}
	if o.BatchSize <= 0 {
		o.BatchSize = defaultBatchSize
	}
	if o.OntologyPart == "" {
		o.OntologyPart = ontology.PartProse
	}
	return o
}
