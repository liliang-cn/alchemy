package dgraph

import (
	"context"
	"fmt"
	"strings"
)

// The Dgraph schema this connector needs, and why it is asserted at Open
// rather than left to the first write.
//
// Dgraph will accept a mutation naming a predicate it has no index for. The
// write succeeds. It is the READS that come back empty — eq(), regexp() and
// @groupby all require an index and answer nothing without one, with no error
// — so a connector that skipped this would load a corpus perfectly and then
// find nothing in it. That is the exact shape §2.1's second lesson names, and
// it is worth one extra request per Open to close.
//
// Each index is here because one primitive needs it and none is here for
// symmetry:
//
//	xid        exact + upsert   node identity; @upsert is what makes a re-load converge
//	run        exact            every read is scoped to one load
//	kind       exact            so a read for entities does not return chunks
//	etype      exact            recall.OfType, and @groupby for recall.Types
//	name       term + trigram   recall.Find is a case-insensitive SUBSTRING, which is
//	                            regexp(), and regexp() without a trigram index is refused
//	source     exact            recall.Cite resolves [source#index]
//	idx        int              chunk index; the other half of a citation marker
//
// The rest are stored and not indexed. A predicate nobody filters on costs
// index maintenance on every write for nothing, and the eleven provenance
// fields are read by following a node this connector already found.
func (l *Loader) schema() string {
	p := l.pred
	lines := []string{
		p(keyXID) + ": string @index(exact) @upsert .",
		p(keyRun) + ": string @index(exact) .",
		p(keyKind) + ": string @index(exact) .",
		p(keyType) + ": string @index(exact) .",
		p(keyName) + ": string @index(term, trigram) .",
		p(keySource) + ": string @index(exact) .",
		p(keyIndex) + ": int @index(int) .",

		p(keyAliases) + ": [string] .",
		p(keyChunk) + ": int .",
		p(keyProducer) + ": string .",
		p(keyModel) + ": string .",
		p(keyOntology) + ": string .",
		p(keyChunking) + ": string .",
		p(keyConfidence) + ": float .",
		p(keyReviewedBy) + ": string .",
		p(keyRuleSet) + ": string .",
		p(keyRuledBy) + ": string .",
		p(keyBy) + ": string .",
		p(keyAt) + ": string .",
		p(keyText) + ": string .",
		p(keyStart) + ": int .",
		p(keyEnd) + ": int .",
		p(keyHeading) + ": string .",
		p(keyDetail) + ": string .",
		p(keySubject) + ": string .",
		p(keySignal) + ": string .",
		p(keyLeft) + ": string .",
		p(keyRight) + ": string .",
		p(keyDigest) + ": string .",
		p(keyComplete) + ": bool .",
		p(keyAttrs) + ": string .",
		p(keyJSONAttrs) + ": string .",
	}
	return strings.Join(lines, "\n") + "\n"
}

// ensureSchema declares the predicates above.
//
// Idempotent by Dgraph's own rule: altering a predicate to the type and index
// it already has is a no-op. Altering it to a DIFFERENT type is not, and Dgraph
// refuses rather than reindexing silently — which is the answer this connector
// wants when a buyer's own writer already owns one of these names, and the
// reason Options.Prefix exists to keep that from happening at all.
func (l *Loader) ensureSchema(ctx context.Context) error {
	if err := l.alter(ctx, l.schema()); err != nil {
		return fmt.Errorf("dgraph: declaring the schema at %s: %w; if a predicate name is already "+
			"owned by another writer with a different type, set Options.Prefix", l.opts.Endpoint, err)
	}
	return nil
}
