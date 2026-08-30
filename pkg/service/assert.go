package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/ontology"
	"github.com/liliang-cn/alchemy/pkg/preflight"
	"github.com/liliang-cn/alchemy/pkg/verify"
	alchemyv1 "github.com/liliang-cn/alchemy/proto/alchemy/v1"
)

// Assert records a fact somebody knows and no document states.
//
// It is synchronous, and that is a property of the operation rather than a
// shortcut taken with it. An assertion has no chunking, no model call and no
// embedding — there is a parse and a check against the ontology, and both are
// over a handful of records a person typed. Handing back a job id for a caller
// to poll would be inventing asynchrony this operation does not have, so the
// graph is built here and returned here. The job id rides on the result, which
// is how the record stays traceable through GetJob like any other.
//
// Nothing about it is a hole in §4. What is stored is a job, which is what
// every other call stores; the graph goes back to the caller and this service
// forgets it, and what happens to it afterwards is the caller's business
// exactly as it is for an extraction.
func (s *Server) Assert(ctx context.Context, req *alchemyv1.AssertRequest) (*alchemyv1.Result, error) {
	res, err := s.stated(req)
	if err != nil {
		return nil, wireError(err)
	}
	// The job is minted only once the assertion is known to be one this
	// service will return. A refusal that had already admitted a job would
	// leave a record of work nobody did, waiting out its expiry, for every
	// caller who mistyped a type name.
	id, err := s.recordAssertion(ctx)
	if err != nil {
		return nil, wireError(err)
	}
	res.Job = id
	return resultToProto(res), nil
}

// stated turns the request into the graph the call will return, or refuses.
//
// Everything refused here could not be a valid assertion for any state of the
// world, which is the line errors.go draws between InvalidArgument and
// FailedPrecondition: a caller who named nobody has not sent a request that
// will be correct in a minute.
func (s *Server) stated(req *alchemyv1.AssertRequest) (alchemy.Result, error) {
	by := strings.TrimSpace(req.GetBy())
	if by == "" {
		// alchemy.ProducerHuman's whole warrant is that there is a person who
		// can be asked. pkg/preflight would report this afterwards as
		// assertion_unsigned, and reporting it would be the wrong answer here:
		// the record is only admissible because somebody signed it, so the
		// signature is a precondition rather than a finding.
		return alchemy.Result{}, invalid("assert: an assertion must name who is asserting; a record whose only authority is a person and which names no person is an anonymous claim wearing a person's badge")
	}
	if len(req.GetEntities()) == 0 && len(req.GetRelations()) == 0 {
		return alchemy.Result{}, invalid("assert: %s asserted nothing; an assertion with no entities and no relations is a mistake rather than an empty graph", by)
	}

	vocabulary, ontologyID, err := vocabularyOf(req)
	if err != nil {
		return alchemy.Result{}, err
	}

	// One Provenance for every record of the assertion, because it is one
	// statement: the same person, the same moment, the same vocabulary. The
	// clock is read once for the same reason — two records of one sentence
	// timestamped a second apart would invite a reader to believe they were
	// asserted separately.
	stamp := alchemy.Provenance{
		// The source of an assertion is the asserter. Every other producer
		// names a document here and there is no document, and leaving it empty
		// would break §5b's promise that every fact names its source in the one
		// case where the source is a person who can be asked about it.
		Source: by,
		// No chunks, so no chunk. See Provenance.Chunk: -1 is what a producer
		// that did not work in chunks says, and any index would be a citation
		// into a Chunks slice this result does not carry.
		Chunk:    -1,
		Producer: alchemy.ProducerHuman,
		Ontology: ontologyID,
		By:       by,
		At:       time.Now().UTC().Format(time.RFC3339),
	}

	note := strings.TrimSpace(req.GetNote())
	entities := make([]alchemy.Entity, 0, len(req.GetEntities()))
	for _, e := range req.GetEntities() {
		entities = append(entities, alchemy.Entity{
			ID: e.GetId(), Type: e.GetType(), Name: e.GetName(),
			Attributes: noted(e.GetAttributes().AsMap(), note),
			// Deliberately not provenanceFromProto(e.GetProvenance()). The
			// field is stamped and never read: a caller who could fill it in
			// could assert on somebody else's behalf, or backdate one, and
			// either turns the record this endpoint exists to produce back into
			// the one it exists to replace.
			Provenance: stamp,
		})
	}
	relations := make([]alchemy.Relation, 0, len(req.GetRelations()))
	for _, r := range req.GetRelations() {
		relations = append(relations, alchemy.Relation{
			From: r.GetFrom(), To: r.GetTo(), Type: r.GetType(), Key: r.GetKey(),
			Attributes: noted(r.GetAttributes().AsMap(), note),
			Provenance: stamp,
		})
	}

	res := checked(req, entities, relations, vocabulary, ontologyID)
	res.Counts = res.Derivable()
	if err := admissible(res); err != nil {
		return alchemy.Result{}, err
	}
	return res, nil
}

