// Package ontology is the vocabulary that constrains extraction and the
// rule-set verification checks against — the same list on both sides of the
// model (DESIGN.md §5b, third mechanism).
package ontology

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Part is the provenance of a vocabulary, and the partition is the whole
// point of this package. §2.1's third lesson:
//
//	The fields above are the PROSE vocabulary: an LLM reads documentation and
//	emits Cluster, Node, DEPLOYED_ON. They are pasted into the extractor's
//	prompt ("Use ONLY these entity types"), which is exactly why a code
//	vocabulary cannot live there — telling a prose extractor it may emit
//	`function` and `calls` invites it to invent code structure out of
//	documentation.
//
// So the partition is structural rather than advisory: a caller gets one part
// at a time from Vocabulary, Prompt renders only the part it was called on,
// and no method on Ontology renders or matches across parts. Handing a prose
// extractor the code vocabulary requires asking for it by name.
type Part string

const (
	// PartProse is what a model reads out of documentation.
	PartProse Part = "prose"
	// PartCode is what a deterministic extractor mines out of source.
	PartCode Part = "code"
	// PartTabular is what a header-and-rows mapping produces.
	PartTabular Part = "tabular"
	// PartSchema is what a CREATE TABLE and a FOREIGN KEY already state.
	PartSchema Part = "schema"
)

// knownParts is closed. A part name is not free text: a misspelled "prosee"
// would load cleanly, sit in the document forever, and leave the prose
// extractor asking for a part the ontology does not have — which is the empty
// vocabulary this package exists to make impossible.
var knownParts = map[Part]struct{}{
	PartProse: {}, PartCode: {}, PartTabular: {}, PartSchema: {},
}

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
	// BothWays says this relation may run either way between one pair of
	// things, and that both directions may be true at once.
	//
	// It exists because verify reads two records running opposite ways as two
	// sources contradicting each other, and that reading is an assertion about
	// the *type* — that it is asymmetric — which nothing here had a way to make
	// or to withhold. Over one customer's real code graph it produced 79
	// questions with no right answer: two Java classes that each import the
	// other is ordinary, legal, and correctly recorded, and §7.3 holds a job on
	// every one of them.
	//
	// It is deliberately not called Symmetric. Symmetric would be the claim
	// that A -> B *implies* B -> A, the way SIBLING_OF does — and an extractor
	// or a verifier that believed it would be entitled to write the reverse
	// edge itself, which is an edge no source ever asserted and no producer can
	// honestly be named for (§5b). `imports` is not that: the two directions
	// are two independent facts about two files, either may hold without the
	// other, and what the ontology needs a word for is only that neither
	// forbids the other. So this field licenses nothing to be added; it
	// withholds a contradiction.
	//
	// False is the default and it is the honest one. Every ontology written
	// before this field existed says nothing, and what its prompt has always
	// told the model is "extract it in that direction and never the reverse" —
	// so silence already meant one-way on the model's side, and reading it as
	// one-way on the checker's side is what keeps the two sides the same list
	// (§5b). It also keeps §5c's own example alive: a foreign key that says
	// orders -> customer against a document that says the customer owns the
	// orders is still a question for a person, and a permissive default would
	// have quietly retired it for every ontology in production.
	BothWays bool `json:"both_ways,omitempty"`
}

// Vocabulary is one part's closed list of types.
type Vocabulary struct {
	Entities  []EntityType   `json:"entities"`
	Relations []RelationType `json:"relations"`
}

// Ontology is a versioned, provenance-partitioned vocabulary.
//
// parts is unexported deliberately. An exported map would be a shared, mutable
// handle across every part at once: a caller could range over it and
// concatenate two parts' types in one statement, or assign the code vocabulary
// over the prose one after Load had validated it, and neither is a compile
// error. The partition is only structural if the parts are reachable one at a
// time and by name, which is Vocabulary; Parts reports which ones exist.
type Ontology struct {
	// ID is what lands in alchemy.Provenance.Ontology. §5b's example is
	// "sds@3", and the version half is required — see checkID.
	ID    string
	parts map[Part]Vocabulary
}

// wire is the JSON shape. It is separate from Ontology so that the parts map
// can stay unexported without teaching the exported type to unmarshal itself.
type wire struct {
	ID    string              `json:"id"`
	Parts map[Part]Vocabulary `json:"parts"`
}

// Parts reports the declared parts, sorted so that two loads of one document
// agree. It deliberately returns names and not vocabularies: a caller
// enumerating parts is usually about to ask for one, not to merge them all.
func (o *Ontology) Parts() []Part { return o.partNames() }

// Load reads a JSON ontology and validates it.
func Load(r io.Reader) (*Ontology, error) {
	var w wire
	dec := json.NewDecoder(r)
	// A misspelled key is not a harmless extra: "form" instead of "from"
	// leaves the relation with no declared start, and this package reads an
	// empty end as OPEN. The typo therefore widens the rules silently, which
	// is the one direction a mistake must never move them.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("ontology: %w", err)
	}
	o := Ontology{ID: w.ID, parts: w.Parts}
	if err := checkID(o.ID); err != nil {
		return nil, err
	}
	if err := o.check(); err != nil {
		return nil, err
	}
	return &o, nil
}

