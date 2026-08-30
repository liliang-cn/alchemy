// The four writes, in the order Load runs them. They are in their own file
// because they are the only part of this package that talks to CortexDB: what
// is handed over, and in whose vocabulary, is the whole design, and it should
// be readable in one sitting without the bookkeeping around it.
package cortexdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/cortexdb/v2/pkg/core"
	cdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// writeDocuments creates one CortexDB document record per source file.
//
// It has to exist before any chunk does — an embedding's doc id is a foreign
// key — and it is also the right shape: alchemy's Provenance.Source is a file,
// CortexDB's document is a file, and once the record exists CortexDB's own
// purge-by-document and source_document_ids bookkeeping work on an alchemy
// import exactly as they do on one of its own.
//
// The record's Content is left empty on purpose. CortexDB stores a document's
// whole text there because its own ingest splits that text; alchemy split it
// already, and the chunks that survived review are the record. Writing a
// reconstructed "full text" would be inventing a document that never existed —
// the chunks may have gaps, because a page nobody could read is in Unread.
func (l *Loader) writeDocuments(ctx context.Context, p *plan, rep *Report) error {
	store := l.cortex.Vector()
	for _, src := range p.sources {
		id := documentID(l.opts.RunID, src)
		if doc, err := store.GetDocument(ctx, id); err == nil && doc != nil {
			continue
		}
		rep.Batches++
		if err := store.CreateDocument(ctx, &core.Document{
			ID: id, Title: src, SourceURL: src, Author: author, Version: 1,
		}); err != nil {
			return fmt.Errorf("cortexdb: create document %s: %w", id, err)
		}
	}
	return nil
}

