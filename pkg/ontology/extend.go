package ontology

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
)

// Extend declares the accepted proposals in a part of this ontology and
// returns the new document.
//
// It is a pure function over a document and takes no store, because §4 means
// nothing here holds an ontology between calls: the caller keeps the document,
// supplies it per job, and this hands back the next one. That is not a
// limitation working around a missing database — it is what lets a vocabulary
// be a file somebody reviews, versions and can roll back, rather than a row
// somebody changed.
//
// THE ID CHANGES, ALWAYS. An ontology that gained a type is a different
// vocabulary, and alchemy.Provenance.Ontology names it on every record: two
// different vocabularies under one id would leave a reader unable to tell what
// a graph was checked against, which is the whole of what that field is for.
// The version half is incremented when it is a number, and a document whose
// version is not one has to say what the new id should be rather than have this
// invent a convention for it.
//
// WHAT IT REFUSES, and this is the substantive rule:
//
// A relation proposal with no observed ends is refused. Load's own comment says
// an empty end is read as OPEN, so declaring one that way would let the type
// hold between anything — "the one direction a mistake must never move them",
// in that comment's words, and it would happen silently at the moment somebody
// pressed Accept. A proposal has empty ends when the end's own type was also
// undeclared, so the fix is an order rather than a flag: accept the entity
// types, run again, and the relation's ends are there because both are now
// declared. Two rounds, each of which is a thing a person can actually check.
//
// WHAT IT RECORDS. Each accepted type carries a sentence saying it came from a
// proposal, who accepted it and what was observed. A vocabulary is a claim
// about a corpus (§2.1) and a claim whose origin nobody wrote down is the kind
// this design spends most of its length refusing to ship.
func (o *Ontology) Extend(part Part, accept []alchemy.Proposal, by, newID string) (*Ontology, []string, error) {
	if strings.TrimSpace(by) == "" {
		return nil, nil, fmt.Errorf("ontology: extending it is a judgement about what a type means, and one nobody is named for cannot be argued with later")
	}
	v, err := o.Vocabulary(part)
	if err != nil {
		return nil, nil, err
	}
	if newID == "" {
		if newID, err = nextID(o.ID); err != nil {
			return nil, nil, err
		}
	}
	if err := checkID(newID); err != nil {
		return nil, nil, err
	}

	haveEntity := map[string]bool{}
	for _, e := range v.Entities {
		haveEntity[fold(e.Name)] = true
	}
	haveRelation := map[string]bool{}
	for _, r := range v.Relations {
		haveRelation[fold(r.Name)] = true
	}

	stamp := time.Now().UTC().Format("2006-01-02")
	var added []string
	for _, p := range accept {
		switch p.Kind {
		case alchemy.ProposalEntity:
			if haveEntity[fold(p.Type)] {
				continue
			}
			haveEntity[fold(p.Type)] = true
			v.Entities = append(v.Entities, EntityType{
				Name:        p.Type,
				Description: origin(p, by, stamp),
			})
			added = append(added, p.Type)
		case alchemy.ProposalRelation:
			if haveRelation[fold(p.Type)] {
				continue
			}
			if len(p.From) == 0 || len(p.To) == 0 {
				return nil, nil, fmt.Errorf(
					"ontology: the proposal for %q observed no %s end, because that end's own type is "+
						"not declared either; a relation declared with an open end holds between "+
						"anything, which is the one direction a rule must never widen by accident. "+
						"Accept the entity types first, run again, and this proposal will carry its ends",
					p.Type, missingEnd(p))
			}
			haveRelation[fold(p.Type)] = true
			v.Relations = append(v.Relations, RelationType{
				Name:        p.Type,
				From:        append([]string(nil), p.From...),
				To:          append([]string(nil), p.To...),
				Description: origin(p, by, stamp),
			})
			added = append(added, p.Type)
		case alchemy.ProposalRelationEnds:
			i, found := indexOfRelation(v.Relations, p.Type)
			if !found {
				return nil, nil, fmt.Errorf(
					"ontology: %q is proposed as a widening but this vocabulary does not declare it; "+
						"a widening changes a rule that already governs records and there is no such rule here",
					p.Type)
			}
			if len(p.From) == 0 && len(p.To) == 0 {
				return nil, nil, fmt.Errorf(
					"ontology: the widening proposed for %q observed no ends, because their own types "+
						"are not declared either; accept the entity types first and run again", p.Type)
			}
			r := v.Relations[i]
			// An end that is already open cannot be widened, and unioning the
			// observed types into it would NARROW it — from "anything" to the
			// handful this corpus happened to show. That is a different
			// decision, in the opposite direction, and making it as a side
			// effect of pressing Accept is exactly the silent movement of a
			// rule this whole path exists to prevent.
			if (len(r.From) == 0 && len(p.From) > 0) || (len(r.To) == 0 && len(p.To) > 0) {
				return nil, nil, fmt.Errorf(
					"ontology: %q already runs between any types on one end, so there is nothing to "+
						"widen; accepting this would narrow it to what one corpus happened to show, "+
						"which is a different decision and one nobody asked for here", p.Type)
			}
			widened := false
			if added := union(r.From, p.From); len(added) > len(r.From) {
				r.From, widened = added, true
			}
			if added := union(r.To, p.To); len(added) > len(r.To) {
				r.To, widened = added, true
			}
			if !widened {
				continue
			}
			r.Description = strings.TrimSpace(r.Description + " " + widening(v.Relations[i], p, by, stamp))
			v.Relations[i] = r
			added = append(added, p.Type)
		default:
			return nil, nil, fmt.Errorf("ontology: proposal for %q has no kind; it belongs in one of the vocabulary's lists and does not say which", p.Type)
		}
	}

	parts := make(map[Part]Vocabulary, len(o.parts))
	for name, vocab := range o.parts {
		parts[name] = vocab.clone()
	}
	parts[part] = v
	out := &Ontology{ID: newID, parts: parts}
	if err := out.check(); err != nil {
		return nil, nil, err
	}
	sort.Strings(added)
	return out, added, nil
}

