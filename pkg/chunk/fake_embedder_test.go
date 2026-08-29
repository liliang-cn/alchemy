package chunk

import (
	"context"
	"errors"
	"strings"
)

// topicEmbedder is a deterministic stand-in for a real embedding model: it
// places a text in one of three corners depending on which topic word it
// contains. Deterministic vectors let the tests assert where the splitter puts
// its boundaries, which is the behaviour that matters — a mock counting calls
// would only assert that the code calls the code.
type topicEmbedder struct {
	err   error
	short bool // return fewer vectors than texts, to test the mismatch path
}

func (e *topicEmbedder) Name() string { return "topic-fake" }

func (e *topicEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		switch {
		case strings.Contains(t, "cat"):
			out = append(out, []float32{1, 0, 0})
		case strings.Contains(t, "database"):
			out = append(out, []float32{0, 1, 0})
		default:
			out = append(out, []float32{0, 0, 1})
		}
	}
	if e.short && len(out) > 1 {
		out = out[:len(out)-1]
	}
	return out, nil
}

var errEmbedderDown = errors.New("embedder is down")
