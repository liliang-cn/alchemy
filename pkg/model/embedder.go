package model

import (
	"context"
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// embedPath is the OpenAI embeddings endpoint.
const embedPath = "/embeddings"

// embedder is alchemy.Embedder, and also embed.UsageEmbedder, over an
// OpenAI-compatible embeddings endpoint. It does not implement that optional
// interface by importing pkg/embed — the method shape is the whole contract,
// and importing the stage that consumes this client to satisfy it would point
// the dependency the wrong way.
type embedder struct {
	*client
	dimensions *int
}

// NewEmbedder builds an alchemy.Embedder for e.
func NewEmbedder(e Endpoint) (alchemy.Embedder, error) {
	c, s, err := newClient(e, embedPath, embedOptions)
	if err != nil {
		return nil, err
	}
	return &embedder{client: c, dimensions: s.dimensions}, nil
}

type embedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions *int     `json:"dimensions,omitempty"`
}

// embedResponse keeps Index as a pointer so that "the field was absent" is
// distinguishable from "the field said 0". Defaulting a missing index to 0 is
// how a whole batch quietly collapses onto the first chunk.
type embedResponse struct {
	Data []struct {
		Index     *int      `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

func (e *embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vecs, _, err := e.EmbedUsage(ctx, texts)
	return vecs, err
}

// EmbedUsage is embed.UsageEmbedder: the vectors plus what the call cost, so
// §7.2's "by model and stage" report has a real number instead of the 0 that
// means "the provider does not report".
func (e *embedder) EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error) {
	// An empty batch is not a call: input:[] wastes a request and some
	// endpoints reject it outright.
	if len(texts) == 0 {
		return nil, 0, nil
	}
	body := embedRequest{Model: e.name, Input: texts, Dimensions: e.dimensions}
	var out embedResponse
	if err := e.postJSON(ctx, body, &out); err != nil {
		return nil, 0, err
	}
	vecs, err := alignByIndex(out, len(texts), e.name, e.baseURL+e.path)
	if err != nil {
		return nil, 0, err
	}
	return vecs, out.Usage.TotalTokens, nil
}

// alignByIndex puts the reply back into input order.
//
// The API documents an index field precisely because the order of data[] is
// not guaranteed, and a batch reordered by accident is not an error anyone
// sees: it is a vector attached to the wrong chunk, which is a search result
// that is quietly wrong forever. So nothing here trusts arrival order, and
// anything that cannot be aligned — a missing index, one out of range, one
// seen twice, a short reply — is an error rather than a guess. A guess is
// exactly the silent mis-attachment the index field exists to prevent.
func alignByIndex(out embedResponse, want int, name, url string) ([][]float32, error) {
	if len(out.Data) != want {
		return nil, fmt.Errorf("model %q at %s: the reply carried %d embeddings for %d inputs",
			name, url, len(out.Data), want)
	}
	vecs := make([][]float32, want)
	// seen is separate from vecs being nil, so that an entry which arrived
	// carrying an empty vector is reported as empty rather than as missing.
	// The two send a reader to different places: a provider dropping entries,
	// or a dimensions setting the endpoint would not honour.
	seen := make([]bool, want)
	for i, d := range out.Data {
		if d.Index == nil {
			return nil, fmt.Errorf("model %q at %s: embedding %d carried no index, so the batch cannot be aligned to its inputs",
				name, url, i)
		}
		idx := *d.Index
		if idx < 0 || idx >= want {
			return nil, fmt.Errorf("model %q at %s: embedding %d claims index %d, outside the batch of %d",
				name, url, i, idx, want)
		}
		if seen[idx] {
			return nil, fmt.Errorf("model %q at %s: two embeddings claim index %d",
				name, url, idx)
		}
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("model %q at %s: the embedding for index %d has no values",
				name, url, idx)
		}
		seen[idx] = true
		vecs[idx] = d.Embedding
	}
	for i := range seen {
		if !seen[i] {
			return nil, fmt.Errorf("model %q at %s: no embedding claimed index %d", name, url, i)
		}
	}
	return vecs, nil
}
