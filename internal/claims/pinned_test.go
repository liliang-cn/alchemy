package claims_test

import (
	"errors"
	"testing"

	"github.com/liliang-cn/alchemy/internal/claims"
)

// These numbers came out of a real corpus, real model endpoints and a real
// deployment, so the build cannot recompute them. They are read from the file
// the run that produced them wrote.
//
// A missing file skips, loudly, naming the command. It does not pass: a check
// that goes green because its evidence is absent is worse than no check, since
// it reports the same colour as one that was actually made.

const howNorthgate = "ALCHEMY_TOKEN=... go run ./cmd/measure <addr> <fixtures-dir>"

func load(t *testing.T, name, how string) claims.Claim {
	t.Helper()
	c, err := claims.Load(root(t), name, how)
	var missing claims.ErrNoClaimFile
	if errors.As(err, &missing) {
		t.Skipf("not measured in this checkout: %v", missing)
	}
	if err != nil {
		t.Fatalf("%v", err)
	}
	return c
}

func value(t *testing.T, c claims.Claim, key string) float64 {
	t.Helper()
	v, err := c.Value(key)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return v
}

// DESIGN.md §7.3: "a conflict always holds the job", and §9's claim that the
// pipeline runs end to end against real endpoints.
//
// Zero is the number that has to hold. It is not zero because conflicts are
// rare — the same corpus produced eighty-nine of them before the parallel-edge
// rule and the mutual-import collapse existed, and every one was a real
// disagreement the verifier was right to raise. It is zero because those two
// rules were the answer, and a regression in either would put the number back
// without breaking anything else: the jobs would still succeed, the graph
// would still load, and six jobs would sit at NEEDS_REVIEW instead.
func TestTheRealCorpusImportsWithoutAConflict(t *testing.T) {
	c := load(t, "evaluation-suite.json", howNorthgate)
	if c.Provenance != claims.Measured {
		t.Fatalf("the evaluation suite is %q; a claim about what this product does on real input "+
			"cannot be inherited from elsewhere", c.Provenance)
	}
	if got := value(t, c, "total.conflicts"); got != 0 {
		t.Fatalf("the evaluation suite produced %v conflicts, want 0 (measured %s)", got, c.MeasuredAt)
	}
}

// §9: "The pipeline runs end to end against real model endpoints." Four source
// kinds across six jobs, every one of them finishing.
//
// The job count and the kind count are separate numbers on purpose. Six jobs
// over one source kind would satisfy neither §5's ontology requirement nor
// §7.1's chunking argument, and four kinds in four jobs would not exercise the
// one case the suite exists for — the same DDL run twice, once governed and
// once not, which is how "an ontology nobody claimed has no rules to break"
// gets tested on real input rather than on a fixture.
func TestEverySourceKindImportedAndEveryJobFinished(t *testing.T) {
	c := load(t, "evaluation-suite.json", howNorthgate)
	if got, want := value(t, c, "source_kinds"), 4.0; got != want {
		t.Fatalf("the suite covered %v source kinds, want %v", got, want)
	}
	run, ok := value(t, c, "jobs.run"), value(t, c, "jobs.succeeded")
	if run != ok {
		t.Fatalf("%v of %v jobs succeeded (measured %s)", ok, run, c.MeasuredAt)
	}
	t.Logf("%v jobs over %v source kinds, all succeeded, %v conflicts (measured %s)",
		run, value(t, c, "source_kinds"), value(t, c, "total.conflicts"), c.MeasuredAt)
}

// The governed DDL job reports violations and the ungoverned one does not, and
// that difference is §5's product guarantee rather than a defect in either.
//
// A schema is a schema; the ontology written for this evaluation does not
// declare every table Ravel has. Under it, the edges it does not cover come
// back as violations with the row that produced them — visible and locatable,
// which is exactly what §5 promises — while the same schema read with no
// ontology has nothing to be measured against and reports none. Pinning the
// pair is how "not silently dropped and not silently kept" stops being prose:
// if the governed run ever reported zero, the vocabulary would have stopped
// being checked and every other number here would look better for it.
func TestGoverningASchemaIsWhatMakesItsGapsVisible(t *testing.T) {
	c := load(t, "evaluation-suite.json", howNorthgate)
	governed := value(t, c, "ddl_governed.violations")
	ungoverned := value(t, c, "ddl_no_ontology.violations")
	if ungoverned != 0 {
		t.Fatalf("the ungoverned DDL job reported %v violations; with no ontology there is "+
			"nothing for a record to violate", ungoverned)
	}
	if governed <= 0 {
		t.Fatalf("the governed DDL job reported %v violations, want more than none: the same "+
			"schema under a vocabulary that does not cover all of it must say so", governed)
	}
	// The two jobs read one file, so the graph itself must be identical: the
	// ontology governs what is reported about the extraction, not what the
	// deterministic reader extracts.
	for _, f := range []string{"entities", "relations"} {
		if a, b := value(t, c, "ddl_governed."+f), value(t, c, "ddl_no_ontology."+f); a != b {
			t.Fatalf("the governed run found %v %s and the ungoverned one %v; a DDL reader is "+
				"deterministic and the ontology is a check on it, not an input to it", a, f, b)
		}
	}
	t.Logf("one schema, %v edges either way, %v violations under the ontology and none without it",
		value(t, c, "ddl_governed.relations"), governed)
}

// DESIGN.md §2.1: "the mechanism that took one real graph's compliance from
// 74% to 94%".
//
// This one is inherited and the check is that it stays inherited. The number
// was measured in oss-agent, on another corpus, before this repository existed;
// it is the argument for the constrained extractor and it is quoted three times
// in DESIGN.md. What must never happen is for it to be quietly re-provenanced
// as something alchemy measured — which is the shape every borrowed benchmark
// takes on its way to becoming a lie about the product.
func TestTheComplianceNumberIsQuotedAndNotClaimed(t *testing.T) {
	c := load(t, "oss-agent-compliance.json", "it is not measurable here; see the file")
	if c.Provenance != claims.Inherited {
		t.Fatalf("the 74%%-to-94%% figure is marked %q; it was measured in %q and this "+
			"repository has never reproduced it", c.Provenance, c.Source)
	}
	if c.Source == "" {
		t.Fatal("an inherited claim must name the system it was measured on")
	}
	before, after := value(t, c, "before"), value(t, c, "after")
	if before != 0.74 || after != 0.94 {
		t.Fatalf("DESIGN.md §2.1 says 74%% to 94%%; the file says %v to %v", before, after)
	}
}