// Document renders this ontology as the JSON a caller supplies to the next job.
//
// It exists because Extend's whole output is a document the caller keeps, and
// handing back a *Ontology whose parts are unexported would be handing back
// something they cannot write to a file.
func (o *Ontology) Document() ([]byte, error) {
	return json.MarshalIndent(wire{ID: o.ID, Parts: o.parts}, "", "  ")
}

// origin is the sentence an accepted type carries about where it came from.
//
// It states what was observed and never what the type means. "used 4 times,
// from Person, by human, per liliang" is a fact; "a person belongs to a team"
// is a judgement, and the judgement is the person's to write into the
// description afterwards if they want one.
func origin(p alchemy.Proposal, by, stamp string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "accepted from a proposal by %s on %s: used %d time(s)", by, stamp, p.Records)
	if len(p.From) > 0 || len(p.To) > 0 {
		fmt.Fprintf(&b, ", observed %s -> %s", strings.Join(p.From, "|"), strings.Join(p.To, "|"))
	}
	if len(p.Sources) > 0 {
		fmt.Fprintf(&b, ", stated by %s", strings.Join(p.Sources, ", "))
	}
	return b.String()
}

func missingEnd(p alchemy.Proposal) string {
	if len(p.From) == 0 {
		return "from"
	}
	return "to"
}

// nextID increments the version half of "name@version".
//
// A non-numeric version is refused rather than decorated. "sds@2026-08" is a
// scheme somebody chose, and appending or incrementing inside it would be this
// package inventing a convention for a document it does not own — so it asks
// for the new id instead, which is one field and no guessing.
func nextID(id string) (string, error) {
	name, version, ok := strings.Cut(strings.TrimSpace(id), "@")
	if !ok {
		return "", fmt.Errorf("ontology: %q has no version to increment (want \"name@version\")", id)
	}
	n, err := strconv.Atoi(version)
	if err != nil {
		return "", fmt.Errorf(
			"ontology: cannot derive the next id from %q because %q is not a number; say what the "+
				"new id should be, since a versioning scheme is the document's own and not this "+
				"package's to guess at", id, version)
	}
	return fmt.Sprintf("%s@%d", name, n+1), nil
}

// indexOfRelation finds a declared relation type by the same folded match
// every other lookup in this package uses.
func indexOfRelation(rels []RelationType, name string) (int, bool) {
	for i, r := range rels {
		if fold(r.Name) == fold(name) {
			return i, true
		}
	}
	return 0, false
}

// union appends what is not already there, preserving the declared order so a
// widened type reads as its original declaration plus what was added.
func union(have, add []string) []string {
	seen := make(map[string]bool, len(have))
	for _, s := range have {
		seen[fold(s)] = true
	}
	out := append([]string(nil), have...)
	for _, s := range add {
		if !seen[fold(s)] {
			seen[fold(s)] = true
			out = append(out, s)
		}
	}
	return out
}

// widening is the sentence a widened type carries.
//
// It states what the rule was before, which the type itself no longer shows.
// A vocabulary that quietly grew an end is one nobody can audit; the whole
// value of writing this down is that somebody reading the graph in six months
// can see that DEVELOPS did not always run to a Platform, and who decided it
// should.
func widening(before RelationType, p alchemy.Proposal, by, stamp string) string {
	return fmt.Sprintf("widened by %s on %s from %s -> %s, after %d record(s) used it %s -> %s.",
		by, stamp,
		strings.Join(before.From, "|"), strings.Join(before.To, "|"),
		p.Records, strings.Join(p.From, "|"), strings.Join(p.To, "|"))
}
