package preflight

import (
	"fmt"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// counts compares what the result claims about itself with what the slices
// beside the claim say.
//
// §5 makes this block the obligation that justifies the ambitious scope:
// "every returned graph is accompanied by the numbers needed to distrust it".
// It arrived as a claim no consumer could test, and all four stores wrote it
// down verbatim because there was nothing else to do with it; one of them wrote
// its own tally next to it in the same node, with a comment saying the two
// differ and the difference is what a buyer would otherwise have to find by
// counting. That is the honest response to a contract that offers no way to
// check, and this is the check.
//
// Eleven fields are compared and two are deliberately not. ChunksEmpty counts
// chunks that produced nothing and Dropped counts records a standing rule
// removed before any queue was shown, and both are what alchemy.Counts says
// they are: numbers that cannot be recomputed, because the thing they count
// left nothing behind. A checker that compared them would report a defect on
// every honest result and would be switched off within a week.
//
// One defect per disagreeing field rather than one for the block, so a caller
// can tell "the guess count is stale" from "this is not the graph these numbers
// describe".
func counts(res alchemy.Result) []Defect {
	claimed, derived := res.Counts, res.Derivable()
	// Fixed order, because a caller diffing two runs of one job over one result
	// must get one list. The names are the JSON ones, so a defect a person
	// reads points at a field they can find in the document.
	fields := []struct {
		name             string
		claimed, derived int
	}{
		{"entities", claimed.Entities, derived.Entities},
		{"relations", claimed.Relations, derived.Relations},
		{"chunks", claimed.Chunks, derived.Chunks},
		{"vectors", claimed.Vectors, derived.Vectors},
		{"deterministic", claimed.Deterministic, derived.Deterministic},
		{"inferred", claimed.Inferred, derived.Inferred},
		{"violations", claimed.Violations, derived.Violations},
		{"conflicts", claimed.Conflicts, derived.Conflicts},
		{"guesses", claimed.Guesses, derived.Guesses},
		{"duplicates", claimed.Duplicates, derived.Duplicates},
		{"chunks_unread", claimed.ChunksUnread, derived.ChunksUnread},
	}
	var out []Defect
	for _, f := range fields {
		if f.claimed == f.derived {
			continue
		}
		out = append(out, Defect{
			Kind: CountsDisagree, Severity: SeverityReport, Subject: f.name,
			Detail: fmt.Sprintf("counts.%s says %d and the result holds %d; §5's numbers are what a reader distrusts the graph with, so one of the two is describing a different graph",
				f.name, f.claimed, f.derived),
		})
	}
	return out
}

// ruleNames checks that the policy every record points at is in the result
// that carries the record.
//
// §5c is explicit that Provenance.RuleSet "is the set's *name* … and not the
// set itself; the contents are on the result once, in Result.RuleSets, and the
// name is how a record points at them." Nothing forced a writer to carry the
// sets and nothing checked that a name resolved, and the predictable thing
// happened: one store shipped without carrying them until a test caught it, and
// another's graph still writes rule-set names into a store that contains no
// rule sets at all. Every record in it says it was proposed under a policy, and
// the policy is nowhere.
//
// A report rather than a refusal. The graph is correct and every edge is still
// attributable to its source, its chunk and its producer; what is lost is the
// answer to "what had the model been told when it proposed this", which is a
// thing a reader is owed and not a thing that corrupts a store.
//
// Two names are checked because they are two claims. RuleSet says which rules
// were in the room and RuledBy says which one moved, and a reader who can
// resolve the first and not the second has a record that came back retyped by
// nobody in particular.
func ruleNames(res alchemy.Result) []Defect {
	sets := make(map[string]bool, len(res.RuleSets))
	rules := map[string]bool{}
	for _, s := range res.RuleSets {
		sets[s.Name] = true
		for _, r := range s.Rules {
			rules[r.Name] = true
		}
	}
	var out []Defect
	saidSet, saidRule := map[string]bool{}, map[string]bool{}
	visit := func(subject string, p alchemy.Provenance) {
		if p.RuleSet != "" && !sets[p.RuleSet] && !saidSet[p.RuleSet] {
			saidSet[p.RuleSet] = true
			out = append(out, Defect{
				Kind: RuleSetUnresolved, Severity: SeverityReport, Subject: p.RuleSet,
				Detail: fmt.Sprintf("%q was proposed under rule set %q and this result declares no such set; the name is how a record points at the policy it was asked under, and here it points at nothing",
					subject, p.RuleSet),
			})
		}
		if p.RuledBy != "" && !rules[p.RuledBy] && !saidRule[p.RuledBy] {
			saidRule[p.RuledBy] = true
			out = append(out, Defect{
				Kind: RuleUnresolved, Severity: SeverityReport, Subject: p.RuledBy,
				Detail: fmt.Sprintf("%q was acted on by rule %q and no rule of that name is in this result's sets; a graph that came back retyped should be able to say which rule did it, and by whose word",
					subject, p.RuledBy),
			})
		}
	}
	for _, e := range res.Entities {
		visit(e.ID, e.Provenance)
	}
	for _, r := range res.Relations {
		visit(fmt.Sprintf("%s -[%s]-> %s", r.From, r.Type, r.To), r.Provenance)
	}
	for _, v := range res.Violations {
		visit(v.Subject, v.Provenance)
	}
	return out
}

// attributes checks that every attribute value is in the domain
// Entity.Attributes declares — the JSON one, because §4 makes the JSON the
// contract.
//
// It is the one defect that is invisible from inside the process that made it.
// A Go value outside the domain — a time.Time, a struct, an int — behaves
// perfectly in memory and changes type on the way to any consumer, so the two
// halves of one system disagree about what the graph says and neither can see
// why. The four stores each met this from a different direction: two hold JSON
// natively and would have written something a reader could not compare against
// the source, and two flatten to a property model and had to invent a
// convention for what to do with a value they cannot hold.
//
// What a store does with a nested value is that store's business, and this
// makes no claim about it; the contract owes the store a declared domain, and
// that is what is checked. Nested objects and arrays are in it at any depth,
// because that is exactly what a model's JSON reply produces and refusing them
// would refuse the extractor's ordinary output.
//
// A report rather than a refusal. The value can be written — every store has
// some rendering for it — and what is wrong is that no two stores will render
// it the same way, which is a fact about the graph rather than a loss inside
// one store.
func attributes(res alchemy.Result) []Defect {
	var out []Defect
	said := map[string]bool{}
	visit := func(subject string, attrs map[string]any) {
		// Sorted iteration is not needed and would cost a slice per record on
		// §8's four-hundred-thousand-record import: a clean result reports
		// nothing at all, and a result with a defect reports one line per
		// distinct key-and-type, which is a set rather than a sequence.
		for k, v := range attrs {
			bad := outsideJSON(v)
			if bad == "" {
				continue
			}
			key := k + "\x00" + bad
			if said[key] {
				continue
			}
			said[key] = true
			out = append(out, Defect{
				Kind: AttributeType, Severity: SeverityReport, Subject: k,
				Detail: fmt.Sprintf("%q has attribute %q of Go type %s, which is outside the JSON value domain alchemy.Entity.Attributes declares; it round-trips inside this process and changes type on the way to every consumer, so no two stores hold the same graph",
					subject, k, bad),
			})
		}
	}
	for _, e := range res.Entities {
		visit(e.ID, e.Attributes)
	}
	for _, r := range res.Relations {
		visit(fmt.Sprintf("%s -[%s]-> %s", r.From, r.Type, r.To), r.Attributes)
	}
	return out
}

// outsideJSON names the Go type of a value that is not in the declared domain,
// or returns "" when it is.
//
// The domain is what encoding/json produces when it decodes into an any:
// string, float64, bool, nil, []any and map[string]any. Nothing else is
// admitted — not int, not float32, not a named string type — and the strictness
// is the point. A Go int survives a marshal and comes back a float64, so a
// producer that writes one has made a graph that is one thing here and another
// thing everywhere else, which is precisely the defect this is looking for. A
// producer that means a number should write the float64 it will become.
//
// It recurses, because a struct three levels down a map is the same problem as
// one at the top and neither is visible from the type of the outer value.
func outsideJSON(v any) string {
	switch t := v.(type) {
	case nil, string, float64, bool:
		return ""
	case []any:
		for _, e := range t {
			if bad := outsideJSON(e); bad != "" {
				return bad
			}
		}
		return ""
	case map[string]any:
		for _, e := range t {
			if bad := outsideJSON(e); bad != "" {
				return bad
			}
		}
		return ""
	default:
		return fmt.Sprintf("%T", v)
	}
}

// assertions checks the one obligation alchemy.ProducerHuman carries: a person
// asserted this, so the record must say which person and when.
//
// Only that producer is checked. Every other one names a document in Source
// and there is nobody to name beyond it — a foreign key is not asserted by
// anybody, it is what a schema says — so demanding a signature from them would
// be demanding a fact that does not exist. This is the check that keeps
// "a named person asserted it" from degrading into "somebody typed it".
func assertions(res alchemy.Result) []Defect {
	var out []Defect
	report := func(subject, what string) {
		out = append(out, Defect{
			Kind:     AssertionUnsigned,
			Severity: SeverityReport,
			Subject:  subject,
			Detail: fmt.Sprintf("producer is %q and %s; an assertion nobody is named for "+
				"cannot be asked about, which is the whole of what made it admissible",
				alchemy.ProducerHuman, what),
		})
	}
	missing := func(p alchemy.Provenance) string {
		switch {
		case p.By == "" && p.At == "":
			return "neither a person nor a date is recorded"
		case p.By == "":
			return "no person is named"
		case p.At == "":
			return "no date is recorded"
		}
		return ""
	}
	for _, e := range res.Entities {
		if e.Provenance.Producer != alchemy.ProducerHuman {
			continue
		}
		if what := missing(e.Provenance); what != "" {
			report(e.ID, what)
		}
	}
	for _, r := range res.Relations {
		if r.Provenance.Producer != alchemy.ProducerHuman {
			continue
		}
		if what := missing(r.Provenance); what != "" {
			report(r.Identity(), what)
		}
	}
	return out
}
