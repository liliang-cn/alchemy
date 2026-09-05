package verify

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// §5b promises a wrong record is "checkable, correctable, and excludable", and
// excludable was the word that failed: a violation named its subject in prose,
// so a sink holding the graph and the findings could only join them by parsing
// "a -[USES]-> b" back into three fields — a private copy of this package's
// output format that no test in either would notice drifting.
func TestAViolationAboutAnEntityNamesTheEntityInFields(t *testing.T) {
	rep := Check(Input{
		Entities: []alchemy.Entity{
			{ID: "flag:--verbose", Type: "Flag", Name: "--verbose",
				Provenance: alchemy.Provenance{Source: "cli.md", Chunk: 2, Producer: alchemy.ProducerLLMExtract}},
		},
		Vocabulary: proseVocab(t),
		OntologyID: "sds@1",
	})
	v := findViolation(t, rep, alchemy.ViolationUnknownEntityType)
	want := alchemy.Ref{Kind: alchemy.RefEntity, ID: "flag:--verbose", Type: "Flag"}
	if v.About != want {
		t.Fatalf("About = %+v, want %+v", v.About, want)
	}
}

// An edge's Ref carries the key for the reason Relation.Key exists: two
// foreign keys between one pair of tables render one subject string, so a
// consumer excluding the offender excludes both.
func TestAViolationAboutARelationNamesBothEndsTheTypeAndTheKey(t *testing.T) {
	rep := Check(Input{
		Entities: []alchemy.Entity{
			{ID: "cluster:a", Type: "Cluster", Name: "a"},
			{ID: "cluster:b", Type: "Cluster", Name: "b"},
		},
		Relations: []alchemy.Relation{
			{From: "cluster:a", To: "cluster:b", Type: "OWNS", Key: "fk_left"},
		},
		Vocabulary: proseVocab(t),
		OntologyID: "sds@1",
	})
	v := findViolation(t, rep, alchemy.ViolationUnknownRelationType)
	want := alchemy.Ref{Kind: alchemy.RefRelation, From: "cluster:a", To: "cluster:b", Type: "OWNS", Key: "fk_left"}
	if v.About != want {
		t.Fatalf("About = %+v, want %+v", v.About, want)
	}
}

// The Ref names the same edge Relation.Identity does, which is the whole point
// of carrying the four fields rather than a rendered string: a sink holding a
// finding can compute the identity of the record it is about and go straight to
// the row it wrote.
func TestARelationsRefIdentifiesTheSameEdgeTheRelationDoes(t *testing.T) {
	r := alchemy.Relation{From: "cluster:a", To: "cluster:b", Type: "OWNS", Key: "fk_left"}
	rep := Check(Input{
		Entities: []alchemy.Entity{
			{ID: "cluster:a", Type: "Cluster", Name: "a"},
			{ID: "cluster:b", Type: "Cluster", Name: "b"},
		},
		Relations:  []alchemy.Relation{r},
		Vocabulary: proseVocab(t),
	})
	v := findViolation(t, rep, alchemy.ViolationUnknownRelationType)
	about := alchemy.Relation{From: v.About.From, To: v.About.To, Type: v.About.Type, Key: v.About.Key}
	if about.Identity() != r.Identity() {
		t.Fatalf("the finding names %q and the record is %q", about.Identity(), r.Identity())
	}
}

// A dangling edge is the case where the join matters most: the record is in the
// graph and one of its ends is not, so a sink that wants to write the usable
// subgraph has to be able to find exactly this edge.
func TestADanglingViolationNamesTheEdgeInFields(t *testing.T) {
	rep := Check(Input{
		Entities:   []alchemy.Entity{{ID: "cluster:a", Type: "Cluster", Name: "a"}},
		Relations:  []alchemy.Relation{{From: "cluster:a", To: "cluster:ghost", Type: "DEPLOYED_ON"}},
		Vocabulary: proseVocab(t),
	})
	v := findViolation(t, rep, alchemy.ViolationDanglingRelation)
	if v.About.Kind != alchemy.RefRelation || v.About.To != "cluster:ghost" {
		t.Fatalf("About = %+v, want the edge and its missing end", v.About)
	}
}

// The rendered subject is untouched. It is what a review item is filed under,
// what a standing rule is matched on, and what review.Apply looks a record up
// by — so a structured companion is an addition and never a replacement.
func TestTheRenderedSubjectIsUnchanged(t *testing.T) {
	rep := Check(Input{
		Entities:   []alchemy.Entity{{ID: "cluster:a", Type: "Cluster", Name: "a"}},
		Relations:  []alchemy.Relation{{From: "cluster:a", To: "cluster:ghost", Type: "DEPLOYED_ON"}},
		Vocabulary: proseVocab(t),
	})
	v := findViolation(t, rep, alchemy.ViolationDanglingRelation)
	if v.Subject != "cluster:a -[DEPLOYED_ON]-> cluster:ghost" {
		t.Fatalf("Subject = %q, want the rendering a reviewer already answers", v.Subject)
	}
}

