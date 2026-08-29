package service_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// graph builds a result with n entities and n-1 relations, each carrying
// enough text that the whole thing is genuinely large rather than nominally so.
func graph(n int) alchemy.Result {
	prov := alchemy.Provenance{
		Source: "corpus.md", Chunk: 1, Producer: alchemy.ProducerLLMExtract,
		Model: "gpt-x", Ontology: "crm", Chunking: "heading", Confidence: 0.9,
	}
	res := alchemy.Result{Counts: alchemy.Counts{Entities: n, Relations: n - 1, Inferred: n}}
	for i := 0; i < n; i++ {
		res.Entities = append(res.Entities, alchemy.Entity{
			ID: fmt.Sprintf("e%d", i), Type: "Customer",
			Name:       strings.Repeat("Acme Holdings International ", 6),
			Provenance: prov,
		})
		if i > 0 {
			res.Relations = append(res.Relations, alchemy.Relation{
				From: fmt.Sprintf("e%d", i-1), To: fmt.Sprintf("e%d", i),
				Type: "supplies", Provenance: prov,
			})
		}
	}
	return res
}

// §8.4: a caller should never have to discover gRPC's 4MB limit by receiving a
// truncation. The refusal has to arrive as our sentence, and it has to name
// the RPC that works.
func TestGetResultRefusesAResultTooLargeForOneMessage(t *testing.T) {
	cli := dial(t, harness{run: staticResult(graph(20000))})
	src := upload(t, cli, "corpus.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	_, err := cli.GetResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition (err %v)", got, err)
	}
	if msg := status.Convert(err).Message(); !strings.Contains(msg, "StreamResult") {
		t.Errorf("refusal = %q; it must name the RPC that works, or the caller is left guessing", msg)
	}
}

// The same result, paged. Every record arrives, the summary is on the first
// page, and no page is anywhere near the 4MB limit.
func TestStreamResultPagesALargeResult(t *testing.T) {
	want := graph(20000)
	cli := dial(t, harness{run: staticResult(want)})
	src := upload(t, cli, "corpus.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	stream, err := cli.StreamResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("StreamResult: %v", err)
	}

	var entities, relations, pages int
	var last bool
	for {
		page, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv after %d pages: %v", pages, err)
		}
		if int32(pages) != page.GetPage() {
			t.Errorf("page number = %d, want %d", page.GetPage(), pages)
		}
		if pages == 0 {
			if page.GetCounts().GetEntities() != int32(len(want.Entities)) {
				t.Error("the first page has no counts; §5's numbers must arrive before the graph, not after it")
			}
		} else if page.GetCounts() != nil {
			t.Errorf("page %d repeats the summary", pages)
		}
		if n := proto.Size(page); n > 4<<20 {
			t.Errorf("page %d is %d bytes, over gRPC's own limit; paging that still blows the limit is not paging", pages, n)
		}
		entities += len(page.GetEntities())
		relations += len(page.GetRelations())
		last = page.GetLast()
		pages++
	}

	if pages < 2 {
		t.Errorf("pages = %d; a result GetResult refused arrived in one page here", pages)
	}
	if !last {
		t.Error("the last page is not marked last; a client cannot tell a finished stream from a broken one")
	}
	if entities != len(want.Entities) || relations != len(want.Relations) {
		t.Errorf("got %d entities and %d relations, want %d and %d", entities, relations, len(want.Entities), len(want.Relations))
	}
}

// A small result still streams, in one page, so a client never has to choose
// between the two RPCs by guessing how big its own graph is.
func TestStreamResultSendsOnePageForASmallResult(t *testing.T) {
	cli := dial(t, harness{run: staticResult(graph(3))})
	src := upload(t, cli, "corpus.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	stream, err := cli.StreamResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("StreamResult: %v", err)
	}
	page, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if !page.GetLast() || len(page.GetEntities()) != 3 {
		t.Errorf("page = %d entities, last = %v; want all 3 in one final page", len(page.GetEntities()), page.GetLast())
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Errorf("second Recv = %v, want EOF", err)
	}
}

// The findings come first: a client that stops reading after one page should
// have the conflicts, violations and guesses in hand rather than the first
// thousand entities.
func TestStreamResultPutsTheFindingsFirst(t *testing.T) {
	res := graph(10)
	res.Violations = []alchemy.Violation{{
		Kind: alchemy.ViolationUnknownEntityType, Subject: "e1",
		Detail: `"Widget" is not a type the ontology declares`,
	}}
	cli := dial(t, harness{pageSize: 2, run: staticResult(res)})
	src := upload(t, cli, "corpus.md", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_SUCCEEDED)

	stream, err := cli.StreamResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if err != nil {
		t.Fatalf("StreamResult: %v", err)
	}
	page, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if len(page.GetViolations()) != 1 {
		t.Error("the violation is not on the first page; a reader who stops early stops without the warnings")
	}
}

// A held job's graph is not a finished graph, whichever RPC asks for it.
func TestStreamResultRefusesAHeldJob(t *testing.T) {
	cli := dial(t, harness{run: staticResult(disputed())})
	src := upload(t, cli, "deal.pdf", alchemyv1.SourceKind_SOURCE_KIND_DOCUMENT, []byte("text"))
	j := create(t, cli, &alchemyv1.CreateJobRequest{SourceIds: []string{src}, Ontology: "crm"})
	awaitState(t, cli, j.GetId(), alchemyv1.JobState_JOB_STATE_NEEDS_REVIEW)

	stream, err := cli.StreamResult(authed(context.Background()), &alchemyv1.GetResultRequest{JobId: j.GetId()})
	if err == nil {
		_, err = stream.Recv()
	}
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition (err %v)", got, err)
	}
}
