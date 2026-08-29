package model

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// chatPath is the OpenAI chat endpoint. Both the LLM and the OCR use it: a
// vision model is a chat model that was handed an image.
const chatPath = "/chat/completions"

// llm is alchemy.LLM over an OpenAI-compatible chat endpoint.
type llm struct {
	*client
	params chatParams
}

// NewLLM builds an alchemy.LLM for e.
func NewLLM(e Endpoint) (alchemy.LLM, error) {
	c, s, err := newClient(e, chatPath, chatOptions)
	if err != nil {
		return nil, err
	}
	return &llm{client: c, params: chatParams{temperature: s.temperature, maxTokens: s.maxTokens}}, nil
}

// chatRequest is the wire shape of one chat completion. Pointers and omitempty
// throughout: an endpoint that was given no temperature must receive no
// temperature field, because a gateway's own default is a better answer than
// a zero this package invented.
type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    *float64      `json:"temperature,omitempty"`
	MaxTokens      *int          `json:"max_tokens,omitempty"`
	ResponseFormat *respFormat   `json:"response_format,omitempty"`
}

type respFormat struct {
	Type string `json:"type"`
}

// chatMessage carries either a plain string content (chat) or the content-part
// array a vision model needs (OCR), so Content is any rather than string.
type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// chatParams are the knobs Options may set on a chat call.
type chatParams struct {
	temperature *float64
	maxTokens   *int
}

func (l *llm) Complete(ctx context.Context, req alchemy.LLMRequest) (alchemy.LLMResponse, error) {
	body := chatRequest{
		Model:       l.name,
		Temperature: l.params.temperature,
		MaxTokens:   l.params.maxTokens,
	}
	// An empty System sends no system turn at all: some endpoints reject a
	// message with no content, and a job that supplied no instruction did not
	// ask for one to be invented.
	if req.System != "" {
		body.Messages = append(body.Messages, chatMessage{Role: "system", Content: req.System})
	}
	body.Messages = append(body.Messages, chatMessage{Role: "user", Content: req.Prompt})
	if req.JSON {
		// Asking is all this can do. A server free to ignore response_format
		// will, and unwrapping the fence it answers with is pkg/extract's job,
		// so nothing here assumes the reply parses.
		body.ResponseFormat = &respFormat{Type: "json_object"}
	}

	var out chatResponse
	if err := l.postJSON(ctx, body, &out); err != nil {
		return alchemy.LLMResponse{}, err
	}
	if len(out.Choices) == 0 {
		// Not an empty answer: an endpoint that returned no choice has failed,
		// and calling that "" hands the extractor a chunk that produced
		// nothing for a reason nobody recorded.
		return alchemy.LLMResponse{}, fmt.Errorf("model %q at %s: the reply carried no choices", l.name, l.baseURL+l.path)
	}
	// Tokens stays 0 when usage is absent — alchemy.ModelCall documents 0 as
	// "the provider did not report", and an invented count is a wrong number
	// about money (§7.2).
	return alchemy.LLMResponse{Text: out.Choices[0].Message.Content, Tokens: out.Usage.TotalTokens}, nil
}
