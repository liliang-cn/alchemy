package neo4j

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/sink"
)

// alchemy.Relation.Key is what makes two parallel edges two edges: a table
// that references another twice states two foreign keys that agree about their
// ends, their type and their source and differ only in which of them they are.
// This connector's key covered the attributes and the provenance and not the
// one field the producer supplied for exactly this — so Ravel's
// NODE_CONNECTIONS, the case the field was added for, arrived as one edge.
func TestTwoParallelEdgesAreTwoAssertions(t *testing.T) {
	src := rel("table:node_connections", "table:nodes", "REFERENCES")
	dst := src
	src.Key, dst.Key = "FK_NC_NODES_SRC", "FK_NC_NODES_DST"
	if relationKey(src) == relationKey(dst) {
		t.Fatal("two foreign keys between one pair of tables share a key, so one of them is never written")
	}
}

// And the same result stays the same result: adding a field to the key must
// not make a replay look like a new import for every graph that has no keys,
// which is every model-extracted one.
func TestAnUnkeyedEdgeIsStillTheSameAssertionTwice(t *testing.T) {
	if relationKey(rel("e1", "e2", "USES")) != relationKey(rel("e1", "e2", "USES")) {
		t.Fatal("the same unkeyed assertion twice has two keys, so a replay would double the edge")
	}
}

// A digest blind to Relation.Key would let a schema whose only correction was
// a constraint name replay as unchanged, leaving the graph with the edge it
// had rather than the two edges it now has.
func TestTheDigestSeesTheProducersKey(t *testing.T) {
	a := alchemy.Result{Relations: []alchemy.Relation{rel("e1", "e2", "REFERENCES")}}
	b := alchemy.Result{Relations: []alchemy.Relation{rel("e1", "e2", "REFERENCES")}}
	b.Relations[0].Key = "fk_left"
	if sink.Digest(a) == sink.Digest(b) {
		t.Fatal("the digest is blind to the producer's key")
	}
}

// §5c: Provenance.RuleSet "is the set's name … and not the set itself; the
// contents are on the result once, in Result.RuleSets". This connector wrote
// the names onto every record and the sets nowhere, so a buyer reading
// `n._rule_set` in the graph it produced got a pointer into nothing.
func TestTheRuleSetsARecordPointsAtAreInTheDigest(t *testing.T) {
	a := alchemy.Result{Entities: []alchemy.Entity{ent("e1", "System", "SuperAI")}}
	b := a
	b.RuleSets = []alchemy.RuleSet{{Name: "rs-9f21", Rules: []alchemy.StandingRule{
		{Name: "authored/violation/type=Flag", Told: "a switch is not an entity, said ana"},
	}}}
	if sink.Digest(a) == sink.Digest(b) {
		t.Fatal("a result whose policy changed replays as unchanged, so the sets are never written")
	}
}

// The one fact only the caller has is which import this is, and the caller
// already said it: alchemy.Result.Job is the service's job ID, stable across a
// retry and across §8.3's takeover by another node. Demanding it again as an
// option was demanding an answer the result was carrying.
func TestTheRunDefaultsToTheJobThatProducedTheResult(t *testing.T) {
	res := alchemy.Result{Job: "job-42", Entities: []alchemy.Entity{ent("e1", "System", "SuperAI")}}
	p, err := preflight(res, Options{})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if p.opts.RunID != "job-42" {
		t.Fatalf("RunID = %q, want the job that produced the result", p.opts.RunID)
	}
}

// An explicit RunID still wins. A caller loading one job's graph under two
// names — a rehearsal and a real import — is doing something the result cannot
// know about.
func TestAnExplicitRunStillWins(t *testing.T) {
	res := alchemy.Result{Job: "job-42", Entities: []alchemy.Entity{ent("e1", "System", "SuperAI")}}
	p, err := preflight(res, Options{RunID: "rehearsal"})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if p.opts.RunID != "rehearsal" {
		t.Fatalf("RunID = %q, want the caller's", p.opts.RunID)
	}
}

// And a result that names no job and a caller that names no run is still
// refused, for the reason Options.RunID gives: a generated default would make a
// retry after a crash indistinguishable from a second import.
func TestAResultWithNoJobAndNoRunIsStillRefused(t *testing.T) {
	_, err := preflight(alchemy.Result{Entities: []alchemy.Entity{ent("e1", "System", "x")}}, Options{})
	if !errors.Is(err, ErrNoRunID) {
		t.Fatalf("err = %v, want ErrNoRunID", err)
	}
}

// And the sets reach the graph, not only the digest. §5b's promise is that a
// record explains itself to whoever is holding it, and after a load the reader
// is holding Neo4j: a `_rule_set` property naming a policy that lives in a JSON
// file on the laptop of whoever ran the import explains nothing.
func TestARecordsRuleSetResolvesInsideTheGraph(t *testing.T) {
	l := liveLoader(t, Options{RunID: "run-rulesets"})
	res := alchemy.Result{
		Entities: []alchemy.Entity{{
			ID: "e1", Type: "System", Name: "SuperAI",
			Provenance: alchemy.Provenance{
				Source: "arch.md", Chunk: 0, Producer: alchemy.ProducerLLMExtract,
				RuleSet: "rs-9f21", RuledBy: "authored/violation/type=Flag",
			},
		}},
		RuleSets: []alchemy.RuleSet{{Name: "rs-9f21", Rules: []alchemy.StandingRule{
			{Name: "authored/violation/type=Flag", Told: "a switch is not an entity, said ana@example.com on 2026-08-30"},
		}}},
	}
	rep, err := l.Load(context.Background(), res)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.RuleSets != 1 {
		t.Errorf("RuleSets = %d, want the one the records point at", rep.RuleSets)
	}

	pre := l.opts.ReservedPrefix
	label := mustQuote(t, l.internalLabel("RuleSet"))
	base := mustQuote(t, l.opts.BaseLabel)
	recs := l.mustQuery(t, fmt.Sprintf(
		"MATCH (n:%s {`%s%s`: 'e1'}) MATCH (s:%s:%s {`%s%s`: $run, `%s%s`: n.`%srule_set`}) "+
			"RETURN s.`%srule_names` AS names, s.`%srule_told` AS told",
		base, pre, keyID, base, label, pre, keyRun, pre, keyID, pre, pre, pre), map[string]any{"run": "run-rulesets"})
	if len(recs) != 1 {
		t.Fatalf("the record's rule set resolves to %d nodes, want exactly one", len(recs))
	}
	names, _ := recs[0]["names"].([]any)
	told, _ := recs[0]["told"].([]any)
	if len(names) != 1 || names[0] != "authored/violation/type=Flag" {
		t.Errorf("rule names = %v, want the rule the record was ruled by", names)
	}
	if len(told) != 1 || !strings.Contains(told[0].(string), "ana@example.com") {
		t.Errorf("told = %v, want the sentence the model was shown and who said so", told)
	}
}
