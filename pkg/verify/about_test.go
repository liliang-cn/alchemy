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
