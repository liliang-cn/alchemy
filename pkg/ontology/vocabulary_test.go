package ontology_test

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// twoPartDoc is the ontology the partition tests share: a prose vocabulary a
// model reads out of documentation, and a code vocabulary a deterministic
// extractor mines out of source. The two overlap on the word "contains" and on
// nothing else.
const twoPartDoc = `{
  "id": "sds@3",
  "parts": {
    "prose": {
      "entities": [
        {"name": "Cluster", "description": "A set of nodes under one control plane.",
         "attributes": ["region", "version"]},
        {"name": "Node", "description": "One machine in a cluster."},
        {"name": "StoragePool", "description": "Backing storage a node offers."}
      ],
      "relations": [
        {"name": "CONTAINS", "description": "A cluster holds every node under it.",
         "from": ["Cluster"], "to": ["Node"]},
        {"name": "DEPLOYED_ON", "description": "A pool is deployed on a node.",
         "from": ["StoragePool"], "to": ["Node"]},
        {"name": "MENTIONS", "description": "The text names one thing while describing another."}
      ]
    },
    "code": {
      "entities": [{"name": "file"}, {"name": "function"}],
      "relations": [{"name": "contains", "from": ["file"], "to": ["function"]},
                    {"name": "calls", "from": ["function"], "to": ["function"]}]
    }
  }
}`

func load(t *testing.T, doc string) *ontology.Ontology {
	t.Helper()
	o, err := ontology.Load(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return o
}

func TestVocabularyReturnsOnlyTheRequestedPart(t *testing.T) {
	o := load(t, twoPartDoc)

	prose, err := o.Vocabulary(ontology.PartProse)
	if err != nil {
		t.Fatalf("Vocabulary(prose): %v", err)
	}
	if len(prose.Entities) != 3 || prose.Entities[0].Name != "Cluster" {
		t.Fatalf("prose entities = %+v", prose.Entities)
	}
	for _, e := range prose.Entities {
		if e.Name == "file" || e.Name == "function" {
			t.Fatalf("the prose vocabulary leaked the code entity type %q", e.Name)
		}
	}

	code, err := o.Vocabulary(ontology.PartCode)
	if err != nil {
		t.Fatalf("Vocabulary(code): %v", err)
	}
	if len(code.Entities) != 2 {
		t.Fatalf("code entities = %+v", code.Entities)
	}
}

// A part the ontology does not declare is an error, never an empty
// Vocabulary — an empty one would render a prompt that constrains nothing, and
// the caller would have no way to tell that from a vocabulary that happened to
// be small.
func TestVocabularyRejectsAnUndeclaredPart(t *testing.T) {
	o := load(t, twoPartDoc)
	v, err := o.Vocabulary(ontology.PartTabular)
	if err == nil {
		t.Fatalf("Vocabulary(tabular) returned %+v with no error; want an error", v)
	}
	if !strings.Contains(err.Error(), "sds@3") || !strings.Contains(err.Error(), "tabular") {
		t.Fatalf("error names neither the ontology nor the part: %v", err)
	}
}

// The returned Vocabulary is a copy. A caller that appends to what it got back
// must not be able to grow the ontology every later caller sees — least of all
// grow the prose vocabulary with a code type.
func TestVocabularyReturnsACopy(t *testing.T) {
	o := load(t, twoPartDoc)
	prose, err := o.Vocabulary(ontology.PartProse)
	if err != nil {
		t.Fatalf("Vocabulary(prose): %v", err)
	}
	prose.Entities = append(prose.Entities, ontology.EntityType{Name: "function"})
	prose.Relations[0].From[0] = "function"

	again, err := o.Vocabulary(ontology.PartProse)
	if err != nil {
		t.Fatalf("Vocabulary(prose): %v", err)
	}
	if len(again.Entities) != 3 {
		t.Fatalf("mutating the returned copy changed the ontology: %d entities", len(again.Entities))
	}
	if again.Relations[0].From[0] != "Cluster" {
		t.Fatalf("mutating the returned endpoints changed the ontology: %q", again.Relations[0].From[0])
	}
}

func TestAllowsEntity(t *testing.T) {
	prose, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)

	if !prose.AllowsEntity("Cluster") {
		t.Error("AllowsEntity(Cluster) = false; the prose part declares it")
	}
	if prose.AllowsEntity("Banana") {
		t.Error("AllowsEntity(Banana) = true; nothing declares it")
	}
	// The partition again, from the matching side: a code type is not a prose
	// type, however real it is elsewhere in the same document.
	if prose.AllowsEntity("function") {
		t.Error("AllowsEntity(function) = true in the prose part; that is a code type")
	}
}

// Models return Cluster, cluster and CLUSTER for one idea. Folding them is a
// tolerance for model spelling, not a licence for the document: the ontology
// still declares one canonical spelling, Prompt renders that one, and Canonical
// hands it back so a graph carries one name per type instead of three.
func TestMatchingIsCaseInsensitive(t *testing.T) {
	prose, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)

	for _, spelling := range []string{"Cluster", "cluster", "CLUSTER", "  cluster  "} {
		if !prose.AllowsEntity(spelling) {
			t.Errorf("AllowsEntity(%q) = false; case and surrounding space are model spelling, not a claim", spelling)
		}
		got, ok := prose.CanonicalEntity(spelling)
		if !ok || got != "Cluster" {
			t.Errorf("CanonicalEntity(%q) = (%q, %v); want (\"Cluster\", true)", spelling, got, ok)
		}
	}
	if ok, _ := prose.AllowsRelation("deployed_on", "storagepool", "NODE"); !ok {
		t.Error("AllowsRelation folds neither the relation name nor the endpoints")
	}
	if got, ok := prose.CanonicalRelation("deployed_on"); !ok || got != "DEPLOYED_ON" {
		t.Errorf("CanonicalRelation(deployed_on) = (%q, %v); want (\"DEPLOYED_ON\", true)", got, ok)
	}
	if _, ok := prose.CanonicalEntity("Banana"); ok {
		t.Error("CanonicalEntity(Banana) reported a canonical spelling for an undeclared type")
	}
}