// checkID insists on "name@version".
//
// The id lands verbatim in alchemy.Provenance.Ontology, which is the only
// record a reader has of what a graph was checked against. An ontology is
// edited — a type added, an endpoint tightened — and "sds" identifies every
// edit of it equally, so two graphs produced under different rules would carry
// identical provenance and no way to tell them apart. Refusing the unversioned
// form is what makes that provenance falsifiable.
func checkID(id string) error {
	name, version, ok := strings.Cut(strings.TrimSpace(id), "@")
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("ontology: missing id (want \"name@version\", e.g. \"sds@3\")")
	}
	if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
		return fmt.Errorf("ontology: id %q has no version; write it as \"name@version\" (e.g. \"sds@3\") so a graph's provenance says which edit of the vocabulary checked it", id)
	}
	return nil
}

// check validates the whole document, so that every rule this package enforces
// at verification time was already enforced when the ontology was read. §5b's
// third mechanism is "the same list on both sides of the model"; a relation
// pointing at an entity type nobody declared would make the two sides differ,
// and it would do so at the far end of an expensive extraction run.
func (o *Ontology) check() error {
	if len(o.parts) == 0 {
		return fmt.Errorf("ontology %q: declares no parts; extraction under it would be unconstrained, and there is no unconstrained mode", o.ID)
	}
	for _, p := range o.partNames() {
		if _, ok := knownParts[p]; !ok {
			return fmt.Errorf("ontology %q: unknown part %q; parts are %s", o.ID, p, partList())
		}
		if err := o.parts[p].check(o.ID, p); err != nil {
			return err
		}
	}
	return nil
}

// partNames returns the declared parts in a stable order, so two loads of one
// bad document report the same error.
func (o *Ontology) partNames() []Part {
	names := make([]Part, 0, len(o.parts))
	for p := range o.parts {
		names = append(names, p)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

func partList() string {
	names := make([]string, 0, len(knownParts))
	for p := range knownParts {
		names = append(names, string(p))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// check validates one part.
func (v Vocabulary) check(id string, p Part) error {
	if len(v.Entities) == 0 {
		return fmt.Errorf("ontology %q: part %q declares no entity types; a prompt saying \"Use ONLY these entity types\" and then listing none forbids everything and permits nothing", id, p)
	}
	entities := make(map[string]struct{}, len(v.Entities))
	for _, e := range v.Entities {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			return fmt.Errorf("ontology %q: part %q has an entity type with no name", id, p)
		}
		if _, dup := entities[fold(name)]; dup {
			return fmt.Errorf("ontology %q: part %q declares entity type %q twice (names are matched case-insensitively, so two spellings of one name are one type)", id, p, name)
		}
		entities[fold(name)] = struct{}{}
	}
	relations := make(map[string]struct{}, len(v.Relations))
	for _, r := range v.Relations {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			return fmt.Errorf("ontology %q: part %q has a relation type with no name", id, p)
		}
		if _, dup := relations[fold(name)]; dup {
			return fmt.Errorf("ontology %q: part %q declares relation type %q twice (names are matched case-insensitively, so two spellings of one name are one type)", id, p, name)
		}
		relations[fold(name)] = struct{}{}
		for _, end := range []struct {
			field string
			types []string
		}{{"from", r.From}, {"to", r.To}} {
			for _, t := range end.types {
				if strings.TrimSpace(t) == "" {
					return fmt.Errorf("ontology %q: part %q: relation %q has a blank %s endpoint", id, p, name, end.field)
				}
				if _, ok := entities[fold(t)]; !ok {
					return fmt.Errorf("ontology %q: part %q: relation %q has %s %q, which this part does not declare as an entity type", id, p, name, end.field, t)
				}
			}
		}
	}
	return nil
}

// fold is the one place type-name matching decides what "the same name" means.
//
// Case-insensitive, deliberately. Models return Cluster, cluster and CLUSTER
// for the same idea, and a case difference coming back from a model is a
// spelling wobble, not a claim about the world. Counting each one as an
// alchemy.Violation would fill the review queue of §5c with items whose only
// content is a shifted letter — and §5c's own warning is that a queue
// containing the obvious is a queue people stop reading.
//
// oss-agent folded the other way (AllowsCodeRelation is deliberately
// case-sensitive) because its prose vocabulary SHOUTS and its code vocabulary
// whispers, and one flat list had to keep "a cluster CONTAINS a node" apart
// from "a file contains a function". This package does that with the partition
// instead: those two names live in different Vocabulary values and no method
// here compares across parts, so case is free to do the job it is actually
// good at — absorbing model spelling — rather than carrying provenance.
//
// The cost is paid at load: two case-variant spellings in one part are a
// duplicate, because a checker that folds them cannot tell them apart.
func fold(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// joinQuoted renders a list of names for an error message a person reads.
func joinQuoted(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}
