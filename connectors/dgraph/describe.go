package dgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Describe returns one entity whole: its type, its name, its aliases, its
// attributes and the whole of its provenance.
//
// It is the only primitive that returns a record rather than an answer, and the
// only way an entity's attributes and an assertion's author and date are
// reachable at all. The measurement behind it is in pkg/recall: an agent asked
// in March 2027 whether to contact somebody whose parental leave ended in
// November 2026 spent nineteen tool calls hunting for dates that were sitting
// in the node's attributes, and dropped the person from a contact list over a
// leave sixteen months past.
func (l *Loader) Describe(ctx context.Context, load, entityID string) (recall.Description, error) {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return recall.Description{}, err
	}
	if !ok {
		return recall.Description{}, noLoad(load)
	}
	p := l.pred
	q := "{ q(func: eq(" + p(keyXID) + ", " + literal(entityXID(load, entityID)) + ")) {\n" +
		"  xid: " + p(keyXID) + "\n  name: " + p(keyName) + "\n  etype: " + p(keyType) + "\n" +
		"  aliases: " + p(keyAliases) + "\n  attrs: " + p(keyAttrs) + "\n" +
		"  source: " + p(keySource) + "\n  chunk: " + p(keyChunk) + "\n  producer: " + p(keyProducer) + "\n" +
		"  model: " + p(keyModel) + "\n  ontology: " + p(keyOntology) + "\n  chunking: " + p(keyChunking) + "\n" +
		"  confidence: " + p(keyConfidence) + "\n  reviewed_by: " + p(keyReviewedBy) + "\n" +
		"  rule_set: " + p(keyRuleSet) + "\n  ruled_by: " + p(keyRuledBy) + "\n" +
		"  by: " + p(keyBy) + "\n  at: " + p(keyAt) + "\n} }\n"

	var out struct {
		Q []struct {
			XID     string   `json:"xid"`
			Name    string   `json:"name"`
			EType   string   `json:"etype"`
			Aliases []string `json:"aliases"`
			Attrs   string   `json:"attrs"`

			Source     string  `json:"source"`
			Chunk      *int    `json:"chunk"`
			Producer   string  `json:"producer"`
			Model      string  `json:"model"`
			Ontology   string  `json:"ontology"`
			Chunking   string  `json:"chunking"`
			Confidence float64 `json:"confidence"`
			ReviewedBy string  `json:"reviewed_by"`
			RuleSet    string  `json:"rule_set"`
			RuledBy    string  `json:"ruled_by"`
			By         string  `json:"by"`
			At         string  `json:"at"`
		} `json:"q"`
	}
	if err := l.queryInto(ctx, q, &out); err != nil {
		return recall.Description{}, fmt.Errorf("dgraph: describe %q in load %q: %w", entityID, load, err)
	}
	if len(out.Q) == 0 {
		// An id the load does not hold is a zero Description and a nil error;
		// the load's absence was refused above. Same asymmetry as
		// Contributions, same reason: a load that is not there is the caller's
		// mistake, an id that is not there is an ordinary answer.
		return recall.Description{}, nil
	}
	r := out.Q[0]
	// A pointer, so that "no chunk predicate" and "chunk 0" are different
	// answers. Decoded into an int they would both be zero, and a record whose
	// provenance never had a chunk would cite the first chunk of its file.
	chunk := -1
	if r.Chunk != nil {
		chunk = *r.Chunk
	}
	d := recall.Description{
		ID: id(r.XID, load), Type: r.EType, Name: r.Name, Aliases: r.Aliases,
		Provenance: alchemy.Provenance{
			Source: r.Source, Chunk: chunk, Producer: alchemy.Producer(r.Producer),
			Model: r.Model, Ontology: r.Ontology, Chunking: r.Chunking,
			Confidence: r.Confidence, ReviewedBy: r.ReviewedBy, RuleSet: r.RuleSet,
			RuledBy: r.RuledBy, By: r.By, At: r.At,
		},
	}
	if r.Attrs != "" {
		if err := json.Unmarshal([]byte(r.Attrs), &d.Attributes); err != nil {
			return recall.Description{}, fmt.Errorf("dgraph: attributes of %q in load %q: %w", entityID, load, err)
		}
	}
	return d, nil
}

