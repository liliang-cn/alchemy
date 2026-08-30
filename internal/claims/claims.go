// Package claims turns the measurable sentences in this repository's
// documentation into checks that fail.
//
// DESIGN.md said "three dependencies (a PDF reader, gRPC, protobuf)" for
// several weeks after go.mod had six. The sentence was true when it was
// written; the Postgres stores landed; nobody reread it. It was repeated in
// conversation more than once as a fact about the product and was found in the
// end by somebody hashing go.sum for an unrelated reason. That is the ordinary
// way a document rots — not by being wrong, but by having been right.
//
// There are two kinds of check here and the difference is where the expected
// value comes from.
//
// A LIVE check computes the number from the repository as it is now. Its
// expected value is the documentation's own claim, written into the test
// beside the sentence it comes from, so the test reads as the sentence and a
// failure names the file to correct. Nothing can go stale: the measurement
// happens on every run.
//
// A PINNED check reads a number the build cannot reproduce — one that came out
// of a real corpus, real model calls and a real deployment — from a file in
// docs/claims/ that the run which produced it wrote. Those files carry their
// own provenance, and it defaults to untrusted, so a file left behind by an
// older run is never mistaken for a fresh measurement. A missing file skips
// with the command that would make it; a file that will not say where its
// numbers came from fails.
//
// The checks live in a test package on purpose. `go test ./...` is this
// repository's gate, and a number checked by a separate optional target is a
// number that drifts.
package claims

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Root is the directory holding go.mod, found by walking up from the caller's
// working directory. Tests run in their own package's directory, so every path
// in this package is relative to what this returns rather than to the process.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// DirectDependencies is the module paths go.mod requires without the
// `// indirect` marker.
//
// Parsed here rather than through golang.org/x/mod, which would be a
// dependency added in order to count dependencies. The format being read is
// the one `go mod tidy` writes, and a line it cannot parse is returned as an
// error rather than skipped: a silent skip would undercount, which is the
// direction this check exists to catch.
func DirectDependencies(root string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	var direct []string
	inBlock := false
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "require (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "require ") && !inBlock:
			line = strings.TrimPrefix(line, "require ")
		case !inBlock:
			continue
		}
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.Contains(line, "// indirect") {
			continue
		}
		path, _, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("go.mod line %q is not `path version`", raw)
		}
		direct = append(direct, path)
	}
	return direct, nil
}

// PackagesUnder is the import paths below dir that hold non-test Go source.
//
// Test-only directories are excluded because a package with no source is not
// something the product ships, and generated protobuf output is excluded
// because it is one `make generate` away from not being in the tree at all.
func PackagesUnder(root, dir string) ([]string, error) {
	var pkgs []string
	base := filepath.Join(root, dir)
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if strings.HasSuffix(name, ".pb.go") || strings.HasSuffix(name, ".pb.gw.go") {
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			pkgs = append(pkgs, rel)
			return nil
		}
		return nil
	})
	return pkgs, err
}

// SourceFiles walks the repository for hand-written Go, returning each file's
// path relative to root and its line count.
//
// Generated output is excluded: its size is protoc's decision and holding it
// to a limit meant for code somebody maintains would be a check nobody can act
// on. So is .claude/, which holds agent worktrees rather than this module.
func SourceFiles(root string) (map[string]int, error) {
	files := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == ".claude" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, ".pb.go") || strings.HasSuffix(name, ".pb.gw.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[rel] = strings.Count(string(b), "\n")
		return nil
	})
	return files, err
}

// TestFunctions counts declarations of the form `func TestXxx(` in the _test.go
// files below the given directories.
//
// Counted by prefix rather than by parsing, because what is being checked is a
// claim about how much test there is, and a claim that coarse deserves a
// measurement no more precise than itself. Subtests are not counted: a table
// with forty cases is one thing somebody wrote.
func TestFunctions(root string, dirs ...string) (int, error) {
	n := 0
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "func Test") && strings.Contains(line, "(") {
					n++
				}
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return n, nil
}

// Provenance says where a pinned file's numbers came from, and the zero value
// is the untrusted one.
//
// A generated file that does not say is not treated as a measurement. That is
// the whole of the rule and it exists because the alternative — assuming a
// file on disk is fresh — is how a number measured against last month's code
// goes on being cited as though it were about this month's.
type Provenance string

const (
	// Measured: produced by running this code against a real corpus, by the
	// command named in the file's How field.
	Measured Provenance = "measured"
	// Inherited: measured somewhere else, about something else, and quoted
	// here because it is the argument for a design decision. It may never
	// stand as evidence about what alchemy does.
	Inherited Provenance = "inherited"
)

// Claim is one pinned measurement: the sentence it backs, where that sentence
// is written, where the numbers came from, and the numbers.
type Claim struct {
	// Claim is the documented sentence, verbatim, so a failure reads as the
	// English somebody has to go and correct rather than as a key path.
	Claim string `json:"claim"`
	// Where names the file and section the sentence is in.
	Where string `json:"where"`
	// Provenance is checked before any value in this file is used.
	Provenance Provenance `json:"provenance"`
	// MeasuredAt is the date of the run, absolute rather than relative,
	// because a file is read long after the conversation that produced it.
	MeasuredAt string `json:"measured_at"`
	// How is the command that reproduces this file. A pinned number whose
	// origin is a transcript is a number nobody can check.
	How string `json:"how"`
	// Source names the system measured, for an inherited claim. Empty means
	// this repository.
	Source string `json:"source,omitempty"`
	// Values are the numbers, keyed by short names the checks refer to.
	Values map[string]float64 `json:"values"`
}

// ErrNoClaimFile means the measurement has not been made in this checkout. A
// check that gets this skips; it does not pass.
type ErrNoClaimFile struct{ Path, How string }

func (e ErrNoClaimFile) Error() string {
	return fmt.Sprintf("%s is not in this checkout; produce it with: %s", e.Path, e.How)
}

// Load reads one pinned claim and refuses it unless it says where it came
// from. how is the command a caller should be told to run when the file is
// absent, which the file itself cannot tell them.
func Load(root, name, how string) (Claim, error) {
	path := filepath.Join("docs", "claims", name)
	b, err := os.ReadFile(filepath.Join(root, path))
	if os.IsNotExist(err) {
		return Claim{}, ErrNoClaimFile{Path: path, How: how}
	}
	if err != nil {
		return Claim{}, err
	}
	var c Claim
	if err := json.Unmarshal(b, &c); err != nil {
		return Claim{}, fmt.Errorf("%s: %w", path, err)
	}
	switch c.Provenance {
	case Measured, Inherited:
	default:
		return Claim{}, fmt.Errorf(
			"%s declares provenance %q; only %q and %q may back a documented claim, and a "+
				"file that does not say where its numbers came from is not a measurement",
			path, c.Provenance, Measured, Inherited)
	}
	if c.How == "" {
		return Claim{}, fmt.Errorf("%s does not say what command produced it", path)
	}
	return c, nil
}

// Value returns one number, naming the file and the claim when it is absent so
// the failure says which measurement is missing rather than which key is.
func (c Claim) Value(key string) (float64, error) {
	v, ok := c.Values[key]
	if !ok {
		return 0, fmt.Errorf("the claim %q carries no value %q; it has %v", c.Claim, key, keys(c.Values))
	}
	return v, nil
}

func keys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
