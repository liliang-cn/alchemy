package cortexdb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
	cdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// Describe returns one entity whole: its type, its name, its aliases, its
// attributes and the whole of its provenance.
//
// It exists because pkg/recall could not read an entity at all, and the cost
// of that was measured rather than argued: an agent asked in March 2027
// whether to contact somebody who had been on parental leave in October 2026
// spent nineteen tool calls hunting for dates that were sitting in the node's
// attributes, could not reach them through any primitive, and dropped the
// person from a contact list over a leave sixteen months past. With Describe
// and no other change, the same run said "his parental leave was from
// 2026-10-05 to 2026-11-05, so he should be back by 15 March 2027".
//
// SEPARATING WHAT THE SOURCE SAID FROM WHAT THIS CONNECTOR WROTE is the whole
// of the work here, and it is why the reserved prefix exists. Attributes come
// back as the record's own fields: everything under the prefix is alchemy's
// bookkeeping and everything CortexDB writes on its own nodes is CortexDB's,
// and neither belongs in a map whose documented meaning is "what the source
// said about this thing". The list of CortexDB's own names is cortexNodeProps
// — the same list preflight refuses a source attribute for colliding with, so
// the write side and the read side cannot come to disagree about which names
// are taken.
func (l *Loader) Describe(ctx context.Context, load, id string) (recall.Description, error) {
	ok, err := l.finished(ctx, load)
	if err != nil {
		return recall.Description{}, err
	}
	if !ok {
		return recall.Description{}, noLoad(load)
	}
	node, err := l.cortex.Graph().GetNode(ctx, entityNodeID(load, id))
	if err != nil || node == nil {
		// An id the load does not hold is a zero Description and a nil error;
		// the load's absence was refused above. Same asymmetry as
		// Contributions, same reason.
		return recall.Description{}, nil
	}
	pre := l.opts.ReservedPrefix
	d := recall.Description{
		ID:         prop(node.Properties, pre+keyEntityID),
		Type:       prop(node.Properties, pre+keyDeclaredType),
		Name:       prop(node.Properties, "name"),
		Provenance: provenanceFrom(node.Properties, pre),
	}
	if raw := prop(node.Properties, pre+keyAliases); raw != "" {
		if err := json.Unmarshal([]byte(raw), &d.Aliases); err != nil {
			return recall.Description{}, fmt.Errorf("cortexdb: aliases of %q in load %q: %w", id, load, err)
		}
	}
	d.Attributes = l.attributesOf(node.Properties, pre)
	return d, nil
}

