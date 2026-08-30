package runner

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/chunk"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/service"
)

// §8.4: what the service passes on is a path, never bytes. The proof is that
// the file does not have to exist when the request is built — a builder that
// slurped the corpus would fail here, and would be the "10GB dump parsed by
// reading it into a string" the design refuses.
func TestRequestOpensSourcesLazily(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spooled")

	spec := service.JobSpec{Sources: []service.Source{
		{ID: "s1", Name: "schema.sql", Kind: alchemy.SourceDDL, Path: path},
	}}
	req, err := buildRequest("job-under-test", spec, nil, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(req.Sources) != 1 {
		t.Fatalf("got %d sources, want 1", len(req.Sources))
	}
	src := req.Sources[0]
	if src.Name != "schema.sql" || src.Kind != alchemy.SourceDDL {
		t.Fatalf("source = %q/%q, want schema.sql/ddl", src.Name, src.Kind)
	}
	if src.Open == nil {
		t.Fatal("source has no Open function")
	}

	// Only now do the bytes exist.
	if err := os.WriteFile(path, []byte("CREATE TABLE t (id INT);"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := src.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "CREATE TABLE t (id INT);" {
		t.Fatalf("read %q", got)
	}
}

const proseOntology = `{"id":"sds@1","parts":{"prose":{"entities":[{"name":"Cluster"}],"relations":[]}}}`

// An empty ontology is passed on as no ontology, not refused. §5's rule — an
// ontology is required for document sources — is enforced by pipeline.validate,
// and a copy of it here would be a second place for it to drift.
func TestRequestLeavesTheOntologyRuleToThePipeline(t *testing.T) {
	req, err := buildRequest("job-under-test", service.JobSpec{}, nil, nil)
	if err != nil {
		t.Fatalf("buildRequest with no ontology: %v", err)
	}
	if req.Ontology != nil {
		t.Fatalf("empty ontology became %v, want nil", req.Ontology)
	}
}

func TestRequestLoadsTheOntology(t *testing.T) {
	req, err := buildRequest("job-under-test", service.JobSpec{Ontology: proseOntology}, nil, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.Ontology == nil {
		t.Fatal("ontology was not loaded")
	}
	if req.Ontology.ID != "sds@1" {
		t.Fatalf("ontology ID = %q, want sds@1", req.Ontology.ID)
	}
	// One job is one corpus under one part, and prose is the part a document
	// job is extracted under.
	if req.Part != ontology.PartProse {
		t.Fatalf("part = %q, want %q", req.Part, ontology.PartProse)
	}
}

// A malformed ontology is the caller's mistake and must arrive as one, before
// a single byte of the corpus is read.
func TestRequestRefusesAMalformedOntology(t *testing.T) {
	_, err := buildRequest("job-under-test", service.JobSpec{Ontology: "{not json"}, nil, nil)
	if err == nil {
		t.Fatal("buildRequest accepted a malformed ontology")
	}
}

// §7.1: the person who knows the corpus is the caller, so what they chose has
// to survive the trip. A zero field stays zero — chunk.Options owns the
// defaults, including the one that matters, a non-zero overlap.
func TestRequestCarriesChunking(t *testing.T) {
	req, err := buildRequest("job-under-test", service.JobSpec{
		Chunking: service.Chunking{Strategy: "sentence", Size: 400, Overlap: 40},
	}, nil, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	want := chunk.Options{Strategy: chunk.Sentence, MaxTokens: 400, Overlap: 40}
	if req.Chunking != want {
		t.Fatalf("chunking = %+v, want %+v", req.Chunking, want)
	}

	empty, err := buildRequest("job-under-test", service.JobSpec{}, nil, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if empty.Chunking != (chunk.Options{}) {
		t.Fatalf("unset chunking = %+v, want the zero Options so chunk owns the defaults", empty.Chunking)
	}
}

// A decision made before the run starts must reach the run, which is §6's whole
// reason for a bidirectional stream: "a decision reaches an extraction that has
// not run yet."
//
// It reads through the inbox rather than off the Request because the Request no
// longer holds a copy: the two are asked for while the job runs, and asking is
// the contract. What this pins is that both halves of the conversation arrive —
// the rules the caller stated when the job was created and the answers the
// service has since been given — and that the stated ones come first, which is
// which of two rules covering one item gets the credit for it.
func TestRequestCarriesReviewAndDecisions(t *testing.T) {
	stated := review.Rule{Shape: "violation/entity_type/Cluster"}
	live := review.Rule{Shape: "violation/entity_type/Widget"}
	in := fakeInbox{
		decisions: []review.Decision{{ItemID: "conflict/entity_attributes/n1", Verb: review.VerbAccept, By: "ops"}},
		rules:     []review.Rule{live},
	}
	req, err := buildRequest("job-under-test", service.JobSpec{
		Review: review.Options{Reviewing: true, MinConfidence: 0.7, Rules: []review.Rule{stated}},
	}, in, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if !req.Reviewing || req.MinConfidence != 0.7 {
		t.Fatalf("review = %v/%v, want true/0.7", req.Reviewing, req.MinConfidence)
	}
	rules := req.Inbox.Rules()
	if len(rules) != 2 || rules[0].Shape != stated.Shape || rules[1].Shape != live.Shape {
		t.Fatalf("rules = %+v, want the stated one and then the live one", rules)
	}
	decisions := req.Inbox.Decisions()
	if len(decisions) != 1 || decisions[0].ItemID != "conflict/entity_attributes/n1" {
		t.Fatalf("decisions = %+v, want the one already in the inbox", decisions)
	}
}

// A nil Inbox is a caller with nowhere for decisions to come from, not a panic
// — and not a job that loses the rules it was created with either.
func TestRequestToleratesNoInbox(t *testing.T) {
	stated := review.Rule{Shape: "violation/entity_type/Cluster"}
	req, err := buildRequest("job-under-test", service.JobSpec{Review: review.Options{Rules: []review.Rule{stated}}}, nil, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.Inbox.Decisions() != nil {
		t.Fatalf("decisions = %+v, want none", req.Inbox.Decisions())
	}
	if rules := req.Inbox.Rules(); len(rules) != 1 || rules[0].Shape != stated.Shape {
		t.Fatalf("rules = %+v, want the one the job was created with", rules)
	}
}

// fakeInbox is a snapshot of the review conversation, which is exactly what
// service.Inbox is.
type fakeInbox struct {
	decisions []review.Decision
	rules     []review.Rule
}

func (f fakeInbox) Decisions() []review.Decision { return f.decisions }
func (f fakeInbox) Rules() []review.Rule         { return f.rules }

// The service knows which job it is running and hands the ID to Run; until now
// it stopped here. §4 makes the returned JSON the contract, and a graph that
// cannot say which run produced it is one every store has to name for itself —
// which four of them did, four ways.
func TestTheJobIDReachesThePipelineAndSoTheResult(t *testing.T) {
	req, err := buildRequest("job-42", service.JobSpec{
		Sources: []service.Source{{ID: "s1", Kind: alchemy.SourceDDL, Path: "x"}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.Job != "job-42" {
		t.Fatalf("Job = %q, want the job the service is running", req.Job)
	}
}