// The same promise asked of the other finding that names records: a Conflict.
//
// A violation names one record and a conflict names two, which is the whole
// reason the Ref is on the Claim rather than on the Conflict. What a store
// needs from it is the knowledge contract's `_contradicts` — the ids of the
// records this one cannot both-be-true with, written on both, because two
// sources disagreeing is information. Until the sides carried Refs the only
// route to those ids was parsing Conflict.Subject, which is the private copy of
// this package's output format that Ref exists to abolish.
func TestBothSidesOfATypeConflictNameTheEntityTheSideRead(t *testing.T) {
	rep := Check(Input{
		Entities: []alchemy.Entity{
			{ID: "n1", Type: "Node", Name: "node-1",
				Provenance: alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}},
			{ID: "n1", Type: "Cluster", Name: "node-1",
				Provenance: alchemy.Provenance{Source: "contract.pdf", Chunk: 41, Producer: alchemy.ProducerLLMExtract}},
		},
		Vocabulary: proseVocab(t),
		OntologyID: "sds@1",
	})
	c := findConflict(t, rep, alchemy.ConflictEntityType)
	// The type is part of an entity's Ref because it is part of what the
	// record claims — findings.go says so, and this is the conflict it says it
	// about. Two records both calling themselves n1 while typing it
	// differently are the whole of ConflictEntityType, and two Refs that could
	// not tell them apart would name one record twice.
	if want := (alchemy.Ref{Kind: alchemy.RefEntity, ID: "n1", Type: "Node"}); c.Left.About != want {
		t.Errorf("Left.About = %+v, want %+v", c.Left.About, want)
	}
	if want := (alchemy.Ref{Kind: alchemy.RefEntity, ID: "n1", Type: "Cluster"}); c.Right.About != want {
		t.Errorf("Right.About = %+v, want %+v", c.Right.About, want)
	}
}

// Two claims about one record carry one Ref twice, and it is the answer rather
// than a defect: an entity given two values for one attribute is one node in
// the graph. A consumer comparing the two Refs learns exactly that — the
// disagreement is inside a record, so there is no second record for
// `_contradicts` to point at.
func TestAnAttributeConflictNamesTheOneRecordFromBothSides(t *testing.T) {
	rep := Check(Input{
		Entities: []alchemy.Entity{
			{ID: "c1", Type: "Cluster", Name: "prod", Attributes: map[string]any{"region": "eu-central"},
				Provenance: alchemy.Provenance{Source: "schema.sql", Chunk: -1, Producer: alchemy.ProducerDDL}},
			{ID: "c1", Type: "Cluster", Name: "prod", Attributes: map[string]any{"region": "us-east"},
				Provenance: alchemy.Provenance{Source: "contract.pdf", Chunk: 41, Producer: alchemy.ProducerLLMExtract}},
		},
		Vocabulary: proseVocab(t),
		OntologyID: "sds@1",
	})
	c := findConflict(t, rep, alchemy.ConflictEntityAttributes)
	want := alchemy.Ref{Kind: alchemy.RefEntity, ID: "c1", Type: "Cluster"}
	if c.Left.About != want || c.Right.About != want {
		t.Fatalf("About = %+v / %+v, want both to name %+v — one node, two claims about it",
			c.Left.About, c.Right.About, want)
	}
	// And the subject is still the attribute, which is the shape findings.go
	// says a structured subject could not have held.
	if c.Subject != "c1.region" {
		t.Errorf("Subject = %q, want the attribute a person is asked about", c.Subject)
	}
}

// The case `_contradicts` exists for, in its plainest form: the graph really
// does hold both records, they cannot both be true, and neither is being
// removed. A reversal is two edges, so the two Refs differ — which is what
// lets a store write each one's id onto the other.
func TestADirectionConflictNamesTheTwoEdgesThatRunOppositeWays(t *testing.T) {
	rep := Check(Input{
		Entities: []alchemy.Entity{
			{ID: "cluster:a", Type: "Cluster", Name: "a"},
			{ID: "node:b", Type: "Node", Name: "b"},
		},
		Relations: []alchemy.Relation{
			{From: "cluster:a", To: "node:b", Type: "DEPLOYED_ON",
				Provenance: alchemy.Provenance{Source: "a.pdf", Chunk: 1, Producer: alchemy.ProducerLLMExtract}},
			{From: "node:b", To: "cluster:a", Type: "DEPLOYED_ON",
				Provenance: alchemy.Provenance{Source: "b.pdf", Chunk: 2, Producer: alchemy.ProducerLLMExtract}},
		},
		Vocabulary: proseVocab(t),
		OntologyID: "sds@1",
	})
	c := findConflict(t, rep, alchemy.ConflictRelationDirection)
	left := alchemy.Ref{Kind: alchemy.RefRelation, From: "cluster:a", To: "node:b", Type: "DEPLOYED_ON"}
	right := alchemy.Ref{Kind: alchemy.RefRelation, From: "node:b", To: "cluster:a", Type: "DEPLOYED_ON"}
	if c.Left.About != left || c.Right.About != right {
		t.Fatalf("About = %+v / %+v, want %+v / %+v — each side names the edge its own source drew",
			c.Left.About, c.Right.About, left, right)
	}
}