// attributesOf recovers what the source said, undoing attributeMeta.
//
// Two things have to be undone and only one of them is obvious. The prefix
// separates alchemy's fields from the source's, and cortexNodeProps separates
// CortexDB's; both are removals. The third step is a restoration: CortexDB's
// metadata is map[string]string, so a number, a boolean or a nested object was
// written as its JSON text, and attributeMeta listed exactly which keys that
// happened to under the reserved json_attrs key.
//
// Reading that list rather than guessing is the point. Trying json.Unmarshal
// on every value and keeping what parses would turn the string "2024" into the
// number 2024 and the string "true" into a boolean — silently changing what a
// source said into something a reader would then compare against a number. The
// list is why the write is reversible; without it the encoding would be lossy
// in the direction nobody checks.
func (l *Loader) attributesOf(props map[string]interface{}, pre string) map[string]any {
	encoded := map[string]bool{}
	if list := prop(props, pre+keyJSONAttrs); list != "" {
		for _, k := range strings.Split(list, ",") {
			encoded[k] = true
		}
	}
	out := map[string]any{}
	for k, v := range props {
		if strings.HasPrefix(k, pre) {
			continue
		}
		if _, taken := cortexNodeProps[k]; taken {
			continue
		}
		s, isString := v.(string)
		if !isString {
			// Written by something other than this connector. It is still the
			// node's own field and it is still not ours to reinterpret.
			out[k] = v
			continue
		}
		if !encoded[k] {
			out[k] = s
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(s), &decoded); err != nil {
			// The key says it was encoded and it will not decode. Handing back
			// the text is the honest answer: the alternative is dropping a
			// field the source stated, which is the loss this connector
			// refuses everywhere else.
			out[k] = s
			continue
		}
		out[k] = decoded
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Cite resolves a [source#index] marker to the text it names.
//
// Both halves have to match. A chunk index is unique across a job, so the
// index alone would resolve — and a caller who passed the wrong file with the
// right number would be handed text from the other file with nothing about the
// answer looking wrong. The source is checked against what writeChunks stamped
// on the chunk rather than against the document id, because documentID mangles
// the name into a namespaced form and a round trip through it is one more place
// for the two to drift.
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
	resp, err := l.cortex.GraphRAGTools().GetChunks(ctx, cdb.ToolGetChunksRequest{
		ChunkIDs: []string{chunkNodeID(load, index)},
		// The text and nothing else. The entity enrichment this tool does by
		// default is a second query per chunk to answer a question Cite was
		// not asked: which entities the passage mentions is what Claims is for.
		DisableGraph: true,
	})
	if err != nil {
		return recall.Citation{}, fmt.Errorf("cortexdb: cite %s in load %q: %w", recall.Mark(source, index), load, err)
	}
	pre := l.opts.ReservedPrefix
	for _, c := range resp.Chunks {
		if c.Metadata[pre+keySource] != source {
			continue
		}
		return recall.Citation{
			Source: source, Index: index, Text: c.Content,
			Start: atoi(c.Metadata[pre+"start"]), End: atoi(c.Metadata[pre+"end"]),
		}, nil
	}
	return recall.Citation{}, fmt.Errorf("%w: %s does not resolve in load %q",
		recall.ErrNoCitation, recall.Mark(source, index), load)
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// Unanswered returns the identity questions of one load.
//
// They are read out of the completion marker rather than out of the graph, and
// that is this store's own shape rather than a shortcut. The other three
// connectors write a duplicate as a node or a triple because their graphs hold
// nothing but alchemy's records; this one is a shared brain, and a Duplicate
// written as a node would be a node — findable by a memory search, walkable
// from a knowledge query, and indistinguishable to every other reader from a
// thing the user actually knows about. completeRun already writes the findings
// as the run's own document for exactly that reason, and reading them back is
// what makes the questions visible without making them part of the graph.
//
// The consequence is worth stating: a question here cannot be answered in
// place. Deciding one is a review decision against the job, and the graph is
// re-loaded — which is what §4 means by a result being the output of a job
// rather than state on a server.
func (l *Loader) Unanswered(ctx context.Context, load, about string) ([]recall.Question, error) {
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return nil, err
	}
	doc, err := l.cortex.Vector().GetDocument(ctx, completionID(load))
	if err != nil || doc == nil {
		return nil, fmt.Errorf("cortexdb: unanswered questions of load %q: %w", load, err)
	}
	var marker runMarker
	if err := json.Unmarshal([]byte(doc.Content), &marker); err != nil {
		return nil, fmt.Errorf("cortexdb: completion marker of load %q: %w", load, err)
	}
	var findings struct {
		Duplicates []alchemy.Duplicate `json:"duplicates"`
	}
	if len(marker.Findings) > 0 {
		if err := json.Unmarshal(marker.Findings, &findings); err != nil {
			return nil, fmt.Errorf("cortexdb: findings of load %q: %w", load, err)
		}
	}
	needle := strings.ToLower(about)
	var out []recall.Question
	for _, d := range findings.Duplicates {
		// Either name, the subject or the detail — the interface's rule, and
		// an empty about matches everything because it is a substring and not
		// a sentinel. "all" was a sentinel once and cost thirty runs: an agent
		// passed it twenty-nine times, was told nothing matched, and wrote
		// "there are no unresolved identity questions" into every answer.
		if needle != "" && !containsAny(needle, d.Subject, d.Detail, d.Left.Name, d.Right.Name) {
			continue
		}
		out = append(out, recall.Question{
			Signal: d.Signal, Subject: d.Subject, Detail: d.Detail,
			Left: d.Left.Name, Right: d.Right.Name,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		return out[i].Detail < out[j].Detail
	})
	return out, nil
}

func containsAny(needle string, fields ...string) bool {
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}
