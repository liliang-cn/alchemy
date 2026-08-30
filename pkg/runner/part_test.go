package runner

import (
	"context"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/service"
)

// TestThePartAJobIsExtractedUnder. pipeline.Run resolves one part of the
// ontology and refuses a job whose ontology does not declare it. Until now the
// part was hardcoded to prose here, so an ontology that declares only a schema
// part was refused with "declares no prose part" and the caller had no way to
// say otherwise — a rule the design states about corpora, enforced as a rule
// about this translator.
//
// The empty case is the one worth pinning: it means prose, because prose is
// what a document source is and documents are what §5 puts in the first
// release. A caller who says nothing is asking for the release's subject.
func TestThePartAJobIsExtractedUnder(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want ontology.Part
	}{
		{name: "unstated means prose", spec: "", want: ontology.PartProse},
		{name: "stated is carried", spec: "schema", want: ontology.PartSchema},
		{name: "another stated part", spec: "tabular", want: ontology.PartTabular},
		{
			// Not refused here. ontology.Vocabulary is the one place that knows
			// which parts a document declares, and it answers with the list;
			// a second refusal here would be a second closed set to keep in
			// step with pkg/ontology's.
			name: "an unknown part is carried too, and refused where the vocabulary is",
			spec: "prosee",
			want: ontology.Part("prosee"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := buildRequest(service.JobSpec{Part: tc.spec}, nil, nil)
			if err != nil {
				t.Fatalf("buildRequest: %v", err)
			}
			if req.Part != tc.want {
				t.Fatalf("Part = %q, want %q", req.Part, tc.want)
			}
		})
	}
}

// TestAJobUnderASchemaOnlyOntologyRuns is the operator's version of the test
// above. The symptom this field exists to remove is a job whose ontology
// declares exactly the part its sources are, refused for not declaring a
// different one.
func TestAJobUnderASchemaOnlyOntologyRuns(t *testing.T) {
	const schemaOnly = `{"id":"sds@1","parts":{"schema":{"entities":[{"name":"Table"}],"relations":[{"name":"REFERENCES"}]}}}`
	spec := service.JobSpec{
		Sources:  []service.Source{spool(t, alchemy.SourceDDL, "schema.sql", "CREATE TABLE users (id INT PRIMARY KEY);")},
		Ontology: schemaOnly,
		Part:     "schema",
	}

	events, finish := collect(t)
	res, err := newRunner(t).Run(context.Background(), "job-schema", spec, events, nil)
	finish()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Entities) == 0 {
		t.Fatal("the job produced no entities")
	}
}
