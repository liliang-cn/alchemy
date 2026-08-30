package pipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/review"
)

// The defect, reproduced through the whole pipeline rather than asserted about
// the verifier alone: two sections of one document, one model call each,
// neither able to see how the other named the cluster. §5b's system prompt
// asks for one spelling and cannot enforce it, which is why the answer is a
// finding rather than a better prompt.
const (
	sectionOne = "# Deployment\n\nSuperAI runs on node-a.\n"
	sectionTwo = "# Storage\n\nThe SuperAI cluster stores its data on node-a.\n"
)

func twoSpellingsLLM() *scriptLLM {
	return &scriptLLM{replies: map[string]string{
		"runs on node-a": `{"entities":[{"type":"Cluster","name":"SuperAI"},{"type":"Node","name":"node-a"}],
		  "relations":[{"type":"DEPLOYED_ON","from":"SuperAI","from_type":"Cluster","to":"node-a","to_type":"Node"}]}`,
		"stores its data": `{"entities":[{"type":"Cluster","name":"SuperAI cluster"},{"type":"Node","name":"node-a"}],
		  "relations":[{"type":"DEPLOYED_ON","from":"SuperAI cluster","from_type":"Cluster","to":"node-a","to_type":"Node"}]}`,
	}}
}

// §5's obligation, one number wider: every returned graph carries the numbers
// needed to distrust it. A graph of three nodes that may be a graph of two is
// exactly such a case, and nothing else in the result says so — both nodes are
// declared types with attributable provenance, and each looks fine alone.
func TestOneClusterUnderTwoNamesIsCountedAndReportedAndStillTwoNodes(t *testing.T) {
	req := Request{
		Sources:  []Source{doc("design.md", sectionOne+"\n"+sectionTwo)},
		Ontology: testOntology(t),
		Part:     ontology.PartProse,
		Models:   alchemy.Models{LLM: twoSpellingsLLM()},
	}

	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v; a duplicate is not a conflict and must not hold the job", err)
	}

	if len(res.Duplicates) != 1 {
		t.Fatalf("duplicates = %+v, want the one pair", res.Duplicates)
	}
	d := res.Duplicates[0]
	if d.Left.Name != "SuperAI" || d.Right.Name != "SuperAI cluster" {
		t.Fatalf("pair = %q and %q, want the two spellings", d.Left.Name, d.Right.Name)
	}
	if d.Left.Provenance.Chunk == d.Right.Provenance.Chunk {
		t.Fatalf("both sides cite chunk %d; the pair exists because two calls could not see each other", d.Left.Provenance.Chunk)
	}
	if res.Counts.Duplicates != len(res.Duplicates) {
		t.Fatalf("counts.Duplicates = %d, want %d", res.Counts.Duplicates, len(res.Duplicates))
	}
	// Reported, never resolved: the graph is what the model proposed.
	if res.Counts.Entities != 3 {
		t.Fatalf("entities = %d, want the three nodes the model proposed, unmerged", res.Counts.Entities)
	}
}

// §5c's `always` reaching the unattended run, which is the whole reason a
// duplicate is a queue item rather than a line in a report: an operator who
// knows their model writes the type word into the name says so once, and the
// nightly import merges it without asking and without guessing.
func TestAnAuthoredMergeRuleJoinsThePairWithoutAskingAnybody(t *testing.T) {
	req := Request{
		Sources:  []Source{doc("design.md", sectionOne+"\n"+sectionTwo)},
		Ontology: testOntology(t),
		Part:     ontology.PartProse,
		Models:   alchemy.Models{LLM: twoSpellingsLLM()},
		// Review mode is off: this is §7.3's unattended nightly import
		// carrying its policy as configuration, and the point is that it needs
		// nobody.
		Inbox: Answered(nil, []review.Rule{authoredMergeRule(t)}),
	}

	res, err := Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Counts.Entities != 2 {
		t.Fatalf("entities = %d, want the pair merged: %+v", res.Counts.Entities, res.Entities)
	}
	for _, r := range res.Relations {
		if r.From == "cluster:superai cluster" || r.To == "cluster:superai cluster" {
			t.Fatalf("relation %s -[%s]-> %s still points at the absorbed node", r.From, r.Type, r.To)
		}
	}
	// The graph is one node shorter and the result says why, in a finding that
	// names both chunks and the rule's author.
	if len(res.Duplicates) != 1 || res.Duplicates[0].Right.Provenance.ReviewedBy != "ops" {
		t.Fatalf("duplicates = %+v, want the finding kept and signed", res.Duplicates)
	}
	if res.Counts.Duplicates != 1 {
		t.Fatalf("counts.Duplicates = %d, want the question to still be counted after it was answered", res.Counts.Duplicates)
	}
	// §5c: the rule names the record it acted on, so a graph one node shorter
	// can say which policy took the node away and who wrote it.
	if res.Entities[0].Provenance.RuledBy == "" {
		t.Fatalf("survivor provenance = %+v, want the rule that merged into it", res.Entities[0].Provenance)
	}
}

// authoredMergeRule is the operator writing the policy §5c's opening argument
// is about: they already know what their model does to names, and making them
// wait for a hold per pair to say so is the ceremony review is supposed to
// avoid. The shape names both spellings in full, so it can only ever act on
// this pair.
func authoredMergeRule(t *testing.T) review.Rule {
	t.Helper()
	rule, err := review.Authorship{
		Shape:   "duplicate/name_affix/type=Cluster/left=superai/right=superai cluster/between=llm-extract|llm-extract/model=fake-llm",
		Verb:    review.VerbAlways,
		Edit:    &review.Edit{Into: "cluster:superai"},
		By:      "ops",
		Because: "this model writes the type word into the name; the short one is the cluster",
		At:      time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
	}.Rule()
	if err != nil {
		t.Fatalf("Authorship.Rule: %v", err)
	}
	return rule
}

// §7.1: a graph re-extracted is compared against the last one, so a report
// whose order moves between two runs of one input cannot be diffed. The
// extractor runs chunks concurrently by default, which is exactly the thing
// that could make the entity order — and therefore which side of a pair is
// found first — depend on a scheduler. The detector is keyed on names rather
// than on arrival, and this is what says so under a race detector.
func TestTheSameCorpusFindsTheSamePairEveryRun(t *testing.T) {
	var first string
	for i := 0; i < 20; i++ {
		req := Request{
			Sources:  []Source{doc("design.md", sectionOne+"\n"+sectionTwo)},
			Ontology: testOntology(t),
			Part:     ontology.PartProse,
			Models:   alchemy.Models{LLM: twoSpellingsLLM()},
		}
		res, err := Run(context.Background(), req, nil)
		if err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		got, err := json.Marshal(res.Duplicates)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = string(got)
			if len(res.Duplicates) == 0 {
				t.Fatal("the corpus produced no duplicate; the test proves nothing")
			}
			continue
		}
		if string(got) != first {
			t.Fatalf("run %d found\n%s\nwant\n%s", i, got, first)
		}
	}
}
