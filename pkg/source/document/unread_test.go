package document

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestScannedPageWithoutOCRIsReportedUnreadNeverEmptyText is the reason this
// package exists, so it is the first test in it. DESIGN.md §5 names the
// harness-rs failure exactly: pdf-extract returned nothing for a scan, and the
// caller could not tell that from a blank page. A page with no text layer and
// no OCR model must arrive in Result.Unread, naming the page and why.
func TestScannedPageWithoutOCRIsReportedUnreadNeverEmptyText(t *testing.T) {
	res, err := Read(context.Background(), "scan.pdf", bytes.NewReader(scannedPagePDF()), nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.TrimSpace(res.Text) != "" {
		t.Fatalf("a page with no text layer produced text: %q", res.Text)
	}
	if len(res.Unread) != 1 {
		t.Fatalf("want 1 unread page, got %d: %+v", len(res.Unread), res.Unread)
	}
	u := res.Unread[0]
	if u.Source != "scan.pdf" {
		t.Errorf("Unread.Source = %q, want scan.pdf", u.Source)
	}
	if !strings.Contains(u.Locator, "1") {
		t.Errorf("Unread.Locator = %q, want it to name page 1", u.Locator)
	}
	if u.Reason == "" {
		t.Error("Unread.Reason is empty; a page reported unread must say why")
	}
	if !strings.Contains(strings.ToLower(u.Reason), "ocr") {
		t.Errorf("Unread.Reason = %q, want it to say no OCR model was supplied", u.Reason)
	}
}
