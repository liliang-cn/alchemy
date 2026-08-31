// Command kgagent answers one question about a graph alchemy loaded, using
// nothing but pkg/recall.
//
//	N4_URI=neo4j://host:7687 N4_USER=neo4j N4_PASS=… RUN_ID=<load> \
//	CPA_API_KEY=… CPA_BASE_URL=… CPA_MODEL=… \
//	kgagent "Who is the CTO?"
//
// Everything specific to a store is the three lines that open one. The agent
// below is handed a recall.Reader and never learns which store is behind it,
// which is the example: swapping Neo4j for pgvector or the RDF connector is one
// line here and nothing anywhere else.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/providers"

	"github.com/liliang-cn/alchemy/connectors/neo4j"
	"github.com/liliang-cn/alchemy/examples/kgagent"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kgagent <question>")
		os.Exit(2)
	}
	question := os.Args[1]
	ctx := context.Background()

	l, err := neo4j.Open(ctx, os.Getenv("N4_URI"), os.Getenv("N4_USER"), os.Getenv("N4_PASS"), neo4j.Options{})
	must(err)
	defer l.Close(ctx)

	// The one line. Everything below this is the same for any store.
	var reader recall.Reader = l

	p, err := providers.NewOpenAILLMProvider(&domain.OpenAIProviderConfig{
		APIKey:   os.Getenv("CPA_API_KEY"),
		BaseURL:  os.Getenv("CPA_BASE_URL"),
		LLMModel: os.Getenv("CPA_MODEL"),
	})
	must(err)

	svc, err := agent.New("kgagent").WithLLM(p).WithSystemPrompt(kgagent.Prompt).Build()
	must(err)
	defer svc.Close()

	g := &kgagent.Graph{Reader: reader, Load: os.Getenv("RUN_ID")}
	for _, t := range g.Tools() {
		svc.AddTool(t.Name, t.Description, t.Schema, t.Do)
	}

	fmt.Printf("Q: %s\n\n", question)
	res, err := svc.Run(ctx, question)
	must(err)
	fmt.Println("─── ANSWER ───")
	fmt.Println(res.Text())

	// The trace is printed every time, because it is the half of the run that
	// says whether the answer was reached or guessed at. Two runtimes gave
	// identical answers to five questions, and every defect the comparison found
	// was visible here and in nothing else.
	calls := g.Calls()
	fmt.Printf("\n─── TOOL CALLS (%d) ───\n", len(calls))
	for i, c := range calls {
		fmt.Printf("  %d. %s\n", i+1, c)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR:", err)
		os.Exit(1)
	}
}
