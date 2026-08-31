# Generation, in the repo rather than in somebody's shell history.
#
# DESIGN.md §6 says the REST gateway is "generated from the same definitions".
# That is only true if anybody who clones this can regenerate it, so the
# commands are here verbatim. A protoc invocation reconstructed from a
# transcript is a second source of truth about what the service does, arriving
# by the same route §6 warns about.
#
# Prerequisites, all on $PATH:
#   protoc, protoc-gen-go, protoc-gen-go-grpc, protoc-gen-grpc-gateway,
#   protoc-gen-openapiv2
# `make tools` installs the four Go plugins into $(go env GOPATH)/bin.

MODULE  := github.com/liliang-cn/alchemy
PROTO   := proto/alchemy/v1/alchemy.proto
# proto/ carries our definitions *and* the vendored google/api ones, so a
# single include path is the whole of protoc's world. See proto/google/api.
INCLUDE := -I proto

.PHONY: all generate tools build test vet fmt clean

all: generate build test

# generate rewrites every file marked "Code generated ... DO NOT EDIT".
#
# --openapiv2_opt json_names_for_fields=false keeps the document's field names
# identical to the proto's, which is what the gateway's own JSON marshaller is
# configured to accept (see pkg/gateway). A document naming fields the server
# will not parse is worse than no document: the buyer curling it gets a 400 and
# concludes the product is broken.
generate:
	protoc $(INCLUDE) \
		--go_out=. --go_opt=module=$(MODULE) \
		--go-grpc_out=. --go-grpc_opt=module=$(MODULE) \
		--grpc-gateway_out=. --grpc-gateway_opt=module=$(MODULE) \
		--openapiv2_out=docs \
		--openapiv2_opt=json_names_for_fields=false \
		--openapiv2_opt=openapi_configuration=proto/alchemy/v1/alchemy.openapi.yaml \
		$(PROTO)
	go mod tidy

tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.30.0
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@v2.30.0

# The three modules. They are separate so that the core's dependency list stays
# what DESIGN.md §9 states -- a buyer wanting Neo4j must not compile pgvector,
# and neither should compile an agent framework -- but `go test ./...` stops at
# a module boundary, so a target that named only the first was gating a third of
# the repository. connectors and examples/kgagent were outside it until this
# line existed; connectors skips loudly without ALCHEMY_TEST_* servers, and
# kgagent needs neither a server nor a model.
MODULES := . connectors examples/kgagent

build:
	@for m in $(MODULES); do (cd $$m && go build ./...) || exit 1; done

test:
	@for m in $(MODULES); do echo "== $$m"; (cd $$m && go test ./... -race -count=1) || exit 1; done

vet:
	@for m in $(MODULES); do (cd $$m && go vet ./...) || exit 1; done

fmt:
	@for m in $(MODULES); do gofmt -l $$m; done

clean:
	rm -f proto/alchemy/v1/*.pb.go proto/alchemy/v1/*.pb.gw.go docs/*.swagger.json
