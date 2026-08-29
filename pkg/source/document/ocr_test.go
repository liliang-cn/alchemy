package document

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// fakeOCR is the caller-supplied model, faked. It records what it was handed so
// the tests can assert that the reader passed a real page image and not, say,
// a slice of the PDF file.
type fakeOCR struct {
	mu    sync.Mutex
	text  string
	err   error
	calls int
	pages [][]byte
	media []string
}

func (f *fakeOCR) Name() string { return "fake-ocr" }

func (f *fakeOCR) Recognize(ctx context.Context, page []byte, mediaType string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.pages = append(f.pages, page)
	f.media = append(f.media, mediaType)
	return f.text, f.err
}

func TestScannedPageWithOCRUsesTheRecognisedText(t *testing.T) {
	for _, tc := range []struct {
		name string
		pdf  []byte
	}{
		{"jpeg image", jpegScanPDF(64, 48)},
		{"flate image", flateScanPDF(64, 48)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ocr := &fakeOCR{text: "the scanned words"}
			res, err := Read(context.Background(), "scan.pdf", bytes.NewReader(tc.pdf), ocr)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if !strings.Contains(res.Text, "the scanned words") {
				t.Errorf("Text = %q, want the OCR result in it", res.Text)
			}
			if len(res.Unread) != 0 {
				t.Errorf("a page OCR read must not be unread: %+v", res.Unread)
			}
			want := []alchemy.ModelCall{{Model: "fake-ocr", Stage: "ocr", Calls: 1}}
			if len(res.ModelCalls) != 1 || res.ModelCalls[0] != want[0] {
				t.Errorf("ModelCalls = %+v, want %+v", res.ModelCalls, want)
			}
			// What OCR was handed must be an image. Handing it the PDF's own
			// bytes is the harness-rs bug wearing a different hat.
			if ocr.calls != 1 {
				t.Fatalf("OCR called %d times, want 1", ocr.calls)
			}
			if bytes.HasPrefix(ocr.pages[0], []byte("%PDF-")) {
				t.Fatal("OCR was handed the PDF file instead of a page image")
			}
			cfg, format, err := image.DecodeConfig(bytes.NewReader(ocr.pages[0]))
			if err != nil {
				t.Fatalf("bytes handed to OCR do not decode as an image: %v", err)
			}
			if !strings.Contains(ocr.media[0], format) {
				t.Errorf("media type %q does not match the actual format %q", ocr.media[0], format)
			}
			if cfg.Width != 64 || cfg.Height != 48 {
				t.Errorf("page image is %dx%d, want 64x48", cfg.Width, cfg.Height)
			}
		})
	}
}

func TestScannedPageWhereOCRReturnsNothingIsReportedUnread(t *testing.T) {
	ocr := &fakeOCR{text: "   \n  "}
	res, err := Read(context.Background(), "scan.pdf", bytes.NewReader(jpegScanPDF(32, 32)), ocr)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.TrimSpace(res.Text) != "" {
		t.Errorf("Text = %q, want empty", res.Text)
	}
	if len(res.Unread) != 1 {
		t.Fatalf("want 1 unread page, got %+v", res.Unread)
	}
	if !strings.Contains(strings.ToLower(res.Unread[0].Reason), "ocr") {
		t.Errorf("Reason = %q, want it to name OCR", res.Unread[0].Reason)
	}
	// The call still happened and still cost something.
	if len(res.ModelCalls) != 1 || res.ModelCalls[0].Calls != 1 {
		t.Errorf("ModelCalls = %+v, want the attempt recorded", res.ModelCalls)
	}
}

func TestScannedPageWhereOCRFailsIsReportedUnread(t *testing.T) {
	ocr := &fakeOCR{err: errors.New("model endpoint refused")}
	res, err := Read(context.Background(), "scan.pdf", bytes.NewReader(jpegScanPDF(32, 32)), ocr)
	if err != nil {
		t.Fatalf("an OCR failure is a page-level fact, not a document error: %v", err)
	}
	if strings.TrimSpace(res.Text) != "" {
		t.Errorf("Text = %q, want empty", res.Text)
	}
	if len(res.Unread) != 1 {
		t.Fatalf("want 1 unread page, got %+v", res.Unread)
	}
	if !strings.Contains(res.Unread[0].Reason, "model endpoint refused") {
		t.Errorf("Reason = %q, want the model's own error in it", res.Unread[0].Reason)
	}
}
