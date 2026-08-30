package pipeline

import (
	"context"
	"fmt"
	"io"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/chunk"
	"github.com/liliang-cn/alchemy/pkg/source/ddl"
	"github.com/liliang-cn/alchemy/pkg/source/document"
	"github.com/liliang-cn/alchemy/pkg/source/graphimport"
	"github.com/liliang-cn/alchemy/pkg/source/tabular"
)

// read routes one source to its reader.
//
// The switch is the whole of §2.1's first lesson: three of the four kinds
// already state what they mean, so they go straight to a deterministic reader,
// and only the fourth — prose, "the hard one" in §3's diagram — is ever put in
// front of a model.
func (r *run) read(ctx context.Context, src Source) error {
	body, err := open(src)
	if err != nil {
		return err
	}
	defer body.Close()

	switch src.Kind {
	case alchemy.SourceDDL:
		return r.readDDL(src, body)
	case alchemy.SourceGraph:
		return r.readGraph(src, body)
	case alchemy.SourceTabular:
		return r.readTabular(ctx, src, body)
	case alchemy.SourceDocument:
		return r.readDocument(ctx, src, body)
	default:
		return fmt.Errorf("unknown source kind %q", src.Kind)
	}
}

// open calls the source's opener and refuses a source that has none, rather
// than treating it as empty: a corpus silently read as nothing is the failure
// §5 spends its length on.
func open(src Source) (io.ReadCloser, error) {
	if src.Open == nil {
		return nil, fmt.Errorf("has no Open function, so there is nothing to read")
	}
	body, err := src.Open()
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if body == nil {
		return nil, fmt.Errorf("Open returned no reader")
	}
	return body, nil
}

// readDDL parses a schema. §2.1: "the database schema *is* the ground-truth
// ontology", so nothing here consults a model and nothing carries a confidence.
func (r *run) readDDL(src Source, body io.Reader) error {
	text, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	res, err := ddl.Parse(src.Name, string(text))
	if err != nil {
		return err
	}
	r.entities = append(r.entities, res.Entities...)
	r.relations = append(r.relations, res.Relations...)
	// A foreign key pointing at a table the file never defines is a violation
	// the verifier cannot re-derive — it is about the file, not the ontology —
	// and a table declared twice with different columns is a conflict that
	// holds the job like any other. Both are found here and nowhere else, so
	// dropping them here would drop them entirely.
	r.violations = append(r.violations, res.Violations...)
	r.found(res.Conflicts...)
	return nil
}

// readGraph imports a graph another tool already produced. Nothing in it is
// inferred about the world — the document asserted all of it — so it too skips
// the model, and its node summaries become chunks for the embedder, which is
// the only way a structured source contributes text to vectorise.
func (r *run) readGraph(src Source, body io.Reader) error {
	res, err := graphimport.Parse(src.Name, body)
	if err != nil {
		return err
	}
	r.entities = append(r.entities, res.Entities...)
	r.relations = append(r.relations, res.Relations...)
	r.violations = append(r.violations, res.Violations...)
	r.guesses = append(r.guesses, res.Guesses...)
	r.adopt(res.Chunks)
	return nil
}

// readTabular is the one reader that can go either way, and §2.1's
// determinism-first rule decides which: a mapping the caller supplied is used
// verbatim and costs nothing, and only a table with no stated mapping is put
// in front of a model — to infer what its header means, never to read its
// rows. The rows are converted by the mapping either way, so the model reads a
// header and five sample rows rather than the table.
//
// Whatever it infers is reported. §2.1: "一个猜错的映射不会报错，它只会让一整张表
// 对不上账，然后在三个月后由一个人手工发现" — so every Guess the reader makes is
// carried into the result, where §5c ranks it above a single unsure edge
// because one wrong mapping misaligns a whole table.
//
// The vocabulary goes with it, and it is the same field the verifier will be
// handed a few stages later. §5b's third mechanism is "the same list on both
// sides of the model", and a table whose mapping was inferred has a model on
// its side too: withholding the list asked the model to invent a shape and then
// judged that shape against a list it had never seen, which made every row of
// every governed table a violation by construction. There is one vocabulary per
// job (Request.Part) rather than one per reader, so passing r.vocabulary is not
// a choice about which list to show — it is the only list this job will be
// checked against.
func (r *run) readTabular(ctx context.Context, src Source, body io.Reader) error {
	res, err := tabular.Read(ctx, src.Name, body, tabular.Options{
		Mapping:    r.req.Mapping,
		LLM:        r.req.Models.LLM,
		Vocabulary: r.vocabulary,
	})
	// The Result comes back on the error path too, carrying the calls already
	// paid for: §7.2, a failed job that reports no calls makes an expensive
	// retry look free.
	r.spend(res.ModelCalls...)
	r.entities = append(r.entities, res.Entities...)
	r.relations = append(r.relations, res.Relations...)
	r.violations = append(r.violations, res.Violations...)
	r.guesses = append(r.guesses, res.Guesses...)
	return err
}

// readDocument is §3's "the hard one". It does the two things that happen
// before a model sees anything — turning a file into text and text into chunks
// — and files the chunks for the extract stage rather than extracting here,
// so that reading a corpus and buying an extraction of it stay two stages a
// caller can watch separately.
func (r *run) readDocument(ctx context.Context, src Source, body io.Reader) error {
	doc, err := document.Read(ctx, src.Name, body, r.req.Models.OCR)
	// Unread and the OCR cost are kept even when the read failed. §5: a page
	// that could not be read is reported as unread, never returned as empty,
	// and that obligation does not lapse because the document ended badly.
	r.spend(doc.ModelCalls...)
	r.unread = append(r.unread, doc.Unread...)
	if err != nil {
		return err
	}
	chunks, err := chunk.Split(ctx, src.Name, doc.Text, r.req.Chunking)
	if err != nil {
		return err
	}
	r.docs = append(r.docs, docSource{name: src.Name, chunks: r.adopt(chunks)})
	return nil
}
