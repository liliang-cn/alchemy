package dgraph

import (
	"errors"
	"net/http"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/ontology"
)

const (
	// defaultPrefix separates alchemy's predicates from every other writer's.
	//
	// A Dgraph alpha has ONE predicate namespace for the whole cluster, so an
	// unprefixed `source` is shared with whatever else the buyer runs — and a
	// predicate's type and index are global, so two writers disagreeing about
	// whether `source` is a string or a uid is a schema conflict that surfaces
	// as somebody else's rows vanishing from a query.
	defaultPrefix = "alchemy_"
	// defaultBatchSize is how many records go in one mutation. Dgraph commits
	// a mutation as one transaction, and a transaction that touches too many
	// predicates is where its conflict detection starts costing more than the
	// write.
	defaultBatchSize = 500
)

// The predicate names, without the prefix. They are constants rather than
// literals so that the writer and every reader agree by construction: a reader
// that spelled one of these differently would return nothing and report
// nothing wrong, which is the failure shape this whole connector is arranged
// against.
const (
	keyRun        = "run"
	keyXID        = "xid"
	keyName       = "name"
	keyType       = "etype"
	keyAliases    = "aliases"
	keyKind       = "kind"
	keySource     = "source"
	keyChunk      = "chunk"
	keyProducer   = "producer"
	keyModel      = "model"
	keyOntology   = "ontology"
	keyChunking   = "chunking"
	keyConfidence = "confidence"
	keyReviewedBy = "reviewed_by"
	keyRuleSet    = "rule_set"
	keyRuledBy    = "ruled_by"
	keyBy         = "by"
	keyAt         = "at"
	keyText       = "text"
	keyStart      = "start"
	keyEnd        = "end"
	keyHeading    = "heading"
	keyIndex      = "idx"
	keyDetail     = "detail"
	keySubject    = "subject"
	keySignal     = "signal"
	keyLeft       = "left"
	keyRight      = "right"
	keyDigest     = "digest"
	keyComplete   = "complete"
	keyAttrs      = "attrs"
	keyJSONAttrs  = "json_attrs"
)

// kind is what a node is, so that a read for entities does not return chunks.
//
// Dgraph has dgraph.type, and this is deliberately not it: dgraph.type is the
// buyer's own type system and a connector that wrote into it would put
// alchemy's bookkeeping in the same list as the classes their schema declares.
const (
	kindEntity    = "entity"
	kindChunk     = "chunk"
	kindRun       = "run"
	kindDuplicate = "duplicate"
	kindViolation = "violation"
	kindGuess     = "guess"
	kindUnread    = "unread"
)

var (
	// ErrHeld is returned for a result carrying unanswered conflicts. §7.3: a
	// graph that contradicts itself is worse than no graph.
	ErrHeld = errors.New("dgraph: result is held — a conflict is unanswered")
	// ErrNoRunID is returned when the caller did not name the run.
	ErrNoRunID = errors.New("dgraph: Options.RunID is required")
	// ErrRunExists is returned when the named load is present and was written
	// from a different result.
	ErrRunExists = errors.New("dgraph: load already written from a different result")
	// ErrRefused is returned when Dgraph answered with an errors array.
	//
	// It is its own error because of how it arrives: under HTTP 200, in the
	// body, on both the query and the mutate endpoint. Everything in this
	// package that talks to the store goes through one place that looks for it,
	// and this is the value that place returns.
	ErrRefused = errors.New("dgraph: the store refused a request")
)

// Options configures one Loader.
type Options struct {
	// Endpoint is the alpha's HTTP address, e.g. http://localhost:47080. Not
	// the gRPC port: this connector speaks the HTTP API so that a buyer can
	// reproduce any request it makes with curl, which is how every claim in
	// this package's doc comment was checked.
	Endpoint string

	// RunID names the import. Required, no default — a generated one makes a
	// retry indistinguishable from a second import.
	RunID string

	// Prefix goes in front of every predicate this connector writes. Empty
	// takes defaultPrefix. See defaultPrefix for why it is not optional in
	// practice.
	Prefix string

	// BatchSize is how many records go in one mutation.
	BatchSize int

	// Overwrite drops a load that is already present before writing, instead
	// of refusing it.
	Overwrite bool

	// SkipChunks leaves Result.Chunks out. A load written with it holds no
	// citations, so recall.Cite answers ErrNoCitation for every marker in it.
	SkipChunks bool

	// SkipFindings leaves violations, duplicates, guesses and unread material
	// out. A load written with it answers nothing from recall.Unanswered,
	// which reads as "nothing is in doubt".
	SkipFindings bool

	// Ontology is the vocabulary this load was extracted under. It is recorded
	// on the run marker and not turned into a Dgraph schema: Dgraph's schema is
	// predicate types and indexes, which is a different thing from a class
	// hierarchy, and writing one as the other would claim a check nobody runs.
	Ontology *ontology.Ontology
	// OntologyPart is which part of that vocabulary this load used.
	OntologyPart ontology.Part

	// Token is the value of the X-Dgraph-AccessToken header, sent only when
	// set. An alpha with ACL disabled needs none.
	Token string

	// HTTPClient is the client every request goes through. Nil takes a default
	// with a timeout.
	HTTPClient *http.Client
}

// withDefaults fills the blanks and returns a copy.
func (o Options) withDefaults() Options {
	if o.Prefix == "" {
		o.Prefix = defaultPrefix
	}
	if o.BatchSize <= 0 {
		o.BatchSize = defaultBatchSize
	}
	o.Endpoint = strings.TrimSuffix(o.Endpoint, "/")
	return o
}
