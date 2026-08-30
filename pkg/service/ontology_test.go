package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const narrow = `{"id":"freight-ops@8","parts":{"prose":{
  "entities":[{"name":"Person"}],
  "relations":[]}}}`

// The loop, end to end over the wire: a vocabulary with no word for Team, an
// assertion that needs one, and the proposal that comes back accepted into the
// next document — which then governs the same assertion cleanly.
//
// It is one test rather than three because the halves are only worth anything
// together. A proposal nobody can accept is a report, and an extension nobody
// derived from a corpus is somebody editing JSON, which is the order this
// endpoint exists to reverse.
func TestAProposalFromAnAssertionCanBeAcceptedAndThenGovernsIt(t *testing.T) {
	cli := dial(t, harness{})
	ctx := authed(context.Background())

	assertion := &alchemyv1.AssertRequest{
		Entities: []*alchemyv1.Entity{
			{Id: "team:ravel", Type: "Team", Name: "Ravel Team"},
		},
		By:       "liliang",
		Ontology: narrow,
		Part:     "prose",
	}
	first, err := cli.Assert(ctx, assertion)
	if err != nil {
		t.Fatalf("Assert: %v", err)
	}
	if len(first.GetProposals()) != 1 {
		t.Fatalf("proposals = %d, want the one undeclared type\n%+v", len(first.GetProposals()), first.GetProposals())
	}
	if len(first.GetViolations()) == 0 {
		t.Fatal("the assertion reported no violation for a type the vocabulary does not declare")
	}

	ext, err := cli.ExtendOntology(ctx, &alchemyv1.ExtendOntologyRequest{
		Ontology: narrow,
		Part:     "prose",
		Accept:   first.GetProposals(),
		By:       "liliang",
	})
	if err != nil {
		t.Fatalf("ExtendOntology: %v", err)
	}
	if ext.GetId() != "freight-ops@9" {
		t.Errorf("new id = %q, want the version bumped: a vocabulary that gained a type is a "+
			"different one and every record names it", ext.GetId())
	}
	if len(ext.GetAdded()) != 1 || ext.GetAdded()[0] != "Team" {
		t.Errorf("added = %v, want [Team]", ext.GetAdded())
	}
	// The document is what the caller keeps and supplies next, so it has to be
	// a document: parseable, and carrying what was accepted.
	var doc map[string]any
	if err := json.Unmarshal([]byte(ext.GetOntology()), &doc); err != nil {
		t.Fatalf("the returned ontology is not JSON: %v", err)
	}

	assertion.Ontology = ext.GetOntology()
	second, err := cli.Assert(ctx, assertion)
	if err != nil {
		t.Fatalf("Assert under the extended ontology: %v", err)
	}
	if n := len(second.GetViolations()); n != 0 {
		t.Errorf("the same assertion still reports %d violation(s) under the vocabulary that was "+
			"just extended to allow it: %+v", n, second.GetViolations())
	}
	if n := len(second.GetProposals()); n != 0 {
		t.Errorf("it still proposes %d type(s) the vocabulary now declares", n)
	}
}

// Accepting nothing would mint a new id for an unchanged document, and an id
// that moved for no reason is worse than one that did not: every record
// extracted under it names a vocabulary that differs from its predecessor in
// nothing a reader can find.
func TestExtendingWithNothingAcceptedIsRefused(t *testing.T) {
	cli := dial(t, harness{})
	_, err := cli.ExtendOntology(authed(context.Background()), &alchemyv1.ExtendOntologyRequest{
		Ontology: narrow, Part: "prose", By: "liliang",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (err %v)", status.Code(err), err)
	}
}

// A relation whose ends were never observed would be declared open, and
// pkg/ontology reads an open end as "any". The refusal has to reach the wire
// intact, because the caller is the one who can act on it.
func TestARelationWithNoObservedEndsIsRefusedOverTheWireWithTheWayPastIt(t *testing.T) {
	cli := dial(t, harness{})
	_, err := cli.ExtendOntology(authed(context.Background()), &alchemyv1.ExtendOntologyRequest{
		Ontology: narrow, Part: "prose", By: "liliang",
		Accept: []*alchemyv1.Proposal{{
			Kind: alchemyv1.ProposalKind_PROPOSAL_KIND_RELATION,
			Type: "MEMBER_OF", Records: 4, From: []string{"Person"},
		}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (err %v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "Accept the entity types first") {
		t.Errorf("the refusal does not tell the caller how to get past it: %v", err)
	}
}

// Extending is a judgement about what a type means, and §5c's argument about
// rules applies unchanged: one nobody signed cannot be argued with later.
func TestExtendingAnOntologyUnsignedIsRefused(t *testing.T) {
	cli := dial(t, harness{})
	_, err := cli.ExtendOntology(authed(context.Background()), &alchemyv1.ExtendOntologyRequest{
		Ontology: narrow, Part: "prose",
		Accept: []*alchemyv1.Proposal{{Kind: alchemyv1.ProposalKind_PROPOSAL_KIND_ENTITY, Type: "Team"}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument (err %v)", status.Code(err), err)
	}
}