// checked runs the assertion past the same verifier a job's extraction goes
// past, and keeps the same parts of its report pkg/pipeline keeps.
//
// The ontology is optional and checked when given, which is the rule graph
// sources already follow. The violations are dropped when no vocabulary was
// supplied for the reason pipeline's verify gives: Check is handed an empty
// vocabulary, an empty vocabulary permits nothing, and every violation it then
// produces says "the vocabulary you did not supply does not declare this" —
// a fact about the request rather than about what was asserted.
//
// The conflicts and the duplicates are kept whatever the ontology said, also
// for pipeline's reason: two claims disagreeing and two names for one thing are
// neither of them rules anybody declared, so an ungoverned assertion is exactly
// as entitled to the finding as a governed one.
func checked(req *alchemyv1.AssertRequest, entities []alchemy.Entity, relations []alchemy.Relation, vocabulary ontology.Vocabulary, ontologyID string) alchemy.Result {
	rep := verify.Check(verify.Input{
		Entities:   entities,
		Relations:  relations,
		Vocabulary: vocabulary,
		OntologyID: ontologyID,
	})
	res := alchemy.Result{
		// Check returns the graph with its types canonicalised, and that is
		// what is returned: a caller who asserted "cluster" under a vocabulary
		// that declares "Cluster" stated the declared type, and handing back
		// their spelling would leave a graph a traversal keyed on the type name
		// only finds part of.
		Entities:   rep.Entities,
		Relations:  rep.Relations,
		Conflicts:  rep.Conflicts,
		Duplicates: rep.Duplicates,
	}
	if strings.TrimSpace(req.GetOntology()) != "" {
		res.Violations = rep.Violations
	}
	return res
}

// admissible is pkg/preflight asked of the graph this call is about to return,
// with everything it finds turned into the refusal.
//
// Refusing on a report as well as on a refusal is stricter than that package's
// own line, and the difference is what an assertion is. preflight draws the
// line where it does because a pipeline's result is two hundred thousand
// records nobody typed, and losing the other 199,999 over one bad citation is
// the worse outcome; an assertion is a handful of records one person typed one
// minute ago, every field preflight checks was either stamped here or supplied
// by them, and the person is still on the other end of the call. Handing them
// back a graph this design's own checker calls defective, from the one endpoint
// whose whole value is that somebody stands behind the record, would be worse
// than making them fix the typo.
//
// It is also the only place they can be told. alchemyv1.Result has no field for
// a Defect — pkg/sink turns them into an error and nothing has ever put one on
// the wire — so a report attached to a successful response would be a report
// with nowhere to go.
//
// Every defect is named rather than the first, which is preflight.Refuse's
// discipline: a caller who fixes one and calls again to discover the next is
// using the service as an error message.
func admissible(res alchemy.Result) error {
	defects := preflight.Check(res)
	if len(defects) == 0 {
		return nil
	}
	lines := make([]string, 0, len(defects))
	for _, d := range defects {
		lines = append(lines, fmt.Sprintf("%s: %s", d.Kind, d.Detail))
	}
	return invalid("assert: %d defect(s) in what was asserted: %s", len(defects), strings.Join(lines, "; "))
}

