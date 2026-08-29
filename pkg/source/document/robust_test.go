package document

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// TestBinaryGarbageIsNeverCoercedIntoText is the second half of the harness-rs
// bug: when extraction produced nothing, the fallback pushed the raw bytes
// through a lossy UTF-8 conversion and handed the mojibake to a model. Bytes
// that are not text are refused, loudly.
func TestBinaryGarbageIsNeverCoercedIntoText(t *testing.T) {
	garbage := make([]byte, 4096)
	for i := range garbage {
		garbage[i] = byte(i*7 + i/256*13)
	}
	for _, name := range []string{"payload.bin", "notes.txt", "page.html", "readme.md", "scan.pdf"} {
		t.Run(name, func(t *testing.T) {
			res, err := Read(context.Background(), name, bytes.NewReader(garbage), nil)
			if err == nil {
				t.Fatalf("binary bytes were accepted as a document: Text=%q", res.Text)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name the source", err)
			}
			if res.Text != "" {
				t.Fatalf("Text = %q, want nothing at all", res.Text)
			}
			if strings.ContainsRune(res.Text, '�') {
				t.Fatal("the replacement character reached Text: bytes were coerced")
			}
		})
	}
}

// A .txt that holds a PDF is a real thing, so content decides and the
// extension only breaks ties the bytes cannot.
func TestFormatComesFromContentNotTheExtension(t *testing.T) {
	t.Run("pdf bytes named .txt", func(t *testing.T) {
		res, err := Read(context.Background(), "invoice.txt",
			bytes.NewReader(textPagePDF("read as a PDF")), nil)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !strings.Contains(res.Text, "read as a PDF") {
			t.Errorf("Text = %q, want the PDF's text", res.Text)
		}
		if strings.Contains(res.Text, "%PDF-") {
			t.Error("the PDF's bytes were returned as text")
		}
	})
	t.Run("html bytes named .md", func(t *testing.T) {
		res, err := Read(context.Background(), "page.md",
			strings.NewReader("<!DOCTYPE html>\n<html><body><h1>Title</h1><p>Body.</body></html>"), nil)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if strings.Contains(res.Text, "<h1>") || strings.Contains(res.Text, "DOCTYPE") {
			t.Errorf("HTML was passed through as markdown: %q", res.Text)
		}
		if !strings.Contains(res.Text, "# Title") {
			t.Errorf("Text = %q, want a markdown heading", res.Text)
		}
	})
	t.Run("markdown named .html without a signature", func(t *testing.T) {
		// No <html> and no doctype: the extension is all there is, and the
		// HTML reader must not eat a markdown heading that is not a tag.
		res, err := Read(context.Background(), "notes.html",
			strings.NewReader("<p>a fragment with <b>markup</b>\n"), nil)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if strings.Contains(res.Text, "<b>") {
			t.Errorf("the .html extension was ignored: %q", res.Text)
		}
	})
	t.Run("unknown extension is text", func(t *testing.T) {
		res, err := Read(context.Background(), "CHANGELOG", strings.NewReader("# 1.0\n\nreleased\n"), nil)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if res.Text != "# 1.0\n\nreleased\n" {
			t.Errorf("Text = %q, want it unchanged", res.Text)
		}
	})
}

func TestTruncatedPDFIsAnErrorNotAPanic(t *testing.T) {
	full := textPagePDF("a line of text", "and another")
	for _, keep := range []int{0, 1, 8, 9, 30, 90, len(full) / 4, len(full) / 2, len(full) - 40, len(full) - 1} {
		if keep < 0 || keep > len(full) {
			continue
		}
		res, err := Read(context.Background(), "cut.pdf", bytes.NewReader(full[:keep]), nil)
		if err != nil {
			if !strings.Contains(err.Error(), "cut.pdf") {
				t.Errorf("keep=%d: error %q does not name the source", keep, err)
			}
			continue
		}
		// Succeeding is allowed only if nothing was invented: either real text
		// or an honest Unread.
		if strings.Contains(res.Text, "%PDF") || strings.ContainsRune(res.Text, '�') {
			t.Errorf("keep=%d: raw bytes leaked into Text: %q", keep, res.Text)
		}
	}
}