// Cite resolves a [source#index] marker to the text it names.
//
// Both halves have to match. A chunk index is unique across a job, so the index
// alone would resolve — and a caller who passed the wrong file with the right
// number would be handed text from the other file with nothing about the answer
// looking wrong.
func (l *Loader) Cite(ctx context.Context, load, source string, index int) (recall.Citation, error) {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return recall.Citation{}, err
	}
	if !ok {
		return recall.Citation{}, noLoad(load)
	}
	if index < 0 {
		// Not a failure. §5b ranks a machine reading something that already
		// asserted a fact above a model reading prose, so a chunk-less record
		// is this store's strongest kind — a DDL import, a graph import, a
		// person's assertion — and refusing it with the sentence reserved for
		// a broken citation tells a reader to distrust the best thing here.
		return recall.Citation{}, fmt.Errorf("%w: %s came from a producer that does not work in chunks",
			recall.ErrNoChunk, recall.Mark(source, index))
	}
	p := l.pred
	q := "{ q(func: " + l.scope(load, kindChunk) +
		" AND eq(" + p(keySource) + ", " + literal(source) + ")" +
		" AND eq(" + p(keyIndex) + ", " + itoa(index) + ")) {\n" +
		"  text: " + p(keyText) + "\n  start: " + p(keyStart) + "\n  end: " + p(keyEnd) + "\n} }\n"
	var out struct {
		Q []struct {
			Text  string `json:"text"`
			Start int    `json:"start"`
			End   int    `json:"end"`
		} `json:"q"`
	}
	if err := l.queryInto(ctx, q, &out); err != nil {
		return recall.Citation{}, fmt.Errorf("dgraph: cite %s in load %q: %w", recall.Mark(source, index), load, err)
	}
	if len(out.Q) == 0 {
		return recall.Citation{}, fmt.Errorf("%w: %s does not resolve in load %q",
			recall.ErrNoCitation, recall.Mark(source, index), load)
	}
	c := out.Q[0]
	return recall.Citation{Source: source, Index: index, Text: c.Text, Start: c.Start, End: c.End}, nil
}

// Unanswered returns the identity questions of one load.
//
// An empty about returns all of them, and it is empty rather than a word like
// "all" because a sentinel that is also a legal search term is a filter that
// silently stops filtering for one input. Measured, on the tool layer: across
// thirty runs an agent passed "all" twenty-nine times, was told nothing
// matched, and wrote "there are no unresolved identity questions affecting this
// answer" into every final answer. The store held thirteen.
//
// Filtered here rather than in the query. Dgraph's regexp needs a trigram index
// and these four predicates have none — they are written once per finding and
// read as a list, so indexing four more strings on every load to save a filter
// over a set that is small by construction would be paying the write path for
// the read path's convenience.
func (l *Loader) Unanswered(ctx context.Context, load, about string) ([]recall.Question, error) {
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return nil, err
	}
	p := l.pred
	q := "{ q(func: " + l.scope(load, kindDuplicate) + ") {\n" +
		"  signal: " + p(keySignal) + "\n  subject: " + p(keySubject) + "\n" +
		"  detail: " + p(keyDetail) + "\n  left: " + p(keyLeft) + "\n  right: " + p(keyRight) + "\n} }\n"
	var out struct {
		Q []struct {
			Signal  string `json:"signal"`
			Subject string `json:"subject"`
			Detail  string `json:"detail"`
			Left    string `json:"left"`
			Right   string `json:"right"`
		} `json:"q"`
	}
	if err := l.queryInto(ctx, q, &out); err != nil {
		return nil, fmt.Errorf("dgraph: unanswered questions of load %q: %w", load, err)
	}
	needle := strings.ToLower(about)
	var res []recall.Question
	for _, r := range out.Q {
		if needle != "" && !containsAny(needle, r.Subject, r.Detail, r.Left, r.Right) {
			continue
		}
		res = append(res, recall.Question{
			Signal: alchemy.DuplicateSignal(r.Signal), Subject: r.Subject,
			Detail: r.Detail, Left: r.Left, Right: r.Right,
		})
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Subject != res[j].Subject {
			return res[i].Subject < res[j].Subject
		}
		return res[i].Detail < res[j].Detail
	})
	return res, nil
}

func containsAny(needle string, fields ...string) bool {
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}
