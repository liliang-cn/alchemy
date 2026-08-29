// Command alchemy is the server of DESIGN.md §6: the gRPC service, with
// pkg/pipeline behind it through pkg/runner.
//
// It is deliberately thin. Everything with a decision in it — how a setting is
// resolved, where the token comes from, what is safe to log, how the pieces
// are assembled, and what a graceful stop is — lives in this package's other
// files and is tested there. What is left here is the part a test cannot have:
// the real process's arguments, environment, signals and exit code.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, stdout, stderr *os.File) error {
	s, err := parseFlags(args, getenv, stderr)
	if err != nil {
		return err
	}
	token, err := readToken(s, getenv)
	if err != nil {
		return err
	}
	sv, err := build(s, token)
	if err != nil {
		return err
	}
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("alchemy: listening on %s: %w", s.addr, err)
	}

	// SIGINT and SIGTERM become a cancelled context, which is the only thing
	// the rest of the program knows about shutting down. NotifyContext also
	// restores the default handler after the first signal, so a second one
	// kills a process that is taking too long to drain — which is what an
	// operator pressing Ctrl-C twice means.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Fprintln(stdout, startupLine(s, token))
	return sv.serve(ctx, lis)
}
