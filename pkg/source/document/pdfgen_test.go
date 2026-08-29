package document

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/jpeg"
	"strings"
)

// A PDF fixture builder. The package must never depend on files outside the
// repository (and a checked-in binary blob is a fixture nobody can review), so
// the tests write their own PDFs byte by byte. Everything the reader under test
// is asked to distinguish — a page with text, a page with none, a truncated
// file, an encrypted one — is a small difference in what this builder emits.
type pdfBuilder struct {
	objs [][]byte
}

// add appends one indirect object body and returns its object number.
func (b *pdfBuilder) add(body string) int {
	b.objs = append(b.objs, []byte(body))
	return len(b.objs)
}

// addStream appends a stream object. /Length is filled in from the data, which
// is what makes a hand-written fixture readable by a real parser.
func (b *pdfBuilder) addStream(dict string, data []byte) int {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "<< /Length %d%s >>\nstream\n", len(data), dict)
	buf.Write(data)
	buf.WriteString("\nendstream")
	b.objs = append(b.objs, buf.Bytes())
	return len(b.objs)
}

// build serialises the objects with a cross-reference table. trailerExtra lets
// one test bolt an /Encrypt dictionary on without a second builder.
func (b *pdfBuilder) build(trailerExtra string) []byte {
	var buf bytes.Buffer
	// The reader validates bytes 0..8 exactly: "%PDF-1.x" then a line break.
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
		// Each entry is exactly 20 bytes; a parser seeks by multiplication.
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n",
		len(b.objs)+1, trailerExtra, xref)
	return buf.Bytes()
}

// onePagePDF builds a single-page document whose content stream is exactly
// what the caller passes. Pass text-drawing operators for a page with a text
// layer, and anything else for a page without one.
func onePagePDF(content string) []byte {
	var b pdfBuilder
	b.add("<< /Type /Catalog /Pages 2 0 R >>")
	b.add("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.add("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << >> >>")
	b.addStream("", []byte(content))
	return b.build("")
}

// scannedPagePDF is the fixture the whole package exists for: a page that
// draws a picture and says nothing. A text extractor finds no text on it, and
// that is not the same fact as "this page is empty".
func scannedPagePDF() []byte {
	// A filled rectangle: valid page content with no text operator in it.
	return onePagePDF("0.5 g\n72 72 468 648 re\nf\n")
}

// textPagePDF is a page with a real text layer.
func textPagePDF(lines ...string) []byte {
	var content bytes.Buffer
	content.WriteString("BT\n/F1 12 Tf\n")
	for _, line := range lines {
		fmt.Fprintf(&content, "(%s) Tj\nT*\n", line)
	}
	content.WriteString("ET\n")
	return onePagePDF(content.String())
}

// scannedImagePDF is a page that draws one image and contains no text
// operators — what a scanner produces. filter is the PDF filter name and data
// is the stream exactly as it appears in the file.
func scannedImagePDF(filter string, w, h int, data []byte) []byte {
	var b pdfBuilder
	b.add("<< /Type /Catalog /Pages 2 0 R >>")
	b.add("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.add("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
		"/Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>")
	b.addStream("", []byte("q\n612 0 0 792 0 0 cm\n/Im0 Do\nQ\n"))
	b.addStream(fmt.Sprintf(
		" /Type /XObject /Subtype /Image /Width %d /Height %d"+
			" /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /%s", w, h, filter), data)
	return b.build("")
}

// flateScanPDF is a scanned page whose image is stored with the filter the PDF
// parser can decode, so the reader has to re-encode the samples for OCR.
func flateScanPDF(w, h int) []byte {
	samples := make([]byte, w*h)
	for i := range samples {
		samples[i] = byte(i % 251)
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(samples)
	zw.Close()
	return scannedImagePDF("FlateDecode", w, h, z.Bytes())
}

// jpegScanPDF is the common case: a scan stored as JPEG. The bytes of the
// stream are the JPEG file, which is what an OCR endpoint wants.
func jpegScanPDF(w, h int) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i % 251)
	}
	var j bytes.Buffer
	if err := jpeg.Encode(&j, img, nil); err != nil {
		panic(err)
	}
	return scannedImagePDF("DCTDecode", w, h, j.Bytes())
}

// pagesPDF builds a document with one page per content stream, so a test can
// put a broken page next to good ones.
func pagesPDF(contents ...string) []byte {
	var b pdfBuilder
	b.add("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, len(contents))
	for i := range contents {
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
	}
	b.add(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(contents)))
	for i, c := range contents {
		b.add(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]"+
			" /Contents %d 0 R /Resources << >> >>", 4+2*i))
		b.addStream("", []byte(c))
	}
	return b.build("")
}

// encryptedPDF has a standard security handler with parameters no empty
// password will satisfy, which is what a password-protected file looks like.
func encryptedPDF() []byte {
	var b pdfBuilder
	b.add("<< /Type /Catalog /Pages 2 0 R >>")
	b.add("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	b.add("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << >> >>")
	b.addStream("", []byte("BT ET"))
	b.add("<< /Filter /Standard /V 2 /R 3 /Length 128 /P -4 /O <" +
		strings.Repeat("ab", 32) + "> /U <" + strings.Repeat("cd", 32) + "> >>")
	id := "<" + strings.Repeat("0f", 16) + ">"
	return b.build(" /Encrypt 5 0 R /ID [" + id + " " + id + "]")
}

// textLine is the content stream of a page that says one thing.
func textLine(s string) string { return "BT\n/F1 12 Tf\n(" + s + ") Tj\nET\n" }

// brokenPageContent is a content stream the interpreter cannot run: Tj with no
// operand. A real file gets this way by being truncated mid-stream.
const brokenPageContent = "BT\n/F1 12 Tf\nTj\nET\n"
