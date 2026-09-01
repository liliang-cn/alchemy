// Package mcp serves a graph alchemy loaded as MCP tools, so that any client
// speaking Model Context Protocol can ask it the eight questions pkg/recall
// answers.
//
// It is thin on purpose. Everything about what the tools are called, what they
// say they do and how their answers read lives in pkg/agenttool, which knows
// nothing about MCP; this package turns that set into MCP's shape and nothing
// else. The alternative — writing the descriptions again here — is how the same
// tool comes to say two different things to two clients, and the descriptions
// are the part that has repeatedly been the defect.
//
// It is a module of its own for the reason connectors and examples/kgagent are:
// the core module's dependency list is stated in DESIGN.md §9 and checked by
// internal/claims on every run, and an MCP SDK does not belong in it. A buyer
// who wants a graph does not have to compile a protocol server for agents.
package mcp

import (
	"context"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liliang-cn/alchemy/pkg/agenttool"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Version is what the server reports to a client.
const Version = "0.1.0"

// Serve builds a server offering one MCP tool per question the reader answers.
//
// The load is fixed at construction rather than taken as a tool argument, and
// that is the one design decision this file makes on its own. Every method in
// pkg/recall takes the load as a parameter because "a corpus imported twice is
// in the store twice, and a citation resolved against the wrong import returns
// the wrong text under a claim from the right one". A model choosing that
// parameter would be choosing which import to answer from, per call, with
// nothing in the question to tell it — so the operator chooses once, on the
// command line, and every tool is scoped to it.
func Serve(r recall.Reader, load string) (*sdk.Server, *agenttool.Graph) {
	g := &agenttool.Graph{Reader: r, Load: load}
	s := sdk.NewServer(&sdk.Implementation{
		Name:    "alchemy",
		Title:   "alchemy — a knowledge graph that can name its sources",
		Version: Version,
	}, nil)

	for _, t := range g.Tools() {
		s.AddTool(&sdk.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Schema,
			// Declared, not inferred. Every tool here reads one finished load
			// and a load is immutable once complete, so a client is free to
			// collapse a repeated call — and a client that has to guess whether
			// a tool is safe to skip guesses that it is not. agent-go infers the
			// same property from names containing read, list or get; none of
			// these eight matches, and the cost of not saying so was measured at
			// twenty-five calls for thirteen distinct questions.
			Annotations: &sdk.ToolAnnotations{
				ReadOnlyHint:   true,
				IdempotentHint: true,
				OpenWorldHint:  boolPtr(false),
			},
		}, handler(t))
	}
	return s, g
}

// handler adapts one agenttool.Tool to MCP's calling convention.
//
// A tool that fails returns its error as CONTENT with IsError set, not as a
// transport error. The difference matters to the model rather than to the
// client: a protocol-level failure is something the client reports and the
// model never sees, and every failure these tools have is one the model needs
// to read — a citation that does not resolve, a load that is not there, an id
// the graph does not hold. Those are answers, and hiding them would leave the
// model to guess why its question vanished.
func handler(t agenttool.Tool) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		args := map[string]any{}
		if raw := req.Params.Arguments; len(raw) > 0 {
			if err := unmarshalArgs(raw, &args); err != nil {
				return text(fmt.Sprintf("could not read the arguments: %v", err), true), nil
			}
		}
		out, err := t.Do(ctx, args)
		if err != nil {
			return text(err.Error(), true), nil
		}
		s, ok := out.(string)
		if !ok {
			s = fmt.Sprint(out)
		}
		return text(s, false), nil
	}
}

func text(s string, isErr bool) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: s}},
		IsError: isErr,
	}
}

func boolPtr(b bool) *bool { return &b }
