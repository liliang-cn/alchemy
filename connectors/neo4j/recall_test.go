package neo4j

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// Every read in this package must be scoped to one load, and to a load that
// finished. It is the property the whole read path exists to keep and the one a
// query gets wrong silently: an unscoped read answers with another import's
// text, and a read that ignores the run marker answers with half of this one.
//
// It is asserted on the statements rather than against a server because a query
// that is only tested where a database is, is only tested on the machines that
// have one — and the four builders exist so that this test does not need one.
func TestEveryReadIsScopedToOneLoadAndToALoadThatFinished(t *testing.T) {
	l := New(nil, Options{})
	scope, err := l.scope()
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	for _, tc := range []struct {
		name  string
		build func() (string, error)
	}{
		{"find an anchor", l.findCypher},
		{"walk one hop", l.claimsCypher},
		{"resolve a citation", l.citeCypher},
		{"ask what is unanswered", l.unansweredCypher},
		{"see what contributed", l.contributionsCypher},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stmt, err := tc.build()
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if !strings.HasPrefix(stmt, scope) {
				t.Errorf("query does not begin with the run scope, so it can answer from an unfinished load:\n%s", stmt)
			}
			if !strings.Contains(stmt, "$run") {
				t.Errorf("query never binds $run, so it can answer from another import of the same file:\n%s", stmt)
			}
		})
	}
}

// The exclusion list a walk runs under has to be every edge this connector
// writes for its own bookkeeping. One missing is not a wrong answer with a
// wrong shape — it is a duplicate report or a retirement handed to an agent as
// a claim about the world, in the same struct as a real one.
func TestAWalkExcludesEveryEdgeThisConnectorWritesForItsOwnBookkeeping(t *testing.T) {
	want := []string{linkAbout, linkCandidate, linkChunk, linkFinding, linkReplacedBy, linkRetires, linkStatement}
	var got []string
	for _, v := range bookkeeping() {
		got = append(got, v.(string))
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("bookkeeping() = %v, want %v", got, want)
	}
	stmt, err := New(nil, Options{}).claimsCypher()
	if err != nil {
		t.Fatalf("claimsCypher: %v", err)
	}
	if !strings.Contains(stmt, "NOT type(r) IN $bookkeeping") {
		t.Errorf("the walk does not exclude the bookkeeping edges:\n%s", stmt)
	}
}

// pgvector's Search refuses k <= 0 for the same reason: an unbounded anchor
// search over a four-hundred-thousand-record import is a page nobody reads and
// a query nobody meant. There is no "everything" value on purpose.
func TestAnAnchorSearchWithoutALimitIsRefusedRatherThanUnbounded(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := New(nil, Options{}).Find(context.Background(), "run", "ravel", limit); err == nil {
			t.Errorf("Find(limit %d) was accepted", limit)
		}
	}
}

// ReservedPrefix is a buyer's to choose, so it reaches a query as an
// identifier, and an identifier this package cannot quote must be refused here
// rather than sent. quoteIdent is the one interpolation site in this package
// and the read path is now the second thing that goes through it.
func TestAReadRefusesAReservedPrefixItCannotQuote(t *testing.T) {
	for _, prefix := range []string{"pre\x00fix", "\xff"} {
		if _, err := New(nil, Options{ReservedPrefix: prefix}).scope(); err == nil {
			t.Errorf("prefix %q was accepted into a query", prefix)
		}
	}
}