// The other kind whose two Refs genuinely differ, and the one §5b's "a fact has
// to be able to go out of date" is about: two well-formed edges that break no
// rule of their own, which the ontology alone says cannot both stand.
func TestACardinalityConflictNamesBothEdgesTheOntologyWillNotHaveTogether(t *testing.T) {
	rep := Check(Input{
		Entities: []alchemy.Entity{
			{ID: "org:northgate", Type: "Org", Name: "Northgate"},
			{ID: "p:ada", Type: "Person", Name: "Ada"},
			{ID: "p:bruno", Type: "Person", Name: "Bruno"},
		},
		Relations: []alchemy.Relation{
			{From: "p:ada", To: "org:northgate", Type: "CTO_OF",
				Provenance: alchemy.Provenance{Source: "profile.pdf", Chunk: 1, Producer: alchemy.ProducerLLMExtract}},
			{From: "p:bruno", To: "org:northgate", Type: "CTO_OF",
				Provenance: alchemy.Provenance{Source: "correction.json", Chunk: -1, Producer: alchemy.ProducerGraphImport}},
		},
		Vocabulary: ontology.Vocabulary{
			Entities: []ontology.EntityType{{Name: "Org"}, {Name: "Person"}},
			Relations: []ontology.RelationType{
				{Name: "CTO_OF", From: []string{"Person"}, To: []string{"Org"}, AtMostOneIn: true},
			},
		},
		OntologyID: "company@1",
	})
	c := findConflict(t, rep, alchemy.ConflictCardinality)
	// The incumbent on the left and the newcomer on the right, the order
	// cardinality.go states: a decision on the item acts on the newcomer.
	left := alchemy.Ref{Kind: alchemy.RefRelation, From: "p:ada", To: "org:northgate", Type: "CTO_OF"}
	right := alchemy.Ref{Kind: alchemy.RefRelation, From: "p:bruno", To: "org:northgate", Type: "CTO_OF"}
	if c.Left.About != left || c.Right.About != right {
		t.Fatalf("About = %+v / %+v, want %+v / %+v", c.Left.About, c.Right.About, left, right)
	}
}

// A Ref that names an edge has to name the same edge the record does, or a
// consumer holding both finds nothing. It is the conflict version of
// TestARelationsRefIdentifiesTheSameEdgeTheRelationDoes, and it is worth its
// own test because a conflict's Refs are built where the record is remembered
// rather than where it is read.
func TestEachSideOfARelationConflictIdentifiesTheEdgeItsRecordDoes(t *testing.T) {
	one := alchemy.Relation{From: "cluster:a", To: "node:b", Type: "DEPLOYED_ON",
		Provenance: alchemy.Provenance{Source: "a.pdf", Chunk: 1, Producer: alchemy.ProducerLLMExtract}}
	two := alchemy.Relation{From: "node:b", To: "cluster:a", Type: "DEPLOYED_ON",
		Provenance: alchemy.Provenance{Source: "b.pdf", Chunk: 2, Producer: alchemy.ProducerLLMExtract}}
	rep := Check(Input{
		Entities: []alchemy.Entity{
			{ID: "cluster:a", Type: "Cluster", Name: "a"},
			{ID: "node:b", Type: "Node", Name: "b"},
		},
		Relations:  []alchemy.Relation{one, two},
		Vocabulary: proseVocab(t),
		OntologyID: "sds@1",
	})
	c := findConflict(t, rep, alchemy.ConflictRelationDirection)
	for _, side := range []struct {
		name  string
		about alchemy.Ref
		want  alchemy.Relation
	}{{"Left", c.Left.About, one}, {"Right", c.Right.About, two}} {
		got := alchemy.Relation{From: side.about.From, To: side.about.To, Type: side.about.Type, Key: side.about.Key}
		if got.Identity() != side.want.Identity() {
			t.Errorf("%s names %q and its record is %q", side.name, got.Identity(), side.want.Identity())
		}
	}
}

func findConflict(t *testing.T, rep Report, kind alchemy.ConflictKind) alchemy.Conflict {
	t.Helper()
	for _, c := range rep.Conflicts {
		if c.Kind == kind {
			return c
		}
	}
	t.Fatalf("no %s conflict in %+v", kind, rep.Conflicts)
	return alchemy.Conflict{}
}

func findViolation(t *testing.T, rep Report, kind alchemy.ViolationKind) alchemy.Violation {
	t.Helper()
	for _, v := range rep.Violations {
		if v.Kind == kind {
			return v
		}
	}
	t.Fatalf("no %s violation in %+v", kind, rep.Violations)
	return alchemy.Violation{}
}

// proseVocab declares Cluster and DEPLOYED_ON and nothing else, so a Flag
// entity and an OWNS edge are each undeclared for their own reason.
func proseVocab(t *testing.T) ontology.Vocabulary {
	t.Helper()
	return ontology.Vocabulary{
		Entities:  []ontology.EntityType{{Name: "Cluster"}, {Name: "Node"}},
		Relations: []ontology.RelationType{{Name: "DEPLOYED_ON", From: []string{"Cluster"}, To: []string{"Node"}}},
	}
}
