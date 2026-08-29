package runner

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/service"
)

// Endpoint is one model the caller supplied, in the shape a provider package
// needs to reach it. It repeats service.Model's fields rather than reusing the
// type so that a Factory implementation does not have to import the wire layer
// to be written or to be faked.
type Endpoint struct {
	Name    string
	BaseURL string
	APIKey  string
	Options map[string]string
}

// Factory makes a caller's endpoints real.
//
// It is declared here, by the consumer, for the same reason service.Runner is
// declared by pkg/service: it keeps this package testable with fakes, and it
// keeps the decision of which provider a name maps to in the binary, where an
// operator can see it. pkg/model is the implementation the server injects.
type Factory interface {
	LLM(e Endpoint) (alchemy.LLM, error)
	Embedder(e Endpoint) (alchemy.Embedder, error)
	OCR(e Endpoint) (alchemy.OCR, error)
}

// buildModels turns the three endpoints a job supplied into live models.
//
// It is done before anything is read, so a provider that cannot be reached is
// the caller's error at the start rather than a failure an hour into a corpus.
//
// An endpoint nobody supplied stays nil. §5 and pkg/alchemy both say a nil
// model is a configuration and not a defect — a job with no OCR reports a
// scanned page as unread — and building an empty client "just in case" would
// satisfy the interface, defeat pipeline.validate's check that a document job
// has an LLM, and move the failure to the first call.
func buildModels(f Factory, m service.Models) (alchemy.Models, error) {
	var out alchemy.Models
	var err error
	if supplied(m.LLM) {
		if out.LLM, err = f.LLM(endpointOf(m.LLM)); err != nil {
			return alchemy.Models{}, fmt.Errorf("runner: llm %q: %w", m.LLM.Name, err)
		}
	}
	if supplied(m.Embedder) {
		if out.Embedder, err = f.Embedder(endpointOf(m.Embedder)); err != nil {
			return alchemy.Models{}, fmt.Errorf("runner: embedder %q: %w", m.Embedder.Name, err)
		}
	}
	if supplied(m.OCR) {
		if out.OCR, err = f.OCR(endpointOf(m.OCR)); err != nil {
			return alchemy.Models{}, fmt.Errorf("runner: ocr %q: %w", m.OCR.Name, err)
		}
	}
	return out, nil
}

// supplied reports that the caller named this model at all. Either half is
// enough: a name with no URL is a model the provider knows how to reach by
// name, and a URL with no name is an endpoint that will report its own.
func supplied(m service.Model) bool { return m.Name != "" || m.Endpoint != "" }

func endpointOf(m service.Model) Endpoint {
	return Endpoint{Name: m.Name, BaseURL: m.Endpoint, APIKey: m.APIKey, Options: m.Options}
}
