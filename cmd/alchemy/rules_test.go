package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The example a nightly pipeline carries: §6's own sentence, written down
// before any job has run.
const ruleSet = `{
  "rules": [
    {
      "shape": "violation/unknown_entity_type/type=Flag/producer=llm-extract/model=gemini-3.6-flash-high",
      "verb": "reject",
      "by": "ana@example.com",
      "because": "--verbose is a command-line switch, not an entity; our manuals are full of them",
      "at": "2026-08-01T09:00:00Z",
      "note": "revisit if the ontology ever gains a Flag type"
    }
  ]
}`

func writeRules(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRulesComeFromAFile(t *testing.T) {
	rules, err := readRules(settings{rulesFile: writeRules(t, ruleSet)})
	if err != nil {
		t.Fatalf("readRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want the one in the file", rules)
	}
	if !rules[0].Authored() || rules[0].From.By != "ana@example.com" {
		t.Fatalf("rule = %+v, want an authored rule naming its author", rules[0])
	}
}

// A rule file nobody named is no policy at all, which is the default and is
// not a failure.
func TestNoRuleFileIsNoPolicy(t *testing.T) {
	rules, err := readRules(settings{})
	if err != nil {
		t.Fatalf("readRules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("rules = %+v, want none", rules)
	}
}

// A malformed rule set stops the process from starting. The alternative is a
// server that comes up, reports itself healthy, and refuses the first job of
// the night hours later — by which time the operator who could have fixed the
// file has gone home, and the import that was supposed to run has not.
func TestAMalformedRuleFileRefusesAtStartup(t *testing.T) {
	for name, body := range map[string]string{
		"not json":                         `{"rules":[`,
		"no stated reason":                 `{"rules":[{"shape":"violation/unknown_entity_type/type=Flag/producer=llm-extract","verb":"reject","by":"ana","at":"2026-08-01T09:00:00Z"}]}`,
		"a conflict answered in advance":   `{"rules":[{"shape":"conflict/entity_type/between=ddl|llm-extract","verb":"always","by":"ana","because":"the schema wins","at":"2026-08-01T09:00:00Z"}]}`,
		"a field the format does not have": `{"rules":[{"shape":"violation/unknown_entity_type/type=Flag/producer=llm-extract","verb":"reject","by":"ana","reason":"a switch is not an entity","at":"2026-08-01T09:00:00Z"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeRules(t, body)
			if _, err := readRules(settings{rulesFile: path}); err == nil {
				t.Fatalf("readRules accepted a rule set with %s", name)
			}
			// The whole program refuses, not just the loader.
			err := run([]string{"-addr", "127.0.0.1:0", "-rules", path}, env(map[string]string{"ALCHEMY_TOKEN": "t"}), os.Stdout, os.Stderr)
			if err == nil {
				t.Fatalf("the server started with a rule set that has %s", name)
			}
			if !strings.Contains(err.Error(), "rule") {
				t.Fatalf("err = %q, want it to name the rule set as the reason", err)
			}
		})
	}
}

// A missing file named on the command line is a refusal, not a shrug. An
// operator who said -rules meant that file, and starting with no policy
// because it could not be read is the configuration mistake nobody notices
// until the graph is wrong.
func TestAnUnreadableRuleFileIsRefused(t *testing.T) {
	if _, err := readRules(settings{rulesFile: filepath.Join(t.TempDir(), "absent.json")}); err == nil {
		t.Fatal("readRules accepted a file it could not read")
	}
}

// The environment is the other way a container is configured, and the flag
// wins when both are given — the flag is on the command that started this
// process, where the environment may have come from an image.
func TestTheRuleFileCanComeFromTheEnvironment(t *testing.T) {
	path := writeRules(t, ruleSet)
	s, err := parseFlags(nil, env(map[string]string{"ALCHEMY_RULES": path}), os.Stderr)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if s.rulesFile != path {
		t.Fatalf("rulesFile = %q, want the one in the environment", s.rulesFile)
	}
	flagged := writeRules(t, ruleSet)
	s, err = parseFlags([]string{"-rules", flagged}, env(map[string]string{"ALCHEMY_RULES": path}), os.Stderr)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if s.rulesFile != flagged {
		t.Fatalf("rulesFile = %q, want the flag to win over the environment", s.rulesFile)
	}
}

// The startup line says how much policy is in force, because a rule set is a
// thing that changes what a graph contains and an operator reading one line
// should be able to see that it is there.
func TestTheStartupLineSaysHowManyRulesAreInForce(t *testing.T) {
	if got := startupLine(settings{}, "token"); !strings.Contains(got, "rules=off") {
		t.Errorf("startup line = %q, want it to spell out that there is no policy", got)
	}
	if got := startupLine(settings{rulesFile: "/etc/alchemy/rules.json", rules: 3}, "token"); !strings.Contains(got, "rules=3") {
		t.Errorf("startup line = %q, want the number of rules in force", got)
	}
}
