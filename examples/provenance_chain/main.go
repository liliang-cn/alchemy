// Command provenance_chain runs the claim end to end: files become a graph, a
// vocabulary checks it, a disagreement stops the job, a person answers it, and
// the answer survives into a store that can still be asked how it knows.
//
// It exists because the claim was true and unrunnable. alchemy's README says
// every record names its source, its chunk and its producer; CortexDB's says it
// holds a knowledge contract you can query. Both are accurate and neither shows
// the join, which is where the interesting part lives: a fact a person decided
// arrives in the store graded differently from one nobody looked at, and you
// can ask which is which afterwards.
//
// Run it:
//
//	export ALCHEMY_LLM_BASE_URL=https://your-gateway/v1
//	export ALCHEMY_LLM_MODEL=some-model
//	export ALCHEMY_LLM_API_KEY=...        # optional; a local Ollama needs none
//	go run ./examples/provenance_chain
//
// It writes a throwaway database in a temp directory and prints what it found.
// Nothing here is scripted output: every line below is read back out of the
// store after the load.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/model"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/pipeline"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/sink"

	alchemycdb "github.com/liliang-cn/alchemy/connectors/cortexdb"
	cortexdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	"github.com/liliang-cn/cortexdb/v2/pkg/graph"
)

const runID = "provenance-chain-demo"

