package cortexdb

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Options.RunID is required here for the same reason it is in the Neo4j
// connector: a node id is (run, entity id), and Entity.ID says nothing across
// runs, so a generated default would make a retry after a crash
// indistinguishable from a second import.
//
// The caller had already answered it. alchemy.Result.Job is the service's job
// ID — stated rather than generated, the same after a crash, the same when
// another node takes the job over (§8.3) — and asking for it a second time as
// an option was asking for something the result was carrying.
func TestTheRunDefaultsToTheJobThatProducedTheResult(t *testing.T) {
	res := alchemy.Result{Job: "job-42", Entities: []alchemy.Entity{
		{ID: "e1", Type: "System", Name: "SuperAI"},
	}}
	p, err := preflight(res, Options{})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if p.opts.RunID != "job-42" {
		t.Fatalf("RunID = %q, want the job that produced the result", p.opts.RunID)
	}
}

// An explicit RunID still wins: loading one job's graph twice under two names
// is something only the caller knows they are doing.
func TestAnExplicitRunStillWins(t *testing.T) {
	res := alchemy.Result{Job: "job-42", Entities: []alchemy.Entity{
		{ID: "e1", Type: "System", Name: "SuperAI"},
	}}
	p, err := preflight(res, Options{RunID: "rehearsal"})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if p.opts.RunID != "rehearsal" {
		t.Fatalf("RunID = %q, want the caller's", p.opts.RunID)
	}
}

// And a result that names no job, loaded by a caller that names no run, is
// still refused.
func TestAResultWithNoJobAndNoRunIsStillRefused(t *testing.T) {
	_, err := preflight(alchemy.Result{Entities: []alchemy.Entity{
		{ID: "e1", Type: "System", Name: "x"},
	}}, Options{})
	if !errors.Is(err, ErrNoRunID) {
		t.Fatalf("err = %v, want ErrNoRunID", err)
	}
}

// A retirement reaches the store, is attributable there, and removes nothing.
//
// The last clause is the one that needs a store to check. This connector is one
// DeleteDocumentGraph call away from performing the retirement, in a database
// that is also somebody's brain and that other agents read — so a connector
// that acted on a supersession would let one producer delete another producer's
// fact by naming it, across every reader of that brain. alchemy states a
// retirement and does not perform one, and these assertions are that this store
// keeps to it: the node named in Retires is still there, the graph is the size
// it was, and the claim sits beside it with the name of whoever made it.
func TestARetirementIsRecordedAndNothingIsRemoved(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-S"})
	rep, err := l.Load(context.Background(), fixture())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.Supersessions != 2 {
		t.Fatalf("Report.Supersessions = %d, want both retirements", rep.Supersessions)
	}
	if rep.Entities != 3 {
		t.Fatalf("%d entities after two retirements, want all 3: a retirement is stated, never performed", rep.Entities)
	}

	// The retired node is still a node, with the type and the run it was
	// written under.
	if n := countRows(t, l, "SELECT COUNT(*) FROM graph_nodes WHERE id = ?", entityNodeID("run-S", "e2")); n != 1 {
		t.Fatalf("the retired entity is in the graph %d times, want 1: nothing here deletes what a result says is over", n)
	}

	// And the claim is in the store rather than only in the return value: the
	// report is gone the moment the process is, and the whole worth of a
	// supersession six months later is that a reader can see somebody said the
	// old answer was over, and name them.
	var body string
	if err := l.db().SQL().QueryRowContext(context.Background(),
		"SELECT content FROM documents WHERE id = ?", completionID("run-S")).Scan(&body); err != nil {
		t.Fatalf("read completion: %v", err)
	}
	var marker runMarker
	if err := json.Unmarshal([]byte(body), &marker); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	if len(marker.Supersessions) != 2 {
		t.Fatalf("%d retirements on the completion, want 2: %s", len(marker.Supersessions), body)
	}
	if marker.Supersessions[1].Retires != "e-from-last-month" {
		t.Errorf("retires = %q, want the record no run here holds: that is the case the field exists for",
			marker.Supersessions[1].Retires)
	}
	if marker.Supersessions[0].Provenance.By != "ana@example.com" {
		t.Errorf("the claim names %q, want the person whose word it is on: a retirement nobody can attribute "+
			"is a deletion with a nicer name", marker.Supersessions[0].Provenance.By)
	}

	// It is beside the findings and not among them. A reader consulting the
	// findings is deciding how far to trust this import, and a correction says
	// nothing about that.
	var findings map[string]any
	if err := json.Unmarshal(marker.Findings, &findings); err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	if _, in := findings["supersessions"]; in {
		t.Error("the retirements are inside the findings blob, where a reader weighing the graph would read " +
			"a correction as a defect")
	}
}
