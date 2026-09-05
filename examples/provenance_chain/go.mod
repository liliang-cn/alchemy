// provenance_chain is a module of its own for the reason kgagent and connectors
// are: it imports a store, and the core module's dependency list is a claim
// DESIGN.md §9 makes and internal/claims checks on every run.
module github.com/liliang-cn/alchemy/examples/provenance_chain

go 1.25.0

// For working in this repository only — a consumer's build ignores a replace in
// a dependency's go.mod, so the require below has to name versions that exist.
replace github.com/liliang-cn/alchemy => ../..

replace github.com/liliang-cn/alchemy/connectors => ../../connectors

require (
	github.com/liliang-cn/alchemy v0.4.0
	github.com/liliang-cn/alchemy/connectors v0.4.0
	github.com/liliang-cn/cortexdb/v2 v2.93.0
)

require (
	github.com/0x51-dev/rdf v0.1.0 // indirect
	github.com/0x51-dev/rids v0.1.0 // indirect
	github.com/0x51-dev/upeg v0.1.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/ledongthuc/pdf v0.0.0-20250511090121-5959a4027728 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/modelcontextprotocol/go-sdk v1.6.1 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/exp v0.0.0-20250620022241-b7579e27df2b // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	modernc.org/libc v1.66.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.38.2 // indirect
)
