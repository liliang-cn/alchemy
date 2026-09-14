package wire

import (
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// A paged graph comes back whole, summary included.
//
// The summary rides on page zero only, so a reassembler that read it off the
// last page would hand a consumer a complete graph reporting that it contains
// nothing — which is worse than an error, because it is a number somebody will
// quote.
func TestAPagedResultComesBackWhole(t *testing.T) {
	pages := []*alchemyv1.ResultPage{
		{
			Page: 0, Job: "job-1",
			Counts:   &alchemyv1.Counts{Entities: 3, Relations: 2},
			RuleSets: []*alchemyv1.RuleSet{{Name: "house"}},
			Entities: []*alchemyv1.Entity{{Id: "a", Type: "Node", Name: "a"}},
			Relations: []*alchemyv1.Relation{
				{From: "a", To: "b", Type: "links"},
			},
		},
		{
			Page:     1,
			Entities: []*alchemyv1.Entity{{Id: "b", Type: "Node", Name: "b"}},
			Relations: []*alchemyv1.Relation{
				{From: "b", To: "c", Type: "links"},
			},
		},
		{
			Page: 2, Last: true,
			Entities: []*alchemyv1.Entity{{Id: "c", Type: "Node", Name: "c"}},
		},
	}
	got, err := Pages(pages)
	if err != nil {
		t.Fatalf("Pages: %v", err)
	}
	if got.Job != "job-1" {
		t.Errorf("job = %q, want the identity page zero carried", got.Job)
	}
	if len(got.Entities) != 3 || len(got.Relations) != 2 {
		t.Errorf("reassembled %d entities and %d relations, want 3 and 2", len(got.Entities), len(got.Relations))
	}
	if got.Counts.Entities != 3 {
		t.Errorf("counts came back as %+v; the summary is on page zero", got.Counts)
	}
	if len(got.RuleSets) != 1 || got.RuleSets[0].Name != "house" {
		t.Errorf("the policy the graph was extracted under did not survive: %+v", got.RuleSets)
	}
	ids := make([]string, 0, 3)
	for _, e := range got.Entities {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Errorf("entities came back as %v, want them in page order", ids)
	}
}

// Half a graph is not a small graph.
//
// Each of these is a sequence a consumer could plausibly be handed by a
// dropped connection or a server bug, and each would otherwise load quietly as
// a corpus somebody would then trust. There is no way from inside to tell a
// short answer from a complete one, which is why the reassembler refuses
// instead of guessing.
func TestAnIncompletePagedResultIsRefusedRatherThanShortened(t *testing.T) {
	whole := func() []*alchemyv1.ResultPage {
		return []*alchemyv1.ResultPage{
			{Page: 0, Job: "j", Entities: []*alchemyv1.Entity{{Id: "a"}}},
			{Page: 1, Last: true, Entities: []*alchemyv1.Entity{{Id: "b"}}},
		}
	}
	for _, tc := range []struct {
		name  string
		pages []*alchemyv1.ResultPage
		says  string
	}{
		{"nothing at all", nil, "at least one page"},
		{"no last page", whole()[:1], "without a last page"},
		{"a gap", []*alchemyv1.ResultPage{whole()[0], {Page: 2, Last: true}}, "gap in a paged graph"},
		{"a nil page", []*alchemyv1.ResultPage{whole()[0], nil}, "is missing"},
		{"last is not last", []*alchemyv1.ResultPage{
			{Page: 0, Last: true}, {Page: 1, Last: true},
		}, "says it is the last"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Pages(tc.pages)
			if err == nil {
				t.Fatalf("accepted %s and returned %d entities", tc.name, len(got.Entities))
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not explain itself: %v", err)
			}
		})
	}
}

// One page is the ordinary case for a small graph, and it has to produce
// exactly what GetResult would have.
func TestASinglePageIsTheSameGraphGetResultWouldHaveReturned(t *testing.T) {
	one := &alchemyv1.Result{
		Job:      "j",
		Entities: []*alchemyv1.Entity{{Id: "a", Type: "Node", Name: "a"}},
		Counts:   &alchemyv1.Counts{Entities: 1},
	}
	direct := ResultFromProto(one)
	paged, err := Pages([]*alchemyv1.ResultPage{{
		Page: 0, Last: true, Job: one.Job, Entities: one.Entities, Counts: one.Counts,
	}})
	if err != nil {
		t.Fatalf("Pages: %v", err)
	}
	if paged.Job != direct.Job || len(paged.Entities) != len(direct.Entities) {
		t.Errorf("paged = %+v, direct = %+v", paged, direct)
	}
	if paged.Counts != direct.Counts {
		t.Errorf("counts differ: paged %+v, direct %+v", paged.Counts, direct.Counts)
	}
	var _ alchemy.Result = paged
}
