package model

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// ocrInstruction is what the vision model is told to do with the page.
//
// Every line of it is defending against one failure. §5 names the one that
// matters: harness-rs sent raw PDF bytes through from_utf8_lossy and got an
// OCR that looked like it worked. The same failure wears a new costume here —
// a model that helpfully describes the image returns a page of prose that is
// not what the page said, and that prose then flows into extraction as if it
// were the document. Prose about a page is worse than no page, because an
// unread page is reported in Result.Unread and a described one is not
// reported at all.
//
// So: transcribe, do not describe, do not summarise, do not explain, and say
// nothing when there is nothing — an empty answer is a fact the pipeline can
// act on, and an apology in the middle of a corpus is text nobody wrote.
const ocrInstruction = `Transcribe all text in this image exactly as it appears.
Output only the transcribed text, nothing else.
Do not describe the image. Do not summarise, explain, translate or comment on it.
Do not add any introduction, heading or closing remark of your own.
Keep the reading order, the line breaks and the original language of the page.
If the image contains no text at all, output nothing.`

// ocr is alchemy.OCR over an OpenAI-compatible chat endpoint: a vision model
// is a chat model that was handed an image, so this posts to the same path.
type ocr struct {
	*client
	params chatParams
}

// NewOCR builds an alchemy.OCR for e.
func NewOCR(e Endpoint) (alchemy.OCR, error) {
	c, s, err := newClient(e, chatPath, chatOptions)
	if err != nil {
		return nil, err
	}
	return &ocr{client: c, params: chatParams{temperature: s.temperature, maxTokens: s.maxTokens}}, nil
}

// contentPart is one element of a vision message's content array.
type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

func (o *ocr) Recognize(ctx context.Context, page []byte, mediaType string) (string, error) {
	// Neither of these is a page to guess about. "data:;base64," is a request
	// no endpoint can honour, and a media type defaulted to image/png on a
	// JPEG is the same class of quiet corruption as the from_utf8_lossy that
	// §5 records: it looks like it worked.
	if len(page) == 0 {
		return "", fmt.Errorf("model %q: the page carried no bytes", o.name)
	}
	if strings.TrimSpace(mediaType) == "" {
		return "", fmt.Errorf("model %q: the page carried no media type, and guessing one is how a PNG gets announced as a JPEG", o.name)
	}

	uri := "data:" + strings.TrimSpace(mediaType) + ";base64," + base64.StdEncoding.EncodeToString(page)
	body := chatRequest{
		Model:       o.name,
		Temperature: o.params.temperature,
		MaxTokens:   o.params.maxTokens,
		Messages: []chatMessage{{
			Role: "user",
			// The instruction leads and the image follows: a model reading the
			// parts in order meets the constraint before it meets the thing it
			// would rather describe.
			Content: []contentPart{
				{Type: "text", Text: ocrInstruction},
				{Type: "image_url", ImageURL: &imageURL{URL: uri}},
			},
		}},
	}

	var out chatResponse
	if err := o.postJSON(ctx, body, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("model %q at %s: the reply carried no choices", o.name, o.baseURL+o.path)
	}
	// An empty transcription is returned as empty and not as an error: a page
	// with no text is a fact, and §5's contract is that the caller decides it
	// belongs in Unread — this client does not get to invent a failure for it.
	return out.Choices[0].Message.Content, nil
}
