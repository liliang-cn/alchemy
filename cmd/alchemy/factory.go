package main

import (
	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/model"
	"github.com/liliang-cn/alchemy/pkg/runner"
)

// modelFactory is the injection point pkg/runner leaves open, and the only
// place in the program that knows which package talks to a model endpoint.
//
// It exists so that pkg/runner can be tested with fakes and so that a second
// provider is a type in this file rather than an edit to the pipeline. The
// translation is field for field; there is deliberately no defaulting here,
// because a base URL this file invented would be an endpoint the caller did
// not ask for, and §6 is explicit that a buyer's endpoints are their business.
type modelFactory struct{}

var _ runner.Factory = modelFactory{}

func (modelFactory) LLM(e runner.Endpoint) (alchemy.LLM, error) {
	return model.NewLLM(endpoint(e))
}

func (modelFactory) Embedder(e runner.Endpoint) (alchemy.Embedder, error) {
	return model.NewEmbedder(endpoint(e))
}

func (modelFactory) OCR(e runner.Endpoint) (alchemy.OCR, error) {
	return model.NewOCR(endpoint(e))
}

func endpoint(e runner.Endpoint) model.Endpoint {
	return model.Endpoint{Name: e.Name, BaseURL: e.BaseURL, APIKey: e.APIKey, Options: e.Options}
}
