package extract

import "github.com/liliang-cn/alchemy/pkg/alchemy"

// Standing is what a reviewer has already settled, asked for rather than
// handed over.
//
// It is a function and not a slice because of DESIGN.md §6's first reason for
// choosing gRPC: "a person working a queue wants their decisions to take
// effect on work still running — an extractor that has already learned 'this
// is not an entity' should stop proposing it in the next chunk." A slice is
// the state of the conversation at the moment the job started, and by chunk
// forty that is not the conversation any more.
//
// Nil is a run with nobody reviewing it, which is every run of an unattended
// import and the one this package is a pure function of its input for.
type Standing func() Settled

// Settled is one snapshot of the standing answers.
//
// It is a snapshot rather than three separate questions because everything
// about one chunk — the prompt it is asked with, which of its proposals
// survive, and what their provenance says they were extracted under — has to
// be decided from one reading. A decision arriving between two of those
// questions would produce a chunk whose provenance names a rule its prompt
// never carried.
type Settled struct {
	// Told is the standing answers as the model is shown them, one line each.
	// They go into the system prompt, which is part of the cache address
	// (§8.2), so a chunk asked under a rule can never be served the answer to
	// the question asked without it.
	Told []string
	// Named is what alchemy.Provenance.Rules records for every record this
	// chunk produced. It names the answers rather than restating them: the
	// prompt is prose for a model and this is an identifier for a reader
	// comparing two runs.
	Named string
	// Filter settles the proposals the snapshot already answers, before they
	// enter the graph. Nil settles nothing.
	//
	// It is a callback rather than a rule list this package interprets because
	// what a rule *means* is pkg/review's subject. Extraction proposes and
	// review decides; a stage that knew how to apply a decision would be doing
	// both, and the design's whole claim is that those are two acts by two
	// parties.
	Filter func(alchemy.Chunk, []alchemy.Entity, []alchemy.Relation) ([]alchemy.Entity, []alchemy.Relation)
}

// inForce takes one snapshot for one chunk. A nil Standing is the zero
// snapshot: nothing to tell the model, nothing to name, nothing to settle.
func (o Options) inForce() Settled {
	if o.Standing == nil {
		return Settled{}
	}
	return o.Standing()
}

// settle applies the snapshot to one chunk's proposal.
func (s Settled) settle(c alchemy.Chunk, ents []alchemy.Entity, rels []alchemy.Relation) ([]alchemy.Entity, []alchemy.Relation) {
	if s.Filter == nil {
		return ents, rels
	}
	return s.Filter(c, ents, rels)
}
