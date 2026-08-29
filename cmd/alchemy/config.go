package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// settings is everything an operator can change about this process. It is a
// struct rather than a set of package-level flag variables so that parsing is
// a function with inputs and outputs, and can therefore be tested without
// starting a server or touching the real environment.
type settings struct {
	addr string
	// httpAddr is where the REST/JSON gateway listens, and empty means it does
	// not. Off by default for the same reason addr is loopback: §6 makes gRPC
	// the service and the gateway a convenience for a buyer with curl and for
	// a browser, and a second listening port nobody asked for is a second
	// thing to secure and to watch.
	httpAddr  string
	spool     string
	store     string
	tokenFile string

	capacity   int
	sweepEvery time.Duration
	// modelConcurrency is §8.2's budget: how many calls may be in flight
	// against one model endpoint. Zero turns the budget off, which is the
	// single node with no declared endpoint limit that a buyer evaluating the
	// product runs.
	modelConcurrency int
	// extractCache is how many chunk extractions §8.2's cache holds. Zero or
	// negative is no cache; see extractCache in server.go.
	extractCache int
}

// envToken is where the credential is read from when no file is named.
const envToken = "ALCHEMY_TOKEN"

// errNoToken names both ways of supplying a token, because the operator
// reading it is being stopped from starting and needs the next step rather
// than the diagnosis. pkg/service refuses too; this is that refusal made
// actionable, and made before a port is bound.
var errNoToken = fmt.Errorf(
	"alchemy: no bearer token. Set %s, or point -token-file (%s) at a file containing one. "+
		"There is deliberately no default and no -token flag: a default is a credential in the source, "+
		"and a flag is one in the process table", envToken, envTokenFile)

// readToken resolves the bearer token, from a file if one was named and from
// the environment otherwise.
//
// getenv is a parameter rather than os.Getenv so that a test can state the
// environment it means instead of mutating the process's.
func readToken(s settings, getenv func(string) string) (string, error) {
	if s.tokenFile != "" {
		raw, err := os.ReadFile(s.tokenFile)
		if err != nil {
			// Deliberately not a fallback to the environment. An operator who
			// named a file meant that file, and starting on some other
			// credential because this one was unreadable is the configuration
			// mistake nobody notices until it matters.
			return "", fmt.Errorf("alchemy: reading -token-file: %w", err)
		}
		// Trimmed because a file written with `echo` ends in a newline, and a
		// token that differs from what the operator typed by one invisible
		// byte costs an afternoon.
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("alchemy: -token-file %q is empty; an empty token is authentication that accepts the empty string", s.tokenFile)
		}
		return token, nil
	}
	if token := strings.TrimSpace(getenv(envToken)); token != "" {
		return token, nil
	}
	return "", errNoToken
}

// storeMemory is the only job store that exists. §8.3 designs a Postgres one
// for a cluster and defers it; naming it here would be a promise the binary
// cannot keep.
const storeMemory = "memory"

// Defaults. The address is loopback rather than every interface because a
// service whose authentication is one environment variable away from being
// wrong should not be reachable from the network by default.
const (
	defaultAddr             = "127.0.0.1:7431"
	defaultCapacity         = 64
	defaultSweep            = time.Minute
	defaultModelConcurrency = 8
	// defaultExtractCache is on rather than off, which is the one default in
	// this file that costs memory by default and is still the right way round.
	// §8.2 calls paying twice for the identical call after a crash a bug, and a
	// bug that has to be configured away is a bug the first buyer meets. The
	// number is entries, not bytes, because that is what the LRU counts: a few
	// thousand chunks is a large document's worth of extractions and tens of
	// megabytes, which is cheap next to the model calls it stops.
	defaultExtractCache = 4096
)

