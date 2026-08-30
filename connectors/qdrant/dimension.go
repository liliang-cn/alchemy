package qdrant

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// dimensionOf is the check a result has to pass before any of it is written:
// one dimension across the whole result, no empty vectors, and every vector
// naming a chunk that exists. A collection is created at one width and cannot
// be changed, which makes this the last moment the question can be asked.
//
// It is now the same three questions pkg/preflight asks of every store, and it
// stays here for two reasons rather than being deleted as a duplicate. Load
// runs it first so that a caller who has always matched on *DimensionError
// still gets one — §4.1 moved the shared refusals above the line, not this
// store's account of them. And Begin needs the width before it can create a
// collection, which pkg/sink supplies from the same result by the same rule;
// this is where that rule is stated in this store's own words.
func dimensionOf(res alchemy.Result) (int, string, error) {
	chunks := make(map[int]bool, len(res.Chunks))
	for _, c := range res.Chunks {
		chunks[c.Index] = true
	}
	dim, model := 0, ""
	for _, v := range res.Vectors {
		if len(v.Values) == 0 {
			return 0, "", fmt.Errorf("qdrant: the vector for chunk %d is empty; "+
				"an embedding nobody can search is not one worth storing", v.Chunk)
		}
		if !chunks[v.Chunk] {
			return 0, "", fmt.Errorf("qdrant: a vector names chunk %d and the result has no such chunk; "+
				"storing it would leave an embedding with no text behind it", v.Chunk)
		}
		if dim == 0 {
			dim, model = len(v.Values), v.Model
			continue
		}
		if len(v.Values) != dim {
			return 0, "", &DimensionError{
				Have: dim, Want: len(v.Values), Model: v.Model,
				Where: fmt.Sprintf("chunk %d", v.Chunk),
			}
		}
	}
	return dim, model, nil
}