// vocabularyOf resolves the part of the ontology this assertion is checked
// against, and reports nothing when none was supplied.
//
// A malformed document is refused rather than treated as an absent one. The
// two are a sentence apart on the wire and a world apart in meaning: an
// assertion checked against nothing comes back clean, so a caller whose
// vocabulary failed to parse would be told their undeclared type was fine.
func vocabularyOf(req *alchemyv1.AssertRequest) (ontology.Vocabulary, string, error) {
	doc := strings.TrimSpace(req.GetOntology())
	if doc == "" {
		return ontology.Vocabulary{}, "", nil
	}
	o, err := ontology.Load(strings.NewReader(doc))
	if err != nil {
		return ontology.Vocabulary{}, "", invalid("assert: %s", err)
	}
	v, err := o.Vocabulary(partAsserted(req.GetPart()))
	if err != nil {
		return ontology.Vocabulary{}, "", invalid("assert: %s", err)
	}
	return v, o.ID, nil
}

// partAsserted reads an empty part as prose, which is the reading JobSpec.Part
// already documents and pkg/runner already implements. A person stating a fact
// in their own words is prose by every meaning of the word, and a second answer
// here would be a second closed set to keep in step with pkg/ontology's.
func partAsserted(name string) ontology.Part {
	if strings.TrimSpace(name) == "" {
		return ontology.PartProse
	}
	return ontology.Part(name)
}

// noted puts the asserter's reason on the record rather than on the envelope.
//
// A fact stated with a reason is a different thing to audit from one stated
// without, and the envelope is the half of a result that does not survive being
// loaded: an edge in a store months later either carries why it was asserted or
// nobody will ever find out.
func noted(attributes map[string]any, note string) map[string]any {
	if note == "" {
		if len(attributes) == 0 {
			// Nil rather than an empty map, so a record with nothing on it
			// marshals without an attributes key at all.
			return nil
		}
		return attributes
	}
	if attributes == nil {
		attributes = make(map[string]any, 1)
	}
	attributes["note"] = note
	return attributes
}

// recordAssertion admits the job this assertion is remembered as and finishes
// it, and hands back its id.
//
// It ends at SUCCEEDED rather than at PENDING because the work is over: a job
// left pending would expire, and until it did it would tell an operator that
// this node still owes somebody an import. pkg/job allows no jump — PENDING to
// SUCCEEDED is absent from the table, deliberately, so that nothing can finish
// work it never held — so this takes the same route a worker takes, by leasing
// the job and moving it on. Assert is the worker for its own job.
//
// The claim is checked against the id, and that check is not defensive
// bookkeeping. Claim is the queue: it hands back the oldest claimable job,
// which on a busy node is somebody else's import rather than this assertion.
// Finishing that job as though it were an assertion would mark an import
// SUCCEEDED that never ran, so a foreign lease is handed straight back and the
// assertion is refused instead. See the note in the report accompanying this
// file: a synchronous operation needs a way to lease a job it names, and
// pkg/job.Store does not offer one.
func (s *Server) recordAssertion(ctx context.Context) (string, error) {
	id := mintID()
	if _, err := s.store.Create(ctx, id); err != nil {
		return "", err
	}
	lease, ok, err := s.store.Claim(ctx, s.node, leaseTTL)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", wrongState("assert: job %s was admitted and is not claimable, so the assertion cannot be recorded as finished", id)
	}
	if lease.Job.ID != id {
		_ = s.store.Release(ctx, lease)
		return "", wrongState("assert: the queue offered job %s rather than this assertion's job %s; try again", lease.Job.ID, id)
	}
	if err := s.store.Transition(ctx, lease, alchemy.JobSucceeded); err != nil {
		return "", err
	}
	return id, nil
}
