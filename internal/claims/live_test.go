package claims_test

import (
	"sort"
	"testing"

	"github.com/liliang-cn/alchemy/internal/claims"
)

// The expected value in each test below is what DESIGN.md says, written here
// beside the sentence it comes from. The actual value is computed from the
// repository on this run. A failure is therefore never ambiguous: either the
// code moved and the sentence needs correcting, or somebody changed the
// sentence without checking.

func root(t *testing.T) string {
	t.Helper()
	r, err := claims.Root()
	if err != nil {
		t.Fatalf("finding the module root: %v", err)
	}
	return r
}

// DESIGN.md §9: "three dependencies (a PDF reader, gRPC, protobuf)".
//
// This is the sentence this whole package exists because of. It was true when
// it was written and stopped being true when §8.3's Postgres stores landed,
// and the gap between those two events was long enough that the number was
// repeated as a fact several times over.
//
// The check is the list and not the count, because the count alone would go on
// passing if a dependency were swapped for another one — and §4's argument
// against holding storage is an argument about WHICH dependencies these are,
// not how many. A buyer reading "gRPC, protobuf and a PDF reader" and finding
// a Postgres driver has been told something false about what they are
// installing.
func TestTheDependenciesAreTheOnesDesignSaysTheyAre(t *testing.T) {
	// Verbatim from DESIGN.md §9. Correct both together or neither.
	documented := []string{
		"github.com/grpc-ecosystem/grpc-gateway/v2",
		"github.com/jackc/pgx/v5",
		"github.com/ledongthuc/pdf",
		"google.golang.org/genproto/googleapis/api",
		"google.golang.org/grpc",
		"google.golang.org/protobuf",
	}

	got, err := claims.DirectDependencies(root(t))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	sort.Strings(got)
	sort.Strings(documented)

	if len(got) != len(documented) {
		t.Fatalf("go.mod requires %d direct dependencies and DESIGN.md §9 names %d\n"+
			"  go.mod:    %v\n  DESIGN.md: %v\n"+
			"§4 argues the product must not force storage choices on a buyer, so this list is "+
			"part of the argument and not trivia. Correct §9.", len(got), len(documented), got, documented)
	}
	for i := range got {
		if got[i] != documented[i] {
			t.Fatalf("go.mod requires %s where DESIGN.md §9 names %s\n  go.mod:    %v\n  DESIGN.md: %v",
				got[i], documented[i], got, documented)
		}
	}
}

// DESIGN.md §9: "Twenty-one packages".
//
// Counted under pkg/, which is what the sentence is about: cmd/alchemy is the
// binary and proto/alchemy/v1 is generated, and neither is a package somebody
// wrote and maintains. internal/ is excluded for the same reason the build
// gate is not itself a product feature.
func TestThePackageCountIsTheOneDesignStates(t *testing.T) {
	const documented = 21

	pkgs, err := claims.PackagesUnder(root(t), "pkg")
	if err != nil {
		t.Fatalf("walking pkg/: %v", err)
	}
	if len(pkgs) != documented {
		sort.Strings(pkgs)
		t.Fatalf("pkg/ holds %d packages, DESIGN.md §9 says %d\n  %v", len(pkgs), documented, pkgs)
	}
}

// DESIGN.md §9: "600+ tests".
//
// A floor rather than a figure, so it fails in one direction only — which is
// the honest shape for a claim written with a plus sign on it. Tests are added
// constantly and a check that demanded the exact number would fail on every
// commit that added one, teaching everybody to edit it without reading it.
func TestThereAreAtLeastAsManyTestsAsDesignClaims(t *testing.T) {
	const documented = 600

	n, err := claims.TestFunctions(root(t), "pkg", "cmd", "internal")
	if err != nil {
		t.Fatalf("counting tests: %v", err)
	}
	if n < documented {
		t.Fatalf("the root module has %d test functions, DESIGN.md §9 claims %d+", n, documented)
	}
	t.Logf("root module test functions: %d (DESIGN.md §9 claims %d+)", n, documented)
}

// Not a DESIGN.md claim: the standing instruction this repository was written
// under, that no single file exceeds six hundred lines.
//
// It is here rather than in a linter's configuration because it is the same
// kind of fact as the ones above — a promise about the code that is true today
// and has nothing checking it — and because the number that matters when it
// breaks is which file, which a lint rule would also tell you but only if
// somebody had installed one.
func TestNoSourceFileExceedsSixHundredLines(t *testing.T) {
	const limit = 600

	files, err := claims.SourceFiles(root(t))
	if err != nil {
		t.Fatalf("walking for sources: %v", err)
	}
	var over []string
	longest, longestName := 0, ""
	for path, lines := range files {
		if lines > limit {
			over = append(over, path)
		}
		if lines > longest {
			longest, longestName = lines, path
		}
	}
	if len(over) > 0 {
		sort.Strings(over)
		t.Fatalf("%d file(s) over %d lines: %v", len(over), limit, over)
	}
	t.Logf("longest hand-written file: %s at %d lines (limit %d)", longestName, longest, limit)
}
