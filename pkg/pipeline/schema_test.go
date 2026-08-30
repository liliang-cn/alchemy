package pipeline

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// A whole schema runs to the end.
//
// This is the failure as a buyer met it. A customer's own schema, uploaded as
// a DDL source, came back held at NEEDS_REVIEW on conflicts that were all
// false — five of its tables reference one table twice, which is how you model
// a relationship between two rows of one table, and the verifier read each
// pair as two sources disagreeing about one edge. §7.3 will not let a caller
// opt out of a person, so the job could never finish, and nothing a person
// could decide would have made it right.
//
// The fixture reproduces that schema's shape and none of its content; the
// customer's file is not in this repository. It lives in pkg/verify/testdata,
// where the unit-level acceptance test for the same defect reads it, and is
// read from there rather than copied: one copy is the honest number.
func TestTheSchemaRunsToTheEnd(t *testing.T) {
	text, err := os.ReadFile("../verify/testdata/freight-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		Sources: []Source{{Name: "freight-schema.sql", Kind: alchemy.SourceDDL, Open: openString(string(text))}},
		Models:  alchemy.Models{LLM: &failLLM{t: t}, Embedder: &failEmbedder{t: t}},
	}
	res, err := Run(context.Background(), req, nil)
	if err != nil {
		var held *HeldError
		if errors.As(err, &held) {
			t.Fatalf("the job is held on %d conflicts, the first being %q", len(held.Pending.Conflicts), held.Pending.Conflicts[0].Subject)
		}
		t.Fatalf("Run: %v", err)
	}
	if res.Counts.Conflicts != 0 {
		t.Fatalf("conflicts = %d, want none: %+v", res.Counts.Conflicts, res.Conflicts)
	}
	// 48 foreign keys in the file, and every one of them an edge — including
	// both ends of each connection table.
	if res.Counts.Relations != 48 || res.Counts.Entities != 45 {
		t.Fatalf("counts = %+v, want 45 tables and 48 foreign keys", res.Counts)
	}
	if res.Counts.Deterministic != 48 {
		t.Fatalf("deterministic = %d, want every edge: a schema states them all", res.Counts.Deterministic)
	}
}
