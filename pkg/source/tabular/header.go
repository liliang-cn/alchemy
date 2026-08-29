package tabular

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// bom is the UTF-8 byte-order mark some exporters write. It is an encoding
// artefact rather than data: left in place it renames the first column to
// "\ufeffid", which then matches no mapping — a mapping made wrong by
// punctuation. Removing it loses nothing, so nothing is reported.
const bom = "\ufeff"

func stripBOM(br *bufio.Reader) {
	if head, _ := br.Peek(len(bom)); string(head) == bom {
		_, _ = br.Discard(len(bom))
	}
}

// readHeader reads and checks the first record.
//
// Two failures are treated differently on purpose. A duplicated name makes
// every mapping that refers to it undecidable — "id" would mean whichever "id"
// came first, which is §2.1's failure exactly — so the table is refused. An
// empty name makes a column unreferenceable rather than ambiguous: no mapping
// can name it, nothing else in the table is affected, so the column is left out
// and the caller is told which one.
func readHeader(source string, cr *csv.Reader) ([]string, []alchemy.Violation, error) {
	rec, err := cr.Read()
	if err == io.EOF {
		// "EOF" is a true description of an empty source and a useless one.
		return nil, nil, fmt.Errorf("the source has no header row")
	}
	if err != nil {
		return nil, nil, err
	}
	head := make([]string, len(rec))
	for i, h := range rec {
		head[i] = strings.TrimSpace(strings.TrimPrefix(h, bom))
	}
	var vs []alchemy.Violation
	prov := alchemy.Provenance{Source: source, Chunk: -1, Producer: alchemy.ProducerTabular}
	seen := map[string]int{}
	for i, h := range head {
		if h == "" {
			vs = append(vs, violation(ViolationUnnamedColumn,
				fmt.Sprintf("%s column %d", source, i+1),
				fmt.Sprintf("column %d of the header has no name, so no mapping can refer to it; its values are left out", i+1),
				prov))
			continue
		}
		if first, dup := seen[h]; dup {
			return nil, nil, fmt.Errorf("the header names %q twice, at columns %d and %d; any mapping naming it would be decided by column order", h, first+1, i+1)
		}
		seen[h] = i
	}
	return head, vs, nil
}

// columnIndex maps a name to its position. Unnamed columns are absent from it,
// which is how they stay out of the graph.
func columnIndex(head []string) map[string]int {
	index := map[string]int{}
	for i, h := range head {
		if h != "" {
			index[h] = i
		}
	}
	return index
}

// named is the header without its unnamed columns, for what the model is shown.
func named(head []string) []string {
	var out []string
	for _, h := range head {
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}
