package service

import (
	"context"
	"errors"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/job"
	"github.com/liliang-cn/alchemy/pkg/review"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
	"google.golang.org/protobuf/types/known/emptypb"
)

// CreateJob admits a job and starts it.
//
// The order here is not incidental. The spec is registered before the store is
// asked to admit the job, because a worker claiming from the queue must always
// find one; and the ID is minted here rather than by the store, because the
// caller's idempotency key has to be that ID for a retry to collide with the
// job it is retrying.
func (s *Server) CreateJob(ctx context.Context, req *alchemyv1.CreateJobRequest) (*alchemyv1.Job, error) {
	spec, err := s.specOf(req)
	if err != nil {
		return nil, wireError(err)
	}

	id := req.GetIdempotencyKey()
	if id == "" {
		id = mintID()
	}
	s.register(id, spec)

	j, err := s.store.Create(ctx, id)
	if errors.Is(err, job.ErrExists) {
		// A retry of work already admitted. Returning the stored job without
		// starting a second run is the whole point of the key: a client whose
		// call timed out must not import a 10GB dump twice.
		return jobToProto(j), nil
	}
	if err != nil {
		s.drop(id)
		// §8.4's "try later" reaches the client as ResourceExhausted, which is
		// the one refusal here that a retry loop should act on.
		return nil, wireError(err)
	}

	s.wg.Add(1)
	go s.work()
	return jobToProto(j), nil
}

// specOf validates the request and turns it into what a runner is given.
//
// Everything refused here is refused because it could not be a valid request
// for any state of the world, which is what separates InvalidArgument from
// FailedPrecondition in errors.go.
func (s *Server) specOf(req *alchemyv1.CreateJobRequest) (JobSpec, error) {
	if len(req.GetSourceIds()) == 0 {
		return JobSpec{}, invalid("create: a job with no sources has nothing to import")
	}
	sources, err := s.sourcesFor(req.GetSourceIds())
	if err != nil {
		return JobSpec{}, err
	}
	// §5: supplying an ontology is required for document sources. There is no
	// unconstrained mode, and the refusal is here rather than in the extractor
	// so a caller learns before paying for a corpus read.
	if req.GetOntology() == "" && hasKind(sources, alchemy.SourceDocument) {
		return JobSpec{}, invalid("create: a document source requires an ontology; there is no unconstrained extraction mode")
	}
	if req.GetChunking().GetOverlap() < 0 {
		return JobSpec{}, invalid("create: chunk overlap cannot be negative")
	}
	if req.GetReview().GetMinConfidence() < 0 || req.GetReview().GetMinConfidence() > 1 {
		return JobSpec{}, invalid("create: min_confidence is a confidence, so it lies between 0 and 1")
	}

	rules := make([]review.Rule, 0, len(req.GetReview().GetRules()))
	for _, r := range req.GetReview().GetRules() {
		rules = append(rules, ruleFromProto(r))
	}
	return JobSpec{
		Sources:  sources,
		Ontology: req.GetOntology(),
		// Carried, not interpreted. An empty part means prose and a part the
		// ontology does not declare is refused, but both of those are
		// statements about a vocabulary, and this layer has a JSON string
		// rather than a vocabulary. pkg/ontology holds the closed set of names
		// and the list of what this document actually declares, which is what
		// the refusal has to say; a second opinion here would be a second
		// closed set to keep in step with it.
		Part: req.GetPart(),
		Models: Models{
			LLM:      modelFromProto(req.GetModels().GetLlm()),
			Embedder: modelFromProto(req.GetModels().GetEmbedder()),
			OCR:      modelFromProto(req.GetModels().GetOcr()),
		},
		Chunking: Chunking{
			Strategy: req.GetChunking().GetStrategy(),
			Size:     int(req.GetChunking().GetSize()),
			Overlap:  int(req.GetChunking().GetOverlap()),
		},
		Review: review.Options{
			Reviewing:     req.GetReview().GetEnabled(),
			MinConfidence: req.GetReview().GetMinConfidence(),
			Rules:         rules,
		},
	}, nil
}

func modelFromProto(m *alchemyv1.ModelEndpoint) Model {
	if m == nil {
		return Model{}
	}
	return Model{Name: m.GetName(), Endpoint: m.GetEndpoint(), APIKey: m.GetApiKey(), Options: m.GetOptions()}
}

func hasKind(sources []Source, kind alchemy.SourceKind) bool {
	for _, src := range sources {
		if src.Kind == kind {
			return true
		}
	}
	return false
}

// GetJob reports where a job is.
//
// A held job says so, and that is the whole of its contract: §7.3 makes
// NEEDS_REVIEW an ordinary outcome rather than an error, and a client that
// could not tell it from RUNNING would wait forever for a job that is waiting
// for them.
func (s *Server) GetJob(ctx context.Context, req *alchemyv1.GetJobRequest) (*alchemyv1.Job, error) {
	if req.GetJobId() == "" {
		return nil, wireError(invalid("get_job: no job ID"))
	}
	j, err := s.store.Get(ctx, req.GetJobId())
	if err != nil {
		return nil, wireError(err)
	}
	return jobToProto(j), nil
}

// DeleteJob withdraws a job and forgets everything held for it.
//
// It is also how a job is cancelled while it runs, which §7.2 asks for by
// name: an operator watching the model-call count climb faster than expected
// needs a way to stop paying, and the way is this.
func (s *Server) DeleteJob(ctx context.Context, req *alchemyv1.DeleteJobRequest) (*emptypb.Empty, error) {
	if req.GetJobId() == "" {
		return nil, wireError(invalid("delete_job: no job ID"))
	}
	id := req.GetJobId()
	if _, err := s.store.Get(ctx, id); err != nil {
		return nil, wireError(err)
	}
	_ = s.store.Cancel(ctx, id)
	if err := s.store.Delete(ctx, id); err != nil {
		return nil, wireError(err)
	}
	s.drop(id)
	return &emptypb.Empty{}, nil
}

// drop forgets a job's spec, its pending result, its queue and its spooled
// sources. §4: the service returns its output and forgets it, and this is
// where "forgets" is a line of code rather than a claim.
func (s *Server) drop(id string) {
	s.mu.Lock()
	r := s.runs[id]
	delete(s.runs, id)
	s.mu.Unlock()
	if r == nil {
		return
	}
	// Stopping first: a runner still working on a job nothing is left to
	// record is the one shape of leak the expiry sweep could otherwise create.
	r.stop()
	r.hub.close()
	s.forget(r.spec.Sources)
}
