package tabular

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The four kinds alchemy declares are all ontology-shaped — an edge whose type
// the vocabulary does not allow. A table fails in a way a schema cannot: a row
// that does not fit its own header. These kinds name that, and they are
// declared here rather than in pkg/alchemy because they are this reader's
// vocabulary and nothing else speaks them yet.
//
// What they share with the declared kinds is the rule that earns them: nothing
// is dropped silently. A row this package refuses is a row the caller is told
// about, with the line it was on, because a row that vanishes between the file
// and the graph is discovered the same way a wrong mapping is — months later,
// by hand.
const (
	// ViolationMalformedRow — a row that cannot be read against the header.
	ViolationMalformedRow alchemy.ViolationKind = "malformed_row"
	// ViolationUnnamedColumn — a header cell with no name, so no mapping can
	// refer to the column and its values are left out.
	ViolationUnnamedColumn alchemy.ViolationKind = "unnamed_column"
	// ViolationMissingID — a row whose identifying column is empty.
	ViolationMissingID alchemy.ViolationKind = "missing_id"
	// ViolationDuplicateID — two rows claiming the same identity, differently.
	ViolationDuplicateID alchemy.ViolationKind = "duplicate_id"
)

// at renders a locator a person can find in the file.
func at(source string, line int) string {
	return fmt.Sprintf("%s line %d", source, line)
}

func violation(kind alchemy.ViolationKind, subject, detail string, prov alchemy.Provenance) alchemy.Violation {
	return alchemy.Violation{Kind: kind, Subject: subject, Detail: detail, Provenance: prov}
}