// parseFlags reads the settings from the command line, falling back to the
// environment for anything the command line did not say.
//
// Both, rather than one: a container is configured by environment and a
// systemd unit by arguments, and an operator should not have to adopt the
// deployment style this binary happens to prefer. The flag wins when both are
// given, because the flag is on the command that started this process where
// the environment may have come from an image somebody else built.
//
// getenv and out are parameters so the whole of this is testable without
// mutating the process's environment or printing to a real terminal.
func parseFlags(args []string, getenv func(string) string, out io.Writer) (settings, error) {
	var s settings
	fs := flag.NewFlagSet("alchemy", flag.ContinueOnError)
	fs.SetOutput(out)

	fs.StringVar(&s.addr, "addr", envString(getenv, "ALCHEMY_ADDR", defaultAddr), "address to listen on")
	fs.StringVar(&s.httpAddr, "http-addr", envString(getenv, "ALCHEMY_HTTP_ADDR", ""), "address the REST/JSON gateway listens on (empty: no gateway)")
	fs.StringVar(&s.spool, "spool", getenv("ALCHEMY_SPOOL"), "directory uploaded sources are spooled to (empty: a temporary directory)")
	fs.StringVar(&s.store, "store", envString(getenv, "ALCHEMY_STORE", storeMemory), "job store: only \"memory\" exists")
	// Note what is missing: there is no -token. See readToken.
	fs.StringVar(&s.tokenFile, "token-file", getenv(envTokenFile), "file containing the bearer token")

	capacity := fs.Int("capacity", 0, "how many live jobs to hold before refusing more")
	sweep := fs.Duration("sweep", 0, "how often expired work is swept")
	concurrency := fs.Int("model-concurrency", -1, "calls in flight per model endpoint (0 turns the budget off)")
	// -1 is "the operator said nothing", so that 0 can mean what it says. A
	// zero default would make this flag able to enlarge the cache and never to
	// remove it, which is the half of the setting an operator reaches for when
	// they are trying to reproduce a run.
	extract := fs.Int("extract-cache", -1, "chunk extractions to cache (0 turns the cache off)")

	if err := fs.Parse(args); err != nil {
		return settings{}, err
	}

	var err error
	if s.capacity, err = pick(*capacity, 0, getenv, "ALCHEMY_CAPACITY", defaultCapacity, atoi); err != nil {
		return settings{}, err
	}
	if s.modelConcurrency, err = pick(*concurrency, -1, getenv, "ALCHEMY_MODEL_CONCURRENCY", defaultModelConcurrency, atoi); err != nil {
		return settings{}, err
	}
	if s.extractCache, err = pick(*extract, -1, getenv, "ALCHEMY_EXTRACT_CACHE", defaultExtractCache, atoi); err != nil {
		return settings{}, err
	}
	if s.sweepEvery, err = pick(*sweep, 0, getenv, "ALCHEMY_SWEEP", defaultSweep, time.ParseDuration); err != nil {
		return settings{}, err
	}

	if s.store != storeMemory {
		// Refused rather than ignored: an operator who asked for a shared
		// store and silently got a private one has a cluster whose nodes each
		// believe they are alone, which is §8.1's failure arriving through a
		// configuration file.
		return settings{}, fmt.Errorf("alchemy: unknown -store %q; only %q exists (§8.3 designs a Postgres store for a cluster and defers it)", s.store, storeMemory)
	}
	return s, nil
}

// pick resolves one setting from the flag, then the environment, then the
// default. unset is the flag's own zero value, which is what "the operator did
// not say" looks like for that flag.
//
// A malformed environment value is an error rather than a fallback to the
// default. ALCHEMY_CAPACITY=lots silently meaning 64 is the class of mistake
// that is only discovered by the queue behaving differently than the file that
// configured it says.
func pick[T comparable](flagged, unset T, getenv func(string) string, key string, fallback T, parse func(string) (T, error)) (T, error) {
	if flagged != unset {
		return flagged, nil
	}
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := parse(raw)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("alchemy: %s=%q: %w", key, raw, err)
	}
	return v, nil
}

func envString(getenv func(string) string, key, fallback string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return fallback
}

func atoi(s string) (int, error) { return strconv.Atoi(s) }

const envTokenFile = "ALCHEMY_TOKEN_FILE"