// The reason becomes the Detail of an alchemy.Violation that a person acts on,
// so the three ways a relation can be wrong must read as three different
// problems. "The extractor invented an edge type" is a prompt or ontology
// problem; "it used a declared edge backwards" is an extraction problem; "one
// end is not a type at all" is usually a second violation's root cause. A
// reviewer sorting a queue of these needs to tell them apart at a glance.
func TestAllowsRelationGivesADistinctReasonForEachFailure(t *testing.T) {
	prose, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)

	undeclared := reject(t, prose, "OWNS", "Cluster", "Node")
	backwards := reject(t, prose, "CONTAINS", "Node", "Cluster")
	badEnd := reject(t, prose, "CONTAINS", "Cluster", "Banana")

	if !strings.Contains(undeclared, "not declared") || !strings.Contains(undeclared, "OWNS") {
		t.Errorf("undeclared relation reason does not say so: %q", undeclared)
	}
	// The second problem must not read as the first: CONTAINS *is* declared,
	// and telling a reviewer otherwise sends them to edit the wrong file.
	if !strings.Contains(backwards, "declared") || strings.Contains(backwards, "not declared") {
		t.Errorf("misused relation reason reads as an undeclared one: %q", backwards)
	}
	if !strings.Contains(backwards, "Node") || !strings.Contains(backwards, "Cluster") {
		t.Errorf("misused relation reason names neither end: %q", backwards)
	}
	if !strings.Contains(badEnd, "Banana") {
		t.Errorf("bad-endpoint reason does not name the offending type: %q", badEnd)
	}
	for a, x := range map[string]string{"undeclared": undeclared, "backwards": backwards, "badEnd": badEnd} {
		for b, y := range map[string]string{"undeclared": undeclared, "backwards": backwards, "badEnd": badEnd} {
			if a != b && x == y {
				t.Errorf("%s and %s produce the same reason %q", a, b, x)
			}
		}
	}
}

// An accepted relation must not carry a reason: a non-empty reason beside a
// true would end up in a Violation nobody should have raised.
func TestAllowsRelationSaysNothingWhenItAllows(t *testing.T) {
	prose, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)
	ok, reason := prose.AllowsRelation("CONTAINS", "Cluster", "Node")
	if !ok {
		t.Fatalf("AllowsRelation(CONTAINS, Cluster, Node) = false: %s", reason)
	}
	if reason != "" {
		t.Fatalf("reason = %q on an allowed relation; want empty", reason)
	}
}

// Empty From/To means "any entity type THIS PART declares" — never "anything".
//
// The alternative, requiring every relation to enumerate its ends, forces the
// author to either lie or maintain a list that rots in the dangerous
// direction: MENTIONS genuinely runs between anything, and an enumeration of
// it goes stale the moment a new entity type is added, at which point valid
// edges start arriving as violations and the ontology gets blamed for the
// extraction. Open ends are declared by omission, but they are still bounded
// by the part's entity list, and Prompt says "any entity type listed above" so
// the model is told the same thing the checker enforces.
func TestOpenEndpointsMeanAnyTypeDeclaredByThisPart(t *testing.T) {
	prose, _ := load(t, twoPartDoc).Vocabulary(ontology.PartProse)

	for _, ends := range [][2]string{
		{"Cluster", "Node"}, {"Node", "Cluster"}, {"StoragePool", "StoragePool"},
	} {
		if ok, reason := prose.AllowsRelation("MENTIONS", ends[0], ends[1]); !ok {
			t.Errorf("MENTIONS has open ends but rejected %s -> %s: %s", ends[0], ends[1], reason)
		}
	}
	// Bounded, not unconstrained.
	reason := reject(t, prose, "MENTIONS", "Cluster", "function")
	if !strings.Contains(reason, "function") {
		t.Errorf("open-ended relation accepted an undeclared endpoint, or did not name it: %q", reason)
	}
}

// Half-open is legal and means the same thing on the open side only.
func TestHalfOpenEndpoints(t *testing.T) {
	const doc = `{"id": "sds@1", "parts": {"prose": {
		"entities": [{"name": "Cluster"}, {"name": "Node"}],
		"relations": [{"name": "OWNS", "from": ["Cluster"]}]}}}`
	prose, _ := load(t, doc).Vocabulary(ontology.PartProse)

	if ok, reason := prose.AllowsRelation("OWNS", "Cluster", "Node"); !ok {
		t.Errorf("OWNS has an open to end but rejected Cluster -> Node: %s", reason)
	}
	if ok, _ := prose.AllowsRelation("OWNS", "Cluster", "Cluster"); !ok {
		t.Error("OWNS has an open to end but rejected Cluster -> Cluster")
	}
	if ok, _ := prose.AllowsRelation("OWNS", "Node", "Cluster"); ok {
		t.Error("OWNS declares from Cluster but accepted Node as the from end")
	}
}

func reject(t *testing.T, v ontology.Vocabulary, relType, from, to string) string {
	t.Helper()
	ok, reason := v.AllowsRelation(relType, from, to)
	if ok {
		t.Fatalf("AllowsRelation(%q, %q, %q) = true; want a rejection", relType, from, to)
	}
	if reason == "" {
		t.Fatalf("AllowsRelation(%q, %q, %q) rejected with no reason; the reason is the Detail of a Violation", relType, from, to)
	}
	return reason
}