// writeChunks puts alchemy's chunks, with alchemy's vectors, into a collection
// of alchemy's dimension, and returns the chunk indexes that made it.
//
// The rows are core.Embeddings and there are no chunk graph nodes: CortexDB
// creates those itself, as stubs, for exactly this caller — "an external
// pipeline with its own chunker" whose chunk text "lives in the caller's
// embedding store, under the same id". Writing them here would be a second
// answer to a question CortexDB has already answered.
//
// It takes the range of p.chunks it has not yet written rather than the whole
// slice, because pkg/sink hands this store chunks a batch at a time and a
// writer that walked everything filed so far would rewrite the corpus once per
// batch.
func (l *Loader) writeChunks(ctx context.Context, p *plan, from int, rep *Report) (map[int]bool, error) {
	written := map[int]bool{}
	if from >= len(p.chunks) {
		return written, nil
	}
	store := l.cortex.Vector()
	if p.dim > 0 {
		if _, err := store.GetCollection(ctx, l.opts.Collection); err != nil {
			if _, err := store.CreateCollection(ctx, l.opts.Collection, p.dim); err != nil {
				return nil, fmt.Errorf("cortexdb: create collection %s: %w", l.opts.Collection, err)
			}
		}
	}

	batch := make([]*core.Embedding, 0, l.opts.BatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		rep.Batches++
		if err := store.UpsertBatch(ctx, batch); err != nil {
			return fmt.Errorf("cortexdb: write chunks: %w", err)
		}
		rep.Chunks += len(batch)
		batch = batch[:0]
		return nil
	}
	for _, i := range p.chunks[from:] {
		c := p.res.Chunks[i]
		vi, ok := p.vectorFor[c.Index]
		if !ok {
			rep.ChunksWithoutVectors++
			continue
		}
		meta := map[string]string{
			// CortexDB's own chunk metadata keys, so its readers behave as they
			// do for chunks it ingested itself.
			"graph_kind": "chunk", "document_id": documentID(l.opts.RunID, c.Source),
			"chunk_index": strconv.Itoa(c.Index),
		}
		pre := l.opts.ReservedPrefix
		meta[pre+keyRun] = l.opts.RunID
		meta[pre+keySource] = c.Source
		meta[pre+keyChunking] = c.Strategy
		meta[pre+keyModel] = p.res.Vectors[vi].Model
		meta[pre+"start"] = strconv.Itoa(c.Start)
		meta[pre+"end"] = strconv.Itoa(c.End)
		if c.Heading != "" {
			meta[pre+"heading"] = c.Heading
		}
		batch = append(batch, &core.Embedding{
			ID: chunkNodeID(l.opts.RunID, c.Index), Collection: l.opts.Collection,
			Vector: p.res.Vectors[vi].Values, Content: c.Text,
			DocID: documentID(l.opts.RunID, c.Source), Metadata: meta,
		})
		written[c.Index] = true
		if len(batch) >= l.opts.BatchSize {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	return written, flush()
}

// writeEntities hands the nodes to CortexDB's own upsert, one request per
// document, because DocumentID is per-request and it is what CortexDB unions
// into an entity's source_document_ids.
func (l *Loader) writeEntities(ctx context.Context, p *plan, chunks map[int]bool, rep *Report) error {
	tools := l.cortex.GraphRAGTools()
	byDoc := map[string][]cdb.ToolEntityInput{}
	order := []string{}
	planned := map[string]bool{}
	for _, i := range p.entities {
		e := p.res.Entities[i]
		meta := provenanceMeta(e.Provenance, l.opts.ReservedPrefix)
		meta[l.opts.ReservedPrefix+keyRun] = l.opts.RunID
		meta[l.opts.ReservedPrefix+keyEntityID] = e.ID
		// The type alchemy's ontology declared, kept verbatim beside the
		// node_type CortexDB may canonicalise to its own spelling. Two names
		// for one thing is a question a reader must be able to ask.
		meta[l.opts.ReservedPrefix+keyDeclaredType] = e.Type
		// CortexDB keeps its own aliases under properties and resolves names
		// through them (v2.77.0). These are alchemy's, under the reserved
		// prefix, and they are kept separate on purpose: one is what this
		// import said, the other is what that brain has concluded, and a
		// reader chasing "who says so" must be able to tell them apart.
		// A JSON array in a string, the way attributeMeta encodes any value
		// this store's metadata cannot hold natively. A comma-joined list
		// would be shorter and would lose a name with a comma in it, which is
		// the sort of loss that shows up years later in one row.
		if len(e.Aliases) > 0 {
			b, err := json.Marshal(e.Aliases)
			if err != nil {
				return fmt.Errorf("entity %s: aliases: %w", e.ID, err)
			}
			meta[l.opts.ReservedPrefix+keyAliases] = string(b)
		}
		if err := attributeMeta(e.Attributes, l.opts.ReservedPrefix, meta); err != nil {
			return fmt.Errorf("entity %s: %w", e.ID, err)
		}
		in := cdb.ToolEntityInput{
			ID: entityNodeID(l.opts.RunID, e.ID), Name: e.Name, Type: e.Type, Metadata: meta,
		}
		if e.Provenance.Chunk >= 0 && chunks[e.Provenance.Chunk] {
			in.ChunkIDs = []string{chunkNodeID(l.opts.RunID, e.Provenance.Chunk)}
		}
		planned[in.ID] = true
		doc := documentID(l.opts.RunID, e.Provenance.Source)
		if _, seen := byDoc[doc]; !seen {
			order = append(order, doc)
		}
		byDoc[doc] = append(byDoc[doc], in)
	}

	for _, doc := range order {
		for _, chunk := range batches(len(byDoc[doc]), l.opts.BatchSize) {
			rep.Batches++
			resp, err := tools.UpsertEntities(ctx, cdb.ToolUpsertEntitiesRequest{
				DocumentID: doc, Entities: byDoc[doc][chunk[0]:chunk[1]],
			})
			if err != nil {
				return fmt.Errorf("cortexdb: write entities of %s: %w", doc, err)
			}
			// CortexDB decides where a node goes, and with an active schema it
			// can decide differently: an entity of a declared type whose
			// primary key is name-shaped is re-keyed to entity:<type>:<name>,
			// which drops this run's namespace and fuses two imports. It
			// happens under `vocabulary` enforcement as well as `strict`, so
			// the guard is on the ids CortexDB actually used rather than on the
			// mode — asked rather than assumed, because the rule is CortexDB's
			// to give and a copy of it would go stale in silence.
			for _, id := range resp.EntityNodeIDs {
				if !planned[id] {
					return fmt.Errorf("%w: asked for a node under run %q and it was written as %q; "+
						"load into a store whose active ontology does not key these types by name",
						ErrRekeyed, l.opts.RunID, id)
				}
			}
			rep.Entities += len(resp.EntityNodeIDs)
			rep.MentionEdges += resp.MentionEdgeCount
		}
	}
	return nil
}

// writeRelations hands the edges over one group at a time — a group being what
// CortexDB's identity rule makes one edge (plan.go). Grouping before the write
// is what stops CortexDB's own merge from firing: it overlays scalar
// properties, so the second member of a group would silently overwrite the
// first's provenance.
func (l *Loader) writeRelations(ctx context.Context, p *plan, chunks map[int]bool, rep *Report) error {
	tools := l.cortex.GraphRAGTools()
	byDoc := map[string][]cdb.ToolRelationInput{}
	order := []string{}
	for _, g := range p.groups {
		in := cdb.ToolRelationInput{From: g.from, To: g.to, Type: g.typ, Metadata: map[string]string{}}
		pre := l.opts.ReservedPrefix
		provs := make([]alchemy.Provenance, 0, len(g.members))
		seenChunk := map[string]bool{}
		for _, m := range g.members {
			r := p.res.Relations[m]
			provs = append(provs, r.Provenance)
			// CortexDB's own three fields, filled from alchemy's. `inferred` is
			// true if any member was inferred: a merged edge that one
			// deterministic source also stated is still an edge a model
			// proposed, and marking it deterministic would launder it.
			if !r.Provenance.Producer.Deterministic() {
				in.Inferred = true
			}
			if r.Provenance.Chunk >= 0 && chunks[r.Provenance.Chunk] {
				id := chunkNodeID(l.opts.RunID, r.Provenance.Chunk)
				if !seenChunk[id] {
					seenChunk[id] = true
					in.ChunkIDs = append(in.ChunkIDs, id)
				}
			}
		}
		blob, err := json.Marshal(provs)
		if err != nil {
			return fmt.Errorf("cortexdb: render relation provenance: %w", err)
		}
		in.Provenance = renderProvenance(provs[0])
		in.Metadata[pre+keyRun] = l.opts.RunID
		in.Metadata[pre+keyAssertions] = strconv.Itoa(len(g.members))
		// The whole of every member's provenance, always, so that a fused group
		// loses nothing even though only one of them can fill the flat fields.
		in.Metadata[pre+keyProvenance] = string(blob)
		if len(g.keys) > 0 {
			in.Metadata[pre+keyEdgeKey] = joinKeys(g.keys)
		}
		if len(g.members) == 1 {
			r := p.res.Relations[g.members[0]]
			for k, v := range provenanceMeta(r.Provenance, pre) {
				in.Metadata[k] = v
			}
			if err := attributeMeta(r.Attributes, pre, in.Metadata); err != nil {
				return fmt.Errorf("relation %s-[%s]->%s: %w", r.From, r.Type, r.To, err)
			}
		} else {
			attrs := make([]map[string]any, 0, len(g.members))
			for _, m := range g.members {
				attrs = append(attrs, p.res.Relations[m].Attributes)
			}
			blob, err := json.Marshal(attrs)
			if err != nil {
				return fmt.Errorf("cortexdb: render relation attributes: %w", err)
			}
			in.Metadata[pre+"attributes"] = string(blob)
		}
		if _, seen := byDoc[g.doc]; !seen {
			order = append(order, g.doc)
		}
		byDoc[g.doc] = append(byDoc[g.doc], in)
	}

	for _, doc := range order {
		for _, chunk := range batches(len(byDoc[doc]), l.opts.BatchSize) {
			rep.Batches++
			resp, err := tools.UpsertRelations(ctx, cdb.ToolUpsertRelationsRequest{
				DocumentID: doc, Relations: byDoc[doc][chunk[0]:chunk[1]],
			})
			if err != nil {
				return fmt.Errorf("cortexdb: write relations of %s: %w", doc, err)
			}
			if len(resp.Rejected) > 0 {
				return fmt.Errorf("cortexdb: the store refused %d edge(s) of %s: %v",
					len(resp.Rejected), doc, resp.Rejected)
			}
			rep.Relations += resp.Written
		}
	}
	return nil
}

// batches yields [start, end) pairs. It exists so that §8.4's "a large result
// does not fit in one write" is one loop written once.
func batches(n, size int) [][2]int {
	out := make([][2]int, 0, n/size+1)
	for start := 0; start < n; start += size {
		end := min(start+size, n)
		out = append(out, [2]int{start, end})
	}
	return out
}

func joinKeys(keys []string) string {
	out := keys[0]
	for _, k := range keys[1:] {
		out += "," + k
	}
	return out
}
