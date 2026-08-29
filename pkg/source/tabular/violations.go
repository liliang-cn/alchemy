package tabular

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// The kinds this reader emits are declared in pkg/alchemy, alongside the
// ontology-shaped ones, because Result.Violations is a single list with a
// single closed "kind" field. Aliased here so the reader's own code reads in
// its own vocabulary.
//
// The rule they share with the ontology kinds is the one that earns them:
// nothing is dropped silently. A row this package refuses is a row the caller
// is told about, with the line it was on, because a row that vanishes between
// the file and the graph is discovered the same way a wrong mapping is —
// months later, by hand.
const (
	ViolationMalformedRow  = alchemy.ViolationMalformedRow
	ViolationUnnamedColumn = alchemy.ViolationUnnamedColumn
	ViolationMissingID     = alchemy.ViolationMissingID
	ViolationDuplicateID   = alchemy.ViolationDuplicateID
)

// at renders a locator a person can find in the file.
func at(source string, line int) string {
	return fmt.Sprintf("%s line %d", source, line)
}

func violation(kind alchemy.ViolationKind, subject, detail string, prov alchemy.Provenance) alchemy.Violation {
	return alchemy.Violation{Kind: kind, Subject: subject, Detail: detail, Provenance: prov}
}
