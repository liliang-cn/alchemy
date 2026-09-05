# alchemy

Turns files into a knowledge graph an LLM agent can query, where every record
says which file, which chunk and which producer it came from — and whether it
was stated by a source or inferred by a model.

## Install

```sh
go get github.com/liliang-cn/alchemy             # service + pkg/recall
go get github.com/liliang-cn/alchemy/connectors  # the six stores
go get github.com/liliang-cn/alchemy/mcp         # MCP server
```

## Use

Files in, graph out as JSON; the service stores nothing. gRPC is `UploadSource`
→ `CreateJob` → `WatchJob` → `GetResult`; `ListFindings` and `Decide` review it,
`Assert` adds a fact, `/ui` draws it.

```sh
go build ./cmd/alchemy
./alchemy -addr 127.0.0.1:7431 -http-addr 127.0.0.1:7432 -token-file /etc/alchemy/token
```

Write the result into a store you run, and read it back. `neo4j` `pgvector`
`qdrant` `cortexdb` `dgraph` `rdf` all implement `sink.Sink` and `recall.Reader`.

```go
l, _ := neo4j.Open(ctx, uri, user, pass, neo4j.Options{RunID: "nightly-01"})
sink.Load(ctx, l, result, sink.Options{Load: "nightly-01"})
var r recall.Reader = l                      // the same eight questions, any store
r.Find(ctx, "nightly-01", "ravel", 10)
```

Serve one loaded graph to any MCP client:

```sh
go build -o alchemy-mcp ./mcp/cmd/alchemy-mcp
claude mcp add --scope local alchemy /path/to/alchemy-mcp \
  -store neo4j -uri neo4j://host:7687 -load nightly-01
```

| Tool | Answers |
|---|---|
| `graph_find` | entities whose name contains this text |
| `graph_types` | every entity type and how many carry it |
| `graph_of_type` | every entity of one type |
| `graph_describe` | one entity whole: fields, aliases, provenance |
| `graph_claims` | every claim one hop out, with who said it |
| `graph_contributors` | which sources had a hand in one node |
| `graph_cite` | the text a claim was extracted from |
| `graph_open_questions` | identity questions nobody has answered |

## Environment

Service: `ALCHEMY_TOKEN_FILE` `ALCHEMY_ADDR` `ALCHEMY_HTTP_ADDR` `ALCHEMY_RULES`.
MCP: `ALCHEMY_MCP_USER` `ALCHEMY_MCP_PASSWORD` `ALCHEMY_MCP_DSN` `ALCHEMY_MCP_TOKEN`.
Credentials never come from a flag.

## License

MIT
