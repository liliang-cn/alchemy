package document

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A PDF parser needs random access, so the bytes are spooled to disk (§8.4).
// Spooling that does not clean up is a disk that fills up on the tenth
// thousand document, so every path through Read has to remove its file.
func TestSpooledTempFilesAreAlwaysRemoved(t *testing.T) {
	count := func() int {
		names, _ := filepath.Glob(filepath.Join(os.TempDir(), "alchemy-document-*"))
		return len(names)
	}
	before := count()
	cases := [][]byte{
		textPagePDF("a page"),
		scannedPagePDF(),
		encryptedPDF(),
		textPagePDF("a page")[:60],
		append([]byte("%PDF-1.4\n"), 0x00, 0x01, 0x02),
	}
	for _, doc := range cases {
		Read(context.Background(), "spool.pdf", bytes.NewReader(doc), nil)
	}
	if after := count(); after != before {
		t.Errorf("temp files left behind: %d before, %d after", before, after)
	}
}

// A cancelled context stops the work rather than finishing it and throwing the
// answer away — the caller cancelled because nobody is waiting for it.
func TestCancelledContextStopsReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("pdf", func(t *testing.T) {
		_, err := Read(ctx, "big.pdf", bytes.NewReader(pagesPDF(textLine("one"), textLine("two"))), nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("text", func(t *testing.T) {
		_, err := Read(ctx, "big.md", strings.NewReader("# heading\n\nbody\n"), nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("html", func(t *testing.T) {
		_, err := Read(ctx, "big.html", strings.NewReader("<html><body><p>body</p></body></html>"), nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

// An empty document is a document, not a failure, and it is not unread either:
// nothing could not be read, there was simply nothing.
func TestEmptyInputIsEmptyTextAndNoUnread(t *testing.T) {
	res, err := Read(context.Background(), "empty.txt", strings.NewReader(""), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Text != "" || len(res.Unread) != 0 {
		t.Errorf("got Text=%q Unread=%+v, want both empty", res.Text, res.Unread)
	}
}

// cancelAfter cancels the context once n bytes have been read, standing in for
// a caller that gives up while a very large source is still arriving.
type cancelAfter struct {
	r      io.Reader
	n      int
	cancel context.CancelFunc
	read   int
}

func (c *cancelAfter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	if c.read >= c.n {
		c.cancel()
	}
	return n, err
}

// §8.4: the source is spooled to disk rather than held in memory, and a spool
// that ignores cancellation copies the whole 10GB before noticing nobody wants
// it.
func TestCancellingDuringSpoolStopsTheCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// A PDF header so the PDF path is chosen, then a lot of filler.
	body := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("0123456789"), 200000)...)
	src := &cancelAfter{r: bytes.NewReader(body), n: 4096, cancel: cancel}

	_, err := Read(ctx, "huge.pdf", src, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if src.read >= len(body) {
		t.Errorf("the whole source was copied anyway: %d of %d bytes", src.read, len(body))
	}
}
