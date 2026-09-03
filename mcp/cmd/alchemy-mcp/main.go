// Command alchemy-mcp serves one loaded graph to any MCP client.
//
//	alchemy-mcp -store neo4j -uri neo4j://host:7687 -load <load-id>
//
// Credentials come from the environment and never from a flag, for the reason
// the service binary gives: a secret on a command line is a secret in ps.
//
//	ALCHEMY_MCP_USER, ALCHEMY_MCP_PASSWORD   neo4j
//	ALCHEMY_MCP_DSN                          pgvector  (-uri is ignored)
//	ALCHEMY_MCP_TOKEN                        dgraph, when the alpha has ACL on
//
// The store is a flag because the tools are not: every one of the eight is a
// question pkg/recall answers, and which database is behind it is the one thing
// a model asking them never learns.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liliang-cn/alchemy/connectors/cortexdb"
	"github.com/liliang-cn/alchemy/connectors/dgraph"
	"github.com/liliang-cn/alchemy/connectors/neo4j"
	"github.com/liliang-cn/alchemy/connectors/pgvector"
	"github.com/liliang-cn/alchemy/connectors/qdrant"
	"github.com/liliang-cn/alchemy/connectors/rdf"
	"github.com/liliang-cn/alchemy/mcp"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// stores is every connector that implements recall.Reader.
//
// All five, and CortexDB was the last to arrive. It used to be absent for a
// reason worth keeping: its one enumeration had no filter for which import a
// node belongs to, so three of the eight questions could not be answered over
// it without either scanning the whole store or reporting a match count that
// is not the real one. cortexdb v2.89.0 gave GraphFilter a property scope and
// the connector a way to name its own batch.
//
// It is the one entry here whose -uri is a file path, because CortexDB is a
// file rather than a server — and the one whose store holds things this load
// did not put there, which is what the scoping is for.
var stores = []string{"cortexdb", "dgraph", "neo4j", "pgvector", "qdrant", "rdf"}

func main() {
	fs := flag.NewFlagSet("alchemy-mcp", flag.ContinueOnError)
	store := fs.String("store", "", "which store holds the graph: "+strings.Join(stores, ", "))
	uri := fs.String("uri", "", "how to reach it (bolt/neo4j URL, Qdrant or SPARQL base URL, CortexDB file path)")
	load := fs.String("load", "", "the load to answer from; every tool is scoped to it")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *store == "" || *load == "" {
		fs.Usage()
		fail("both -store and -load are required; a server with no load would be answering from " +
			"whichever import it happened to find, which is the bug the load parameter exists to prevent")
	}

	ctx := context.Background()
	reader, closeIt, err := open(ctx, *store, *uri)
	if err != nil {
		fail(err.Error())
	}
	defer closeIt()

	server, _ := mcp.Serve(reader, *load)
	// stdio, because that is how an MCP client starts a local server: it runs
	// the binary and speaks the protocol down the pipe. Nothing is logged to
	// stdout — that pipe carries the protocol, and a stray line on it is a
	// parse error at the client with no explanation.
	if err := server.Run(ctx, &sdk.StdioTransport{}); err != nil {
		fail(err.Error())
	}
}

// open returns the reader for one store, and the function that closes it.
func open(ctx context.Context, store, uri string) (recall.Reader, func(), error) {
	switch store {
	case "dgraph":
		// -uri is the alpha's HTTP address, not its gRPC port: this connector
		// speaks the HTTP API so that a buyer can reproduce any request it
		// makes with curl. RunID is left empty because every read takes the
		// load as an argument, which is what -load means here.
		l, err := dgraph.Open(ctx, dgraph.Options{Endpoint: uri, Token: os.Getenv("ALCHEMY_MCP_TOKEN")})
		if err != nil {
			return nil, nil, fmt.Errorf("dgraph: %w", err)
		}
		return l, func() { _ = l.Close() }, nil
	case "cortexdb":
		// No credential: a CortexDB is a file, and reaching it is a matter of
		// having the file. RunID is deliberately left empty — it is the write
		// side's identity for a load, and every read below takes the load as
		// an argument instead, which is the whole of what -load means here.
		l, err := cortexdb.Open(uri, cortexdb.Options{})
		if err != nil {
			return nil, nil, fmt.Errorf("cortexdb: %w", err)
		}
		return l, func() { _ = l.Close() }, nil
	case "neo4j":
		l, err := neo4j.Open(ctx, uri, os.Getenv("ALCHEMY_MCP_USER"), os.Getenv("ALCHEMY_MCP_PASSWORD"), neo4j.Options{})
		if err != nil {
			return nil, nil, fmt.Errorf("neo4j: %w", err)
		}
		return l, func() { l.Close(ctx) }, nil
	case "pgvector":
		dsn := os.Getenv("ALCHEMY_MCP_DSN")
		if dsn == "" {
			return nil, nil, fmt.Errorf("pgvector needs ALCHEMY_MCP_DSN; a DSN carries a password and " +
				"a password on the command line is a password in ps")
		}
		l, err := pgvector.Open(ctx, dsn, pgvector.Config{})
		if err != nil {
			return nil, nil, fmt.Errorf("pgvector: %w", err)
		}
		return l, l.Close, nil
	case "qdrant":
		l, err := qdrant.Open(ctx, uri, qdrant.Config{})
		if err != nil {
			return nil, nil, fmt.Errorf("qdrant: %w", err)
		}
		return l, func() {}, nil
	case "rdf":
		l, err := rdf.Open(ctx, rdf.Options{Endpoint: uri})
		if err != nil {
			return nil, nil, fmt.Errorf("rdf: %w", err)
		}
		return l, func() {}, nil
	}
	return nil, nil, fmt.Errorf("unknown -store %q; alchemy reads back from %s",
		store, strings.Join(stores, ", "))
}

func fail(msg string) {
	// stderr, never stdout: stdout is the protocol.
	fmt.Fprintln(os.Stderr, "alchemy-mcp: "+msg)
	os.Exit(1)
}
