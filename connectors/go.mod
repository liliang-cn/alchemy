// The connectors live in their own module so that the core module's dependency
// list stays the three things DESIGN.md §4 and §9 claim it is. A buyer who
// wants Neo4j must not be made to pull pgvector, Qdrant and CortexDB as the
// price of the argument that alchemy stores nothing.
module github.com/liliang-cn/alchemy/connectors

go 1.25.0

replace github.com/liliang-cn/alchemy => ..

require (
	github.com/jackc/pgx/v5 v5.10.0
	github.com/liliang-cn/alchemy v0.0.0
	github.com/neo4j/neo4j-go-driver/v5 v5.28.4
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)
