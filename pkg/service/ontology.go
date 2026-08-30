package service

import (
	"context"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/wire"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// ExtendOntology closes the loop a Result's proposals open.
//
// A run says which types its corpus used and its vocabulary does not declare;
// this takes the ones a person accepted and hands back the document that does
// declare them. Nothing is stored between the two calls — see the rpc's
// comment, and pkg/ontology.Extend, which is where the rules are and where
// they are tested. This method is the wire around that function and owns two
// things it cannot: which errors are the caller's fault, and reading a part
// name the same way every other endpoint in this package reads one.
//
// It is deliberately not gated on the proposals having come from a job this
// service ran. A caller may hold a result from last month, or one produced on
// another node (§8.3), and demanding a job id would make the endpoint useless
// for exactly the case a vocabulary grows in — somebody reading a report and
// deciding, later, that the word should exist.
func (s *Server) ExtendOntology(ctx context.Context, req *alchemyv1.ExtendOntologyRequest) (*alchemyv1.ExtendOntologyResponse, error) {
	if strings.TrimSpace(req.GetOntology()) == "" {
		return nil, wireError(invalid("extend: no ontology to extend; this returns the next version of a document and needs the one it follows"))
	}
	if len(req.GetAccept()) == 0 {
		return nil, wireError(invalid("extend: no proposals accepted; extending a vocabulary with nothing in it would mint a new id for an unchanged document, and an id that moved for no reason is worse than one that did not move"))
	}
	o, err := ontology.Load(strings.NewReader(req.GetOntology()))
	if err != nil {
		return nil, wireError(invalid("extend: %s", err))
	}
	accepted := make([]alchemy.Proposal, 0, len(req.GetAccept()))
	for _, p := range req.GetAccept() {
		accepted = append(accepted, proposalFromProto(p))
	}
	out, added, err := o.Extend(partAsserted(req.GetPart()), accepted, req.GetBy(), req.GetNewId())
	if err != nil {
		// Every refusal Extend makes is about what the caller sent — an
		// unsigned request, a relation whose ends nobody observed, a version
		// this package may not guess the successor of — so they are the
		// caller's to fix and each one says how.
		return nil, wireError(invalid("extend: %s", err))
	}
	doc, err := out.Document()
	if err != nil {
		return nil, err
	}
	return &alchemyv1.ExtendOntologyResponse{
		Ontology: string(doc),
		Id:       out.ID,
		Added:    added,
	}, nil
}

// proposalFromProto reads a proposal back off the wire, narrowed to what
// Extend uses.
//
// The reading itself is pkg/wire's, and has to be: the producer table and the
// proposal-kind table are closed sets whose two halves must not drift, and a
// second decoder here would be a second place for one of them to go short.
// What stays local is the narrowing. Example is a pointer back at the corpus
// and has no meaning in a vocabulary, and a caller who edits the ends before
// accepting is making exactly the judgement this endpoint exists to keep in a
// person's hands — so the ends are taken as sent rather than checked against
// anything.
func proposalFromProto(p *alchemyv1.Proposal) alchemy.Proposal {
	out := wire.ProposalFromProto(p)
	out.Example = alchemy.Ref{}
	return out
}
