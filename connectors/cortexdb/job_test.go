package cortexdb

import (
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
