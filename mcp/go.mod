// The MCP server lives in its own module so that a protocol SDK lands neither
// in the core module, whose dependency list DESIGN.md §9 states and
// internal/claims checks on every run, nor in connectors, which a buyer imports
// in order to write a graph into their own store.
module github.com/liliang-cn/alchemy/mcp

go 1.25.0

replace github.com/liliang-cn/alchemy => ..

replace github.com/liliang-cn/alchemy/connectors => ../connectors

require (
	github.com/liliang-cn/alchemy v0.0.0
	github.com/liliang-cn/alchemy/connectors v0.0.0-00010101000000-000000000000
	github.com/modelcontextprotocol/go-sdk v1.7.0
)

require (
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/neo4j/neo4j-go-driver/v5 v5.28.4 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)