// A cross-reference table full of zeros is a file that opens and then goes
// wrong, which is where a parser that panics takes the service with it.
func TestCorruptCrossReferenceIsAnErrorNotAPanic(t *testing.T) {
	full := textPagePDF("a line of text")
	broken := bytes.Replace(full, []byte("xref\n"), []byte("xref\n"), 1)
	i := bytes.Index(broken, []byte("xref\n"))
	tail := broken[i:]
	for j := 0; j < len(tail); j++ {
		if tail[j] >= '1' && tail[j] <= '9' {
			tail[j] = '9'
		}
	}
	res, err := Read(context.Background(), "corrupt.pdf", bytes.NewReader(broken), nil)
	if err != nil {
		if !strings.Contains(err.Error(), "corrupt.pdf") {
			t.Errorf("error %q does not name the source", err)
		}
		return
	}
	if strings.Contains(res.Text, "%PDF") {
		t.Errorf("raw bytes leaked into Text: %q", res.Text)
	}
}

func TestEncryptedPDFIsAClearErrorNamingTheSource(t *testing.T) {
	_, err := Read(context.Background(), "secret.pdf", bytes.NewReader(encryptedPDF()), nil)
	if err == nil {
		t.Fatal("an encrypted PDF was read without a password")
	}
	if !strings.Contains(err.Error(), "secret.pdf") {
		t.Errorf("error %q does not name the source", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "encrypt") &&
		!strings.Contains(strings.ToLower(err.Error()), "password") {
		t.Errorf("error %q does not say the file is encrypted", err)
	}
}

// §5 again, at page granularity: one bad page does not cost the good ones, and
// it does not pass silently either.
func TestOneUnreadablePageLeavesTheOtherPagesReadable(t *testing.T) {
	doc := pagesPDF(textLine("page one text"), brokenPageContent, textLine("page three text"))
	res, err := Read(context.Background(), "mixed.pdf", bytes.NewReader(doc), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(res.Text, "page one text") || !strings.Contains(res.Text, "page three text") {
		t.Errorf("Text = %q, want both good pages", res.Text)
	}
	if len(res.Unread) != 1 {
		t.Fatalf("want 1 unread page, got %+v", res.Unread)
	}
	if res.Unread[0].Locator != "page 2" {
		t.Errorf("Locator = %q, want page 2", res.Unread[0].Locator)
	}
	if res.Unread[0].Reason == "" {
		t.Error("the unread page does not say why")
	}
}

// A source reader that can be crashed by a customer's file is a service that
// can be crashed by a customer's file. Mutating a valid PDF byte by byte is the
// cheapest way to find the paths where the parser panics instead of failing.
func TestMutatedPDFsNeverPanic(t *testing.T) {
	full := jpegScanPDF(16, 16)
	for i := 0; i < len(full); i += 1 {
		for _, b := range []byte{0x00, 0xff, '9', '<', '>', '/', '(', ')', '%', 0x0a} {
			mutant := append([]byte(nil), full...)
			mutant[i] = b
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic on byte %d set to %#x: %v", i, b, r)
					}
				}()
				res, err := Read(context.Background(), "fuzz.pdf", bytes.NewReader(mutant), &fakeOCR{text: "ocr"})
				if err == nil && strings.Contains(res.Text, "%PDF") {
					t.Fatalf("byte %d: raw bytes leaked into Text", i)
				}
			}()
		}
	}
}

// A malformed content stream must not hang the reader.
//
// The chosen PDF library spins forever on an unterminated hex string: its
// lexer returns a synthetic newline at end of input and the hex-string loop
// treats newlines as skippable whitespace, so it never reaches the ">" it is
// waiting for. A source reader that a customer's file can hang is worse than
// one that fails, because nothing about it looks broken from outside.
func TestMalformedContentStreamDoesNotHang(t *testing.T) {
	for _, content := range []string{
		"BT\n/F1 12 Tf\n<4142",           // hex string with no ">"
		"BT\n<",                          // "<" as the last byte
		"BT\n/F1 12 Tf\n<41 42 43 44 45", // spaces inside, still unterminated
	} {
		done := make(chan Result, 1)
		go func() {
			res, _ := Read(context.Background(), "hang.pdf", bytes.NewReader(onePagePDF(content)), nil)
			done <- res
		}()
		select {
		case res := <-done:
			if len(res.Unread) != 1 {
				t.Errorf("content %q: want the page reported unread, got %+v", content, res.Unread)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("content %q: Read did not return; the parser is spinning", content)
		}
	}
}
