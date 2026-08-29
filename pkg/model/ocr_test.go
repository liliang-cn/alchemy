package model

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

// The data URI is the whole request: a vision endpoint given a malformed one
// answers about nothing, and a wrong media type is a PNG announced as a JPEG,
// which some providers reject and others decode into noise. So the bytes are
// asserted to survive the round trip exactly.
func TestRecognizeSendsThePageAsADataURI(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, `{"choices":[{"message":{"content":"INVOICE 41"}}]}`)

	o, err := NewOCR(Endpoint{Name: "qwen2-vl", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatalf("NewOCR: %v", err)
	}
	page := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0x00}
	text, err := o.Recognize(context.Background(), page, "image/png")
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if text != "INVOICE 41" {
		t.Errorf("Recognize = %q, want %q", text, "INVOICE 41")
	}
	if got.path != "/v1/chat/completions" {
		t.Errorf("posted to %q, want %q", got.path, "/v1/chat/completions")
	}

	url := imageURLOf(t, got)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(page)
	if url != want {
		t.Errorf("image_url.url = %q,\nwant %q", url, want)
	}
}

// A page whose media type is unknown is not a page to guess about: sending
// "data:;base64," is a request no endpoint can honour, and a defaulted
// image/png on a JPEG is the same class of quiet corruption.
func TestRecognizeRejectsAPageWithNoMediaType(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, `{"choices":[{"message":{"content":"x"}}]}`)

	o, _ := NewOCR(Endpoint{Name: "m", BaseURL: srv.URL})
	if _, err := o.Recognize(context.Background(), []byte{1, 2, 3}, ""); err == nil {
		t.Fatal("Recognize accepted a page with no media type")
	}
	if _, err := o.Recognize(context.Background(), nil, "image/png"); err == nil {
		t.Fatal("Recognize accepted an empty page")
	}
}

// The instruction is the guard against the harness-rs failure in a new
// costume: a model that helpfully describes the image produces a page of prose
// that is not what the page said, and that prose then flows into extraction as
// if it were the document. It must ask for the text and nothing else.
func TestRecognizeAsksForTheTextAndNothingElse(t *testing.T) {
	var got capture
	srv := chatServer(t, &got, `{"choices":[{"message":{"content":"x"}}]}`)

	o, _ := NewOCR(Endpoint{Name: "m", BaseURL: srv.URL})
	if _, err := o.Recognize(context.Background(), []byte{1}, "image/jpeg"); err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	instruction := strings.ToLower(textPartOf(t, got))
	if instruction == "" {
		t.Fatal("no text part accompanied the image: the model was sent a picture and no instruction")
	}
	for _, phrase := range []string{"transcribe", "do not describe"} {
		if !strings.Contains(instruction, phrase) {
			t.Errorf("the OCR instruction does not say %q; it reads:\n%s", phrase, instruction)
		}
	}
}

// imageURLOf digs the image_url out of the recorded request, failing with the
// raw body when the shape is not what a vision endpoint expects.
func imageURLOf(t *testing.T, got capture) string {
	t.Helper()
	for _, part := range contentPartsOf(t, got) {
		p, _ := part.(map[string]any)
		if p["type"] != "image_url" {
			continue
		}
		iu, ok := p["image_url"].(map[string]any)
		if !ok {
			t.Fatalf("image_url part has no image_url object: %s", got.raw)
		}
		url, _ := iu["url"].(string)
		return url
	}
	t.Fatalf("no image_url part in the request: %s", got.raw)
	return ""
}

func textPartOf(t *testing.T, got capture) string {
	t.Helper()
	for _, part := range contentPartsOf(t, got) {
		p, _ := part.(map[string]any)
		if p["type"] == "text" {
			s, _ := p["text"].(string)
			return s
		}
	}
	return ""
}

func contentPartsOf(t *testing.T, got capture) []any {
	t.Helper()
	msgs, _ := got.body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("no messages in the request: %s", got.raw)
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	parts, ok := last["content"].([]any)
	if !ok {
		t.Fatalf("the user message content is not a content-part array: %s", got.raw)
	}
	return parts
}
