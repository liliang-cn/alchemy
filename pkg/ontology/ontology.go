// Package ontology is the vocabulary that constrains extraction and the
// rule-set verification checks against — the same list on both sides of the
// model (DESIGN.md §5b, third mechanism).
package ontology

import (
	"encoding/json"
	"fmt"
	"io"
)

// Part is the provenance of a vocabulary.
type Part string

// EntityType is one node type the ontology declares.
type EntityType struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Attributes  []string `json:"attributes,omitempty"`
}

// RelationType is one edge type the ontology declares.
type RelationType struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	From        []string `json:"from,omitempty"`
	To          []string `json:"to,omitempty"`
}

// Vocabulary is one part's closed list of types.
type Vocabulary struct {
	Entities  []EntityType   `json:"entities"`
	Relations []RelationType `json:"relations"`
}

// Ontology is a versioned, provenance-partitioned vocabulary.
type Ontology struct {
	ID    string              `json:"id"`
	Parts map[Part]Vocabulary `json:"parts"`
}

// Load reads a JSON ontology and validates it.
func Load(r io.Reader) (*Ontology, error) {
	var o Ontology
	if err := json.NewDecoder(r).Decode(&o); err != nil {
		return nil, fmt.Errorf("ontology: %w", err)
	}
	if o.ID == "" {
		return nil, fmt.Errorf("ontology: missing id")
	}
	return &o, nil
}
