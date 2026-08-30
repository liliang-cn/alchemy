package pgvector

import (
	"context"
	"sync"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Two loaders handed the same graph under two different names is the case the
// fingerprint check alone cannot cover: both check before either commits, so
// both pass. The unique index is what actually enforces it, and the loser
// resolves to the same answer a sequential second load would get.
//
// This is not a hypothetical. §8.3's coordination is at-least-once, so a
// retried job running beside the original is the normal way this happens.
func TestTwoLoadersRacingOnOneGraphProduceOneCopy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// The dimension is bound first so the race is about the graph rather than
	// about two processes racing on DDL, which the advisory lock already
	// serialises and which has its own test.
	warm := f.open(t, Config{Dimension: 8})
	_ = warm

	res := smallResult(8)
	var wg sync.WaitGroup
	out := make([]Loaded, 2)
	errs := make([]error, 2)
	ids := []string{"run-a", "run-b"}
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := f.open(t, Config{})
			out[i], errs[i] = l.Load(ctx, res, LoadOptions{ID: ids[i]})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("loader %d: %v", i, err)
		}
	}
	if out[0].Already == out[1].Already {
		t.Errorf("Already = %v/%v; exactly one of two racing loaders should have written the graph",
			out[0].Already, out[1].Already)
	}
	if out[0].ID != out[1].ID {
		t.Errorf("IDs = %q/%q; the loser has to resolve to the graph that is actually there", out[0].ID, out[1].ID)
	}
	if n := f.count(t, "loads"); n != 1 {
		t.Errorf("loads = %d, want 1", n)
	}
	if n := f.count(t, "entities"); n != 2 {
		t.Errorf("entities = %d, want 2: the graph was written twice", n)
	}
}

// Migration and dimension binding are DDL, and ten processes starting together
// must produce one column rather than nine crashes.
func TestConcurrentMigrationsAndBindingsAgree(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := f.openRaw(t, Config{})
			if err := l.Migrate(ctx); err != nil {
				errs[i] = err
				return
			}
			errs[i] = l.bindDimension(ctx, 8, "embed-4")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("starter %d: %v", i, err)
		}
	}
	l := f.openRaw(t, Config{})
	if got := l.BoundDimension(ctx); got != 8 {
		t.Errorf("bound dimension = %d, want 8", got)
	}
}

// Duplicates are §5's "numbers needed to distrust it" made joinable: a reader
// looking at a node has to be able to see that another node may be the same
// one, without being told it is.
func TestDuplicatesLandBesideTheNodesTheyAreAbout(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	res := smallResult(8)
	res.Duplicates = []alchemy.Duplicate{{
		Signal:  alchemy.DuplicateNameAffix,
		Subject: "SuperAI ~ SuperAI service",
		Detail:  "chunk 0 said SuperAI, chunk 1 said SuperAI service",
		Left:    alchemy.DuplicateSide{ID: "SuperAI", Type: "Service", Name: "SuperAI", Provenance: prov(0)},
		Right:   alchemy.DuplicateSide{ID: "CortexDB", Type: "Service", Name: "SuperAI service", Provenance: prov(1)},
	}}
	res.Counts.Duplicates = 1
	if _, err := l.Load(context.Background(), res, LoadOptions{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	var name, signal, model string
	f.scalar(t, &name, `SELECT e.name FROM {s}.loaded_entities e
		JOIN {s}.loaded_duplicates d ON d.load_id = e.load_id AND d.left_id = e.entity_id`)
	f.scalar(t, &signal, `SELECT signal FROM {s}.loaded_duplicates`)
	f.scalar(t, &model, `SELECT left_prov->>'model' FROM {s}.loaded_duplicates`)
	if name != "SuperAI" || signal != "name_affix" || model != "gemini-3.6-flash-high" {
		t.Errorf("duplicate join = %q/%q/%q, want SuperAI/name_affix/gemini-3.6-flash-high", name, signal, model)
	}
	var dups int
	f.scalar(t, &dups, `SELECT (counts->>'duplicates')::int FROM {s}.loads`)
	if dups != 1 {
		t.Errorf("counts.duplicates = %d, want 1", dups)
	}
}

// Postgres text cannot hold a NUL byte. Stripping one would be an unrecorded
// edit to the buyer's corpus, so the load is refused and leaves nothing.
func TestANULByteInChunkTextRefusesTheLoad(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	res := smallResult(8)
	res.Chunks[1].Text = "a scanned page\x00with a stray byte"
	if _, err := l.Load(context.Background(), res, LoadOptions{}); err == nil {
		t.Fatal("a NUL byte was loaded")
	}
	for _, table := range append([]string{"loads"}, childTables...) {
		if n := f.count(t, table); n != 0 {
			t.Errorf("%s = %d, want 0", table, n)
		}
	}
}

// §5's block travels with the graph. A store that kept the edges and dropped
// the counts would leave every reader downstream unable to distrust it by the
// right amount.
func TestTheNumbersNeededToDistrustTheGraphAreKept(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	res := smallResult(8)
	res.Counts = alchemy.Counts{Entities: 2, Relations: 1, Deterministic: 0, Inferred: 3,
		Violations: 4, Guesses: 2, ChunksEmpty: 7, ChunksUnread: 1, Dropped: 5}
	res.RuleSets = []alchemy.RuleSet{{Name: "rs-9f21", Rules: []alchemy.StandingRule{
		{Name: "authored/type:Service", Told: "treat Service and Svc as one type — ada"},
	}}}
	res.Unread = []alchemy.Unread{{Source: "scan.pdf", Locator: "p4", Reason: "no text layer and no OCR model"}}
	res.ModelCalls = []alchemy.ModelCall{{Model: "gemini-3.6-flash-high", Stage: "extract", Calls: 12, Tokens: 40100}}
	loaded, err := l.Load(context.Background(), res, LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var empty, dropped, tokens int
	var told, reason string
	f.scalar(t, &empty, `SELECT (counts->>'chunks_empty')::int FROM {s}.loads WHERE id = $1`, loaded.ID)
	f.scalar(t, &dropped, `SELECT (counts->>'dropped')::int FROM {s}.loads WHERE id = $1`, loaded.ID)
	f.scalar(t, &tokens, `SELECT (model_calls->0->>'tokens')::int FROM {s}.loads WHERE id = $1`, loaded.ID)
	f.scalar(t, &told, `SELECT rule_sets->0->'rules'->0->>'told' FROM {s}.loads WHERE id = $1`, loaded.ID)
	f.scalar(t, &reason, `SELECT unread->0->>'reason' FROM {s}.loads WHERE id = $1`, loaded.ID)
	if empty != 7 || dropped != 5 || tokens != 40100 {
		t.Errorf("counts = %d empty, %d dropped, %d tokens; want 7, 5, 40100", empty, dropped, tokens)
	}
	if told == "" {
		t.Error("the sentence the model was told was dropped; a record's rule_set then points at nothing")
	}
	if reason == "" {
		t.Error("unread source material was dropped; 'no text here' is now indistinguishable from 'an empty page'")
	}
}
