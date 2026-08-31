# alchemy

Turns sources — PDFs, schemas, tables, existing graphs — into a knowledge graph
where **every record can say where it came from**: which file, which chunk of it,
which producer, under which ontology, and whether it was *stated* by something
that already asserted it or *inferred* by a model reading prose.

That is the whole product. Extracting a graph with an LLM is easy and common;
checking one afterwards is not.

- **Provenance on every entity and every edge**, not on the batch. Two documents
  that assert the same edge stay two records with two sources.
- **The graph reports its own quality.** Ontology violations, duplicate
  candidates nobody ruled on, guesses the mapper made, and sources it could not
  read are delivered *with* the result rather than swallowed.
- **A conflict stops the job.** Two sources that disagree leave it at
  `NEEDS_REVIEW` and `GetResult` refuses, because a graph that silently picked a
  winner is worse than no graph.
- **Nothing is extracted without a vocabulary to check it against**, and the
  corpus is what tells you the vocabulary is missing something.

## Running it

```sh
make build   # or: go build ./cmd/alchemy
./alchemy -addr 127.0.0.1:7431 \
          -http-addr 127.0.0.1:7432 \
          -token-file /etc/alchemy/token \
          -rules /etc/alchemy/always.json
```

`-http-addr` is optional; empty means no gateway. There is deliberately no
`-token` flag — a secret on the command line is a secret in `ps`.

gRPC is the interface. `UploadSource` streams a file in, `CreateJob` starts the
run, `WatchJob` streams progress and cost, `GetResult` (or `StreamResult`, for a
graph too big for one message) hands it back. `ListFindings`/`Decide` are the
review path, `ExtendOntology` adds a type a corpus turned out to need, and
`Assert` puts in a fact a person knows and no document states, stamped with who
said it and when. The gateway is generated from the same service definition and
refuses `Review` with 501 rather than pretend a bidirectional stream has an
honest shape over HTTP.

## It returns a graph; it does not store one

alchemy holds no database. The companion module `connectors/` writes a result into
a store you already run — **Neo4j, pgvector, Qdrant, CortexDB, or any SPARQL
endpoint speaking RDF-star** — and reads it back through `pkg/recall`, seven
primitives an agent can build a context pack from:

    Find · Claims · Cite · Unanswered · Contributions · Types · OfType

They were not designed up front. Each of the last three was added after an agent
answered a question wrongly in a way none of the others could have caught: a join
the graph had made silently, a citation refused for the store's most trustworthy
records, an enumeration attempted by trying the alphabet. The measurement is
written down beside each method.

`examples/kgagent` is that agent — a ReAct loop that knows nothing but these
seven — and doubles as the instrument the defects were found with. Its tests
need no server and no model: every case in them is a wrong answer that reached
production.

`DESIGN.md` is the specification and the argument, including what is deliberately
*not* built. Every measurable sentence in its status section is a check in
`internal/claims` that `go test ./...` runs, so the next number to go stale fails
instead of ageing.

```sh
go test ./...                    # the root module needs no database
cd connectors && go test ./...   # skips loudly without ALCHEMY_TEST_* servers
```
