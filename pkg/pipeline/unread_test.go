package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// §5, and the failure it names: "harness-rs hit exactly this: pdf-extract
// returns nothing for a scan, and the fallback sent raw PDF bytes through
// from_utf8_lossy to the model — an OCR that looked like it worked."
//
// pkg/source/document is what refuses to do that, and this is the assertion
// that the refusal survives the trip through the whole pipeline: the page is
// named in Unread, it is counted, and nothing was sent to a model.
func TestAScannedPageArrivesAsUnreadAndNotAsAnEmptyDocument(t *testing.T) {
	req := Request{
		Sources:  []Source{{Name: "scan.pdf", Kind: alchemy.SourceDocument, Open: openBytes(scannedPagePDF())}},
		Ontology: testOntology(t),
		Part:     ontology.PartProse,
		Models:   alchemy.Models{LLM: &failLLM{t: t}, Embedder: &failEmbedder{t: t}},
	}
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Unread) != 1 {
		t.Fatalf("Unread = %+v, want the one page that could not be read", res.Unread)
	}
	u := res.Unread[0]
	if u.Source != "scan.pdf" || u.Locator == "" || u.Reason == "" {
		t.Errorf("Unread = %+v, want it to name the source, the page and why", u)
	}
	if res.Counts.ChunksUnread != 1 {
		t.Errorf("Counts.ChunksUnread = %d, want 1", res.Counts.ChunksUnread)
	}
	if len(res.Entities) != 0 || len(res.Chunks) != 0 {
		t.Errorf("a page nobody could read produced %d entities and %d chunks",
			len(res.Entities), len(res.Chunks))
	}
}

// The PDF fixtures below are written byte by byte for the reason
// pkg/source/document gives for doing the same: a checked-in binary blob is a
// fixture nobody can review, and the difference between the documents this
// package has to tell apart is a few bytes of content stream.
type pdfBuilder struct{ objs [][]byte }

func (b *pdfBuilder) add(body string) { b.objs = append(b.objs, []byte(body)) }

func (b *pdfBuilder) addStream(data []byte) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "<< /Length %d >>\nstream\n", len(data))
	buf.Write(data)
	buf.WriteString("\nendstream")
	b.objs = append(b.objs, buf.Bytes())
}

func (b *pdfBuilder) build() []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(b.objs))
	for i, body := range b.objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n", i+1)
		buf.Write(body)
		buf.WriteString("\nendobj\n")
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(b.objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(b.objs)+1, xref)
	return buf.Bytes()
}

// scannedPagePDF draws a filled rectangle: a valid page with no text operator
// on it, which is what a scan looks like to a text extractor.
func scannedPagePDF() []byte {
	var b pdfBuilder
	b.add("<< /Type /Catalog /Pages 2 0 R >>")
	b.add("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.add("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << >> >>")
	b.addStream([]byte("0.5 g\n72 72 468 648 re\nf\n"))
	return b.build()
}
