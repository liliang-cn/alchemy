package service

import "github.com/liliang-cn/alchemy/pkg/review"

// Hooks the tests need and no caller does.
//
// They live in a _test.go file rather than in the API because a spool path and
// a decision log are the service's own bookkeeping: exporting them would
// invite a gateway to reach past the RPCs, and §6 is explicit that a gateway
// is a translation and never a second source of truth.

// SourceForTest exposes a spooled upload so a test can check that the bytes
// reached disk rather than a buffer.
func (s *Server) SourceForTest(id string) (Source, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sources[id]
	return src, ok
}

// DecisionsForTest is what the service believes it has been told about a job,
// which is what the reconnection test asserts about.
func (s *Server) DecisionsForTest(jobID string) []review.Decision {
	h := s.hubFor(jobID)
	if h == nil {
		return nil
	}
	return h.Decisions()
}