func main() {
	ctx := context.Background()
	log.SetFlags(0)

	llm := mustLLM()

	onto, err := ontology.Load(strings.NewReader(sdsOntology))
	if err != nil {
		log.Fatalf("ontology: %v", err)
	}

	sources := []pipeline.Source{
		doc("sds-meta-ha-runbook.md", runbook),
		doc("incident-2026-08-17.md", incidentNote),
	}

	// ── 1. Extraction, under the vocabulary ──────────────────────────────────
	step(1, "Read both documents under sds-demo@1")
	first, err := run(ctx, pipeline.Request{
		Job:      runID,
		Sources:  sources,
		Ontology: onto,
		Part:     ontology.PartProse,
		Models:   alchemy.Models{LLM: llm},
	})

	// ── 2. The hold ──────────────────────────────────────────────────────────
	// A conflict is a question, and a job that found one does not return a
	// finished graph whether or not anybody asked for review (§7.3). It comes
	// back as a typed error carrying the graph, so a caller cannot reach a held
	// result without naming the hold — a job that needs a person cannot be
	// mistaken for one that finished.
	var hold *pipeline.HeldError
	switch {
	case errors.As(err, &hold):
	case err != nil:
		log.Fatalf("pipeline: %v", err)
	}

	pending := first
	if hold != nil {
		pending = hold.Pending
	}
	fmt.Printf("   %d entities, %d relations, %d violations\n",
		len(pending.Entities), len(pending.Relations), len(pending.Violations))
	for _, v := range pending.Violations {
		fmt.Printf("   the vocabulary declined one: %s — %s\n", v.Kind, v.Detail)
	}

	step(2, "Held: a DRBD resource has one Primary, and two sources name different ones")
	if hold == nil {
		fmt.Println("   Nothing was held. The rest of this demo is about what happens")
		fmt.Println("   to a conflict, so it has nothing to show — the extractor did")
		fmt.Println("   not produce both sides of the disagreement this time.")
	}
	var queue []review.Item
	if hold != nil {
		queue = hold.Queue
		for _, c := range hold.Conflicts {
			fmt.Printf("   %s on %s\n", c.Kind, c.Subject)
			fmt.Printf("     %s\n", c.Detail)
			describeClaim("     incumbent", c.Left)
			describeClaim("     newcomer ", c.Right)
		}
	}

	// ── 3. A person answers it ───────────────────────────────────────────────
	// The decision is signed. A decision nobody signed cannot be written into
	// provenance, and "reviewed by" with nobody in it is worse than not
	// claiming review at all.
	step(3, "A person decides, and the decision is signed")
	decisions := acceptTheFailover(queue)
	for _, d := range decisions {
		fmt.Printf("   %s %s — by %s: %s\n", d.Verb, d.ItemID, d.By, d.Note)
	}

	second, err := run(ctx, pipeline.Request{
		Job:       runID,
		Sources:   sources,
		Ontology:  onto,
		Part:      ontology.PartProse,
		Models:    alchemy.Models{LLM: llm},
		Reviewing: true,
		Inbox:     pipeline.Answered(decisions, nil),
	})
	if err != nil {
		log.Fatalf("pipeline (reviewed): %v", err)
	}
	fmt.Printf("   after review: %d relations, %d still held\n",
		len(second.Relations), len(second.Held()))

	// ── 4. Into the store ────────────────────────────────────────────────────
	dir, err := os.MkdirTemp("", "provenance-chain")
	if err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "brain.db")

	step(4, "Load into CortexDB — the only sink that carries the contract")
	loader, err := alchemycdb.Open(dbPath, alchemycdb.Options{RunID: runID})
	if err != nil {
		log.Fatalf("open cortexdb: %v", err)
	}
	report, err := sink.Load(ctx, loader, second, sink.Options{Load: runID, Replace: true})
	if err != nil {
		_ = loader.Close()
		log.Fatalf("load: %v", err)
	}
	_ = loader.Close()
	fmt.Printf("   %+v\n", report)

	// ── 5. Ask the store how it knows ────────────────────────────────────────
	db, err := cortexdb.Open(cortexdb.DefaultConfig(dbPath))
	if err != nil {
		log.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	step(5, "What does the shelf stand on?")
	tally, err := db.ContractTally(ctx)
	if err != nil {
		log.Fatalf("tally: %v", err)
	}
	printTally(tally)

	step(6, "Which records would a person want to look at?")
	att, err := db.NeedsAttention(ctx, 10)
	if err != nil {
		log.Fatalf("needs attention: %v", err)
	}
	if len(att) == 0 {
		fmt.Println("   nothing is held or refused in this load")
	}
	for _, r := range att {
		kind := "node"
		if r.Edge {
			kind = "edge"
		}
		fmt.Printf("   [%s] %s %s %s\n", r.Grade, kind, r.Type, strings.TrimSpace(r.Content))
		if r.Why != "" {
			fmt.Printf("        why: %s\n", r.Why)
		}
		if r.Source != "" {
			fmt.Printf("        from %s (%s)\n", r.Source, r.Producer)
		}
	}

	step(7, "And the one a person signed")
	// The reviewer's name is not on GradedRecord: _by is who ASSERTED a record
	// by hand, and nobody did — a model extracted this edge. The signature from
	// step 3 lives in _reviewed_by, which is what made it `verified` above, so
	// it is read back as a property rather than assumed from the grade.
	signed, err := db.Graph().RecordsWithProperties(ctx, graph.PropertyRecordQuery{
		Where: map[string][]string{"_grade": {"verified"}},
		Fetch: []string{"_grade", "_reviewed_by", "_source", "_producer", "_contradicts"},
		Limit: 10,
	})
	if err != nil {
		log.Fatalf("verified records: %v", err)
	}
	if len(signed) == 0 {
		fmt.Println("   none — no decision reached the store")
	}
	for _, r := range signed {
		fmt.Printf("   %s —%s→ %s\n", shortID(r.From), r.Type, shortID(r.To))
		fmt.Printf("     reviewed by %q, extracted by %s from %s\n",
			r.Properties["_reviewed_by"], r.Properties["_producer"], r.Properties["_source"])
		if c := r.Properties["_contradicts"]; c != "" {
			fmt.Printf("     contradicts %s — the store keeps both, and says so\n", c)
		}
	}

	fmt.Println()
	fmt.Println("Nothing above was written by this program as text. The grades come")
	fmt.Println("from the contract the connector wrote while loading: a record a named")
	fmt.Println("person kept is `verified`, one the vocabulary declined is `refused`,")
	fmt.Println("and one a model produced that nobody checked is `asserted` — the")
	fmt.Println("weakest claim, which is the safe direction to be wrong in.")
}

