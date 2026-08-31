// Package contributions folds the mentions a store found into the answer
// recall.Reader.Contributions returns.
//
// The queries stay each connector's own, because only a property graph has an
// opinion about excluding bookkeeping edges by name and only a triple store has
// one about an inclusion list over predicates. What is here is the part that is
// a rule rather than a query: what counts as ONE mention, what order the
// mentions come in, and which names reach Names. Those three decide whether two
// reads of one node produce the same document, and they have to be the same
// decision in every store or a buyer comparing two backends is comparing
// shuffles.
//
// Writing them once is the read side of the lesson pkg/sink is the write side
// of. Four connectors each invented edge identity, provenance handling and a
// content address, and every one of the four was defensible on its own; what
// made it a defect was that nothing said which was right. A dedup key copied
// into three files is three answers to one question the moment one of them is
// edited.
package contributions

import (
	"sort"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/recall"
)

// mention is what makes two records one contributor.
//
// It is the citation and the producer and deliberately not the name. A node's
// own record and an edge asserted from the same chunk of the same file by the
// same producer are one source having a hand in the node once; reporting them
// as two would make an entity described by a single sentence look like a join,
// which is the same false alarm as reporting no join at all, one field over.
//
// The name is excluded because only one kind of record carries one — see
// Assemble — so including it would split that single mention in two: the
// record that named the node and the edge that only pointed at it.
type mention struct {
	source   string
	chunk    int
	producer alchemy.Producer
}

// Assemble builds the answer for one node out of every mention of it a store
// could find, in the order recall.Contributions specifies.
//
// The mentions arrive in whatever order a query produced and may repeat; this
// is where they become a document. Callers pass the node's own record first
// where they have one, which matters only for the name: it is the record that
// named the node, and the merge below keeps the first non-empty Name it sees
// for one mention.
//
// Name is left alone otherwise, and that is the whole discipline of this
// package. A store that cannot say what a given source called the node must
// leave it empty rather than repeat the node's own name into every
// contributor — that would make every join read as unanimous, which is exactly
// the false confidence recall.Contributor exists to end.
//
// No mentions is a zero Contributions rather than an empty one, because that is
// the value the interface specifies for an id the load does not hold, and a
// caller comparing against recall.Contributions{} must not have to know whether
// this returned an empty slice or a nil one.
func Assemble(id, typ string, mentions []recall.Contributor) recall.Contributions {
	if len(mentions) == 0 {
		return recall.Contributions{}
	}
	at := make(map[mention]int, len(mentions))
	out := make([]recall.Contributor, 0, len(mentions))
	for _, m := range mentions {
		// Stated is alchemy.Producer.Deterministic and is computed here rather
		// than read from the store, for the reason recall.NewClaim gives about
		// the same field: both stores materialise the boolean so a buyer can
		// write "the half that was guessed" as their own WHERE clause, and the
		// stored value is the answer the rule gave on the day of the import. A
		// reader deciding today how far to trust a source should be told
		// today's answer.
		m.Stated = m.Producer.Deterministic()
		k := mention{m.Source, m.Chunk, m.Producer}
		i, seen := at[k]
		if !seen {
			at[k] = len(out)
			out = append(out, m)
			continue
		}
		if out[i].Name == "" {
			out[i].Name = m.Name
		}
	}
	// By source then chunk, as recall.Contributions says, and then by producer
	// so that one file read twice by two producers has an order at all. Without
	// the last key the sort is not total and two reads of one node can differ
	// in exactly the case the method is for: a file that was both parsed and
	// read by a model.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Chunk != b.Chunk {
			return a.Chunk < b.Chunk
		}
		return a.Producer < b.Producer
	})
	return recall.Contributions{ID: id, Type: typ, Names: names(out), Contributors: out}
}

// names is the distinct names the contributors used, sorted.
//
// An empty name is not a name and is left out. It means "this source referred
// to the node and this store does not record what it called it", which is a
// different fact from a source that called it nothing, and putting an empty
// string into the list would make a reader counting the names conclude the
// sources disagreed.
func names(cs []recall.Contributor) []string {
	seen := make(map[string]bool, len(cs))
	var out []string
	for _, c := range cs {
		if c.Name == "" || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}
