package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
)

// The two halves of "the vocabulary was missing a word", tested apart.
//
// The machinery for it was built and complete — verify names the undeclared
// type, proposals turns it into something Extend can accept, and the cortexdb
// connector grades the record refused with the vocabulary's own reason. None
// of it had ever run on a document, because the extractor's prompt told the
// model that a chunk it cannot express is an empty chunk. The first test below
// is the downstream half working; the second is the prompt that kept it from
// ever being reached.

// TestAnUndeclaredRelationIsNamedAndProposed is the downstream half. It drives
// the model's answer directly, which is what the prompt change below makes a
// real model produce.
func TestAnUndeclaredRelationIsNamedAndProposed(t *testing.T) {
	llm := &scriptLLM{name: "gemini-3.6-flash-high", replies: map[string]string{
		"SuperAI": `{"entities":[
		   {"type":"Cluster","name":"SuperAI"},
		   {"type":"Node","name":"node-a"}],
		 "relations":[
		   {"type":"OPERATED_BY","from":"SuperAI","from_type":"Cluster","to":"node-a","to_type":"Node"}]}`,
	}}
	res, err := Run(context.Background(), Request{
		Sources:  []Source{{Name: "architecture.md", Kind: alchemy.SourceDocument, Open: openString("# Overview\n\nSuperAI is operated by node-a.\n")}},
		Ontology: testOntology(t),
		Part:     ontology.PartProse,
		Models:   alchemy.Models{LLM: llm},
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var found *alchemy.Violation
	for i, v := range res.Violations {
		if v.Kind == alchemy.ViolationUnknownRelationType {
			found = &res.Violations[i]
		}
	}
	if found == nil {
		t.Fatalf("no %s violation; the vocabulary declares no OPERATED_BY and the record used it. violations = %+v",
			alchemy.ViolationUnknownRelationType, res.Violations)
	}
	if !strings.Contains(found.Detail, "OPERATED_BY") {
		t.Errorf("the violation does not name the type: %q", found.Detail)
	}

	// And it reaches the one workflow that can fix a vocabulary, carrying the
	// ends — without them Extend refuses the proposal, because a relation
	// declared with an open end holds between anything.
	var p *alchemy.Proposal
	for i, q := range res.Proposals {
		if q.Type == "OPERATED_BY" {
			p = &res.Proposals[i]
		}
	}
	if p == nil {
		t.Fatalf("the undeclared type produced no proposal; proposals = %+v", res.Proposals)
	}
	if p.Kind != alchemy.ProposalRelation {
		t.Errorf("proposal kind = %q, want %q", p.Kind, alchemy.ProposalRelation)
	}
	if len(p.From) == 0 || len(p.To) == 0 {
		t.Errorf("proposal carries no ends (%v -> %v); Extend refuses one that cannot say what it holds between", p.From, p.To)
	}

	// The record itself is in the graph, which is what lets a store grade it
	// refused rather than lose it. A fact dropped for being unsayable is the
	// failure this whole path exists to prevent.
	var kept bool
	for _, r := range res.Relations {
		if r.Type == "OPERATED_BY" {
			kept = true
		}
	}
	if !kept {
		t.Error("the undeclared relation is not in the result, so nothing downstream can grade it refused or show it to a person")
	}
}
