package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/preflight"
)

// The producer keeps the invariants its consumers check. pkg/preflight states
// what a result has to be true about itself; this is the pipeline saying it is
// — which is the only reason a store may rely on it, since a rule enforced
// nowhere is one the next reader has to re-derive from the code.
func TestAFinishedJobPassesTheChecksAWriterMakes(t *testing.T) {
	res, err := Run(context.Background(), mixedJob(t), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ds := preflight.Check(res); len(ds) != 0 {
		for _, d := range ds {
			t.Errorf("%s (%s): %s", d.Kind, d.Severity, d.Detail)
		}
		t.Fatal("the pipeline produced a result its own consumers would refuse")
	}
}

// §8.4 pages a result and a page carries no source beside a chunk index, so
// the index has to name one chunk across the whole job — and adopt() is the
// only thing making that true. A renumbering that broke would produce a graph
// that looks correct and whose vectors describe the wrong text.
func TestTwoSourcesChunksAreNumberedAcrossTheWholeJob(t *testing.T) {
	_, err := Run(context.Background(), regionRequest(t,
		doc("one.md", docEU), doc("two.md", docUS)), nil)
	// Two sources that disagree hold the job (§7.3), which is the point of the
	// fixture: the pending graph is where the chunks are.
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want the hold these two sources always produce", err)
	}
	chunks := held.Pending.Chunks
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want both sources", len(chunks))
	}
	seen := map[int]string{}
	for _, c := range chunks {
		if prev, dup := seen[c.Index]; dup {
			t.Fatalf("chunk %d comes from %q and from %q; a chunk index names one chunk", c.Index, prev, c.Source)
		}
		seen[c.Index] = c.Source
	}
	if len(seen) != len(chunks) {
		t.Fatalf("%d chunks under %d indexes", len(chunks), len(seen))
	}
}

// §5's numbers are the obligation that justifies the scope, and §8.4 puts them
// on the first page of a paged result — before the records. Without these two a
// paged reader could say how many entities it had seen of how many and could
// say nothing of the sort about chunks or vectors.
func TestTheCountsSayHowManyChunksAndVectorsTheJobProduced(t *testing.T) {
	res, err := Run(context.Background(), mixedJob(t), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Counts.Chunks != len(res.Chunks) || res.Counts.Chunks == 0 {
		t.Errorf("counts.chunks = %d, chunks = %d", res.Counts.Chunks, len(res.Chunks))
	}
	if res.Counts.Vectors != len(res.Vectors) || res.Counts.Vectors == 0 {
		t.Errorf("counts.vectors = %d, vectors = %d", res.Counts.Vectors, len(res.Vectors))
	}
}

// A result had no identity at all, and every store that wanted one had to
// demand it from its caller or derive it from the bytes. The job already had
// one; it simply never reached the graph it produced.
func TestTheResultCarriesTheJobThatProducedIt(t *testing.T) {
	req := mixedJob(t)
	req.Job = "job-42"
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Job != "job-42" {
		t.Fatalf("Job = %q, want the job that produced it", res.Job)
	}
}

// A held job's pending graph is the one a reviewer reads and the one a store
// will eventually be handed, and it carries the same identity: §7.3's hold is a
// state of a job, not a different job.
func TestAHeldResultCarriesTheJobToo(t *testing.T) {
	req := regionRequest(t, doc("eu.md", docEU), doc("us.md", docUS))
	req.Job = "job-held"
	_, err := Run(context.Background(), req, nil)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want a hold", err)
	}
	if held.Pending.Job != "job-held" {
		t.Fatalf("Pending.Job = %q, want the job that produced it", held.Pending.Job)
	}
}

// A chunk numbering this package cannot vouch for must stop the job rather than
// be handed to a store that will merge two chunks into one and report two.
//
// It is a post-condition on this package's own work and is unreachable through
// Run — adopt() is what makes it hold — which is exactly why it is checked
// rather than commented: the guard is what would notice if adopt ever stopped
// renumbering, and a test that could only reach it through a corpus would be a
// test of the corpus.
func TestTheChunkNumberingGuardRefusesTwoChunksUnderOneIndex(t *testing.T) {
	err := ownChunkNumbering(alchemy.Result{Chunks: []alchemy.Chunk{
		{Index: 0, Source: "one.md"},
		{Index: 0, Source: "two.md"},
	}})
	if err == nil {
		t.Fatal("the guard accepted two chunks under one index")
	}
	for _, want := range []string{"one.md", "two.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
}

// And it says nothing about a job that has no chunks, which is every schema
// import.
func TestTheChunkNumberingGuardIsSilentOnAJobWithNoChunks(t *testing.T) {
	if err := ownChunkNumbering(alchemy.Result{}); err != nil {
		t.Fatalf("err = %v, want nil: a schema import has no chunks", err)
	}
}