// run drains the event channel rather than passing nil, because a nil channel
// blocks the sender forever. Run closes the channel itself when it returns, so
// this must not: the drain goroutine ends on its own.
func run(ctx context.Context, req pipeline.Request) (alchemy.Result, error) {
	events := make(chan pipeline.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range events {
		}
	}()
	res, err := pipeline.Run(ctx, req, events)
	<-done
	return res, err
}

func doc(name, body string) pipeline.Source {
	return pipeline.Source{
		Name: name,
		Kind: alchemy.SourceDocument,
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(body)), nil
		},
	}
}

// acceptTheFailover answers each queued question, once.
//
// A conflict is one queue item, not two, and the question it asks is narrower
// than "which of these is true": pkg/review records the incumbent on the left
// and the dissenting claim on the right, so a decision here is a decision about
// the NEWCOMER. Accept means the new claim stands.
//
// Which is exactly the judgement a person is needed for. Nothing in either
// document says which was written later, and nothing in the graph knows that
// drbd-reactor moved sds-meta at 02:14 — an operator does. A pipeline that
// guessed would be doing the reviewer's job with less information than the
// reviewer has.
//
// The IDs come from the queue the hold handed back rather than being rebuilt
// here, because the queue is literally what a person would be shown.
func acceptTheFailover(queue []review.Item) []review.Decision {
	out := make([]review.Decision, 0, len(queue))
	for _, it := range queue {
		out = append(out, review.Decision{
			ItemID: it.ID,
			Verb:   review.VerbAccept,
			By:     "demo-operator",
			Note:   "drbd-reactor demoted hp at 02:14 and dell took it; the runbook predates the failover",
			At:     time.Now(),
		})
	}
	return out
}

func describeClaim(label string, c alchemy.Claim) {
	p := c.Provenance
	fmt.Printf("%s  %s\n", label, c.Statement)
	fmt.Printf("%s    from %s, chunk %d, produced by %s\n", label, p.Source, p.Chunk, p.Producer)
}

func printTally(t cortexdb.ContractTally) {
	rows := []struct {
		name  string
		count int
	}{
		{"verified       (a named person kept it)", t.Verified.Nodes + t.Verified.Edges},
		{"self_consistent(derived from a statement)", t.SelfConsistent.Nodes + t.SelfConsistent.Edges},
		{"asserted       (a model said so, unchecked)", t.Asserted.Nodes + t.Asserted.Edges},
		{"held           (waiting on a person)", t.Held.Nodes + t.Held.Edges},
		{"refused        (the vocabulary declined it)", t.Refused.Nodes + t.Refused.Edges},
		{"untagged       (no contract at all)", t.Untagged.Nodes + t.Untagged.Edges},
	}
	for _, r := range rows {
		fmt.Printf("   %-44s %d\n", r.name, r.count)
	}
}

func mustLLM() alchemy.LLM {
	base := os.Getenv("ALCHEMY_LLM_BASE_URL")
	name := os.Getenv("ALCHEMY_LLM_MODEL")
	if base == "" || name == "" {
		log.Fatalf("this demo extracts prose, so it needs a model:\n" +
			"  export ALCHEMY_LLM_BASE_URL=https://your-gateway/v1\n" +
			"  export ALCHEMY_LLM_MODEL=some-model\n" +
			"  export ALCHEMY_LLM_API_KEY=...   # optional\n" +
			"Any OpenAI-compatible endpoint answers: a gateway, vLLM, Ollama, LiteLLM.")
	}
	llm, err := model.NewLLM(model.Endpoint{
		Name:    name,
		BaseURL: base,
		APIKey:  os.Getenv("ALCHEMY_LLM_API_KEY"),
	})
	if err != nil {
		log.Fatalf("model: %v", err)
	}
	return llm
}

// shortID drops the connector's run prefix so an edge reads as a sentence.
func shortID(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 && strings.Contains(id[:i], ":") {
		parts := strings.Split(id, ":")
		if len(parts) >= 2 {
			return parts[len(parts)-2] + ":" + parts[len(parts)-1]
		}
	}
	return id
}

func step(n int, title string) {
	fmt.Printf("\n%d. %s\n", n, title)
}
