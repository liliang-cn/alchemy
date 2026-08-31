package kgagent_test

import (
	"context"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// store is a recall.Reader that answers from maps, so that every test below
// runs with no database and no model endpoint.
//
// It is deliberately faithful about the three things the defects turned on and
// careless about everything else: an empty `about` means every question, a
// negative chunk is ErrNoChunk, and a claim carries both names and ids. A fake
// that got those wrong would let the tests pass over the bugs they exist for.
type store struct {
	load      string
	nodes     []recall.Node
	records   map[string]recall.Description
	claims    map[string][]recall.Claim
	chunks    map[string]recall.Citation // keyed source#index
	questions []recall.Question
	types     []recall.TypeCount
}

func (s *store) Find(_ context.Context, load, name string, limit int) (recall.Found, error) {
	if load != s.load {
		return recall.Found{}, recall.ErrNoLoad
	}
	var hits []recall.Node
	for _, n := range s.nodes {
		if strings.Contains(strings.ToLower(n.Name), strings.ToLower(name)) {
			hits = append(hits, n)
		}
	}
	found := recall.Found{Total: len(hits), Nodes: hits}
	if len(hits) > limit {
		found.Nodes = hits[:limit]
	}
	return found, nil
}

func (s *store) Claims(_ context.Context, _, id string) ([]recall.Claim, error) {
	return s.claims[id], nil
}

func (s *store) Cite(_ context.Context, _, source string, index int) (recall.Citation, error) {
	if index < 0 {
		return recall.Citation{}, recall.ErrNoChunk
	}
	c, ok := s.chunks[recall.Mark(source, index)]
	if !ok {
		return recall.Citation{}, recall.ErrNoCitation
	}
	return c, nil
}

func (s *store) Unanswered(_ context.Context, _, about string) ([]recall.Question, error) {
	if about == "" {
		return s.questions, nil
	}
	var out []recall.Question
	for _, q := range s.questions {
		if strings.Contains(strings.ToLower(q.Subject+q.Detail+q.Left+q.Right), strings.ToLower(about)) {
			out = append(out, q)
		}
	}
	return out, nil
}

func (s *store) Contributions(_ context.Context, _, id string) (recall.Contributions, error) {
	return recall.Contributions{ID: id}, nil
}

func (s *store) Describe(_ context.Context, _, id string) (recall.Description, error) {
	return s.records[id], nil
}

func (s *store) Types(_ context.Context, _ string) ([]recall.TypeCount, error) {
	return s.types, nil
}

func (s *store) OfType(_ context.Context, _, typ string, limit int) (recall.Found, error) {
	var hits []recall.Node
	for _, n := range s.nodes {
		if n.Type == typ {
			hits = append(hits, n)
		}
	}
	found := recall.Found{Total: len(hits), Nodes: hits}
	if len(hits) > limit {
		found.Nodes = hits[:limit]
	}
	return found, nil
}

// graph is the corpus the tests read, shaped like the one the defects were
// measured on: two people whose names differ only by a surname and are held
// apart as a question, a claim from a producer that worked in chunks and one
// from a producer that did not, and enough People to overflow an anchor page.
func graph() *store {
	nodes := []recall.Node{
		{ID: "person:mira", Type: "Person", Name: "Mira"},
		{ID: "person:nadia", Type: "Person", Name: "Nadia"},
		{ID: "person:nadia-okonkwo", Type: "Person", Name: "Nadia Okonkwo"},
		{ID: "product:ledger", Type: "Product", Name: "Ledger"},
	}
	for _, n := range []string{"Ada", "Bea", "Cai", "Dee", "Eli", "Fay", "Gil", "Hal", "Ivo", "Jo", "Kit", "Lou"} {
		nodes = append(nodes, recall.Node{ID: "person:" + strings.ToLower(n), Type: "Person", Name: n})
	}
	prose := alchemy.Provenance{Source: "profile.pdf", Chunk: 20, Producer: alchemy.ProducerLLMExtract}
	asserted := alchemy.Provenance{Source: "team.json", Chunk: -1, Producer: alchemy.ProducerGraphImport}
	said := alchemy.Provenance{
		Source: "slack/#general", Chunk: -1, Producer: alchemy.ProducerHuman,
		By: "joel.c@halcyon.com", At: "2026-08-31T18:35:00Z",
	}
	return &store{
		load:  "ld-1",
		nodes: nodes,
		// One record carrying detail no one-line claim can show: a window in
		// attributes, and a named person with a date. This is the shape the
		// leave experiment proved unreachable.
		records: map[string]recall.Description{
			"absence:1": {
				ID: "absence:1", Type: "Absence", Name: "Joel C parental leave",
				Aliases: []string{"Joel parental leave"},
				Attributes: map[string]any{
					"from": "2026-10-05", "to": "2026-11-05", "start_confirmed": false,
				},
				Provenance: said,
			},
			"person:mira": {ID: "person:mira", Type: "Person", Name: "Mira", Provenance: prose},
		},
		types: []recall.TypeCount{{Type: "Person", Count: 15}, {Type: "Product", Count: 1}},
		claims: map[string][]recall.Claim{
			"person:mira": {
				recall.NewClaim(recall.Endpoint{ID: "person:mira", Name: "Mira"},
					recall.Endpoint{ID: "product:ledger", Name: "Ledger"}, "DEVELOPS", asserted),
				recall.NewClaim(recall.Endpoint{ID: "person:mira", Name: "Mira"},
					recall.Endpoint{ID: "org:halcyon", Name: "Halcyon"}, "WORKS_FOR", prose),
			},
		},
		chunks: map[string]recall.Citation{
			"[profile.pdf#20]": {Source: "profile.pdf", Index: 20, Start: 100, End: 122, Text: "Mira works for Halcyon."},
			"[profile.pdf#0]":  {Source: "profile.pdf", Index: 0, Start: 0, End: 11, Text: "Chapter one"},
		},
		questions: []recall.Question{{
			Signal: alchemy.DuplicateNameAffix, Subject: "Nadia ~ Nadia Okonkwo",
			Detail: "one name is the other with a word added", Left: "Nadia", Right: "Nadia Okonkwo",
		}},
	}
}
