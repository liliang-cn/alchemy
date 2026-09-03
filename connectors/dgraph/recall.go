package dgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/liliang-cn/alchemy/pkg/recall"
)

// Reading a load back out of Dgraph.
//
// Every primitive lands on something the alpha indexes, and the schema in
// schema.go exists for exactly this list — an index that no read below needs is
// not in it, and a read below that had no index would answer nothing with no
// error, which is this store's signature failure.
//
//	Find          regexp(name, /needle/i) over a trigram index
//	Types         @groupby(etype) over the load
//	OfType        eq(etype) — exact, because a type is declared by an ontology
//	Describe      eq(xid) — one node, whole
//	Claims        the node's outgoing and incoming rel_ predicates, with facets
//	Contributions the node's own provenance plus every edge's
//	Cite          eq(source) and eq(idx), both halves of the marker
//	Unanswered    the duplicate nodes this load wrote
var _ recall.Reader = (*Loader)(nil)

// finished reports whether a load is present AND complete.
//
// Present is not enough. A load that died halfway has its nodes and a marker
// saying complete=false, and answering from it would report a partial graph as
// a whole one — the confident wrong answer this whole design is arranged
// against.
func (l *Loader) finished(ctx context.Context, load string) (bool, error) {
	if load == "" {
		return false, nil
	}
	m, err := l.readMarker(ctx, load)
	if err != nil {
		return false, err
	}
	return m != nil && m.Complete, nil
}

func noLoad(load string) error {
	return fmt.Errorf("%w: %q is not a finished load in this cluster; "+
		"a load that is still arriving answers nothing, and a corpus imported twice is two loads",
		recall.ErrNoLoad, load)
}

// scope is the filter every read starts from: one load, one kind of node.
//
// Both halves are needed and neither is redundant. Without the run, a read
// would answer from every import in the cluster; without the kind, it would
// count this connector's own chunk and marker nodes as entities and report a
// vocabulary no ontology declares.
func (l *Loader) scope(load, kind string) string {
	return "eq(" + l.pred(keyRun) + ", " + literal(load) + ")) " +
		"@filter(eq(" + l.pred(keyKind) + ", " + literal(kind) + ")"
}

// entityFields is what a read asks for when it wants an anchor.
func (l *Loader) entityFields() string {
	return "  xid: " + l.pred(keyXID) + "\n" +
		"  name: " + l.pred(keyName) + "\n" +
		"  etype: " + l.pred(keyType) + "\n"
}

// node is one row of an entity read.
type node struct {
	XID   string `json:"xid"`
	Name  string `json:"name"`
	EType string `json:"etype"`
}

// id recovers the alchemy id from the xid.
//
// The xid is "alchemy:<run>:<id>" and an id may itself contain a colon, so the
// split is from the left and bounded, never strings.Split on every colon. An id
// like "table:public.users" is ordinary in a DDL import and cutting it at the
// wrong colon would return an anchor the caller cannot walk from.
func id(xid, run string) string {
	return strings.TrimPrefix(xid, "alchemy:"+run+":")
}

func (l *Loader) anchor(n node, load string) recall.Node {
	return recall.Node{ID: id(n.XID, load), Type: n.EType, Name: n.Name}
}

// page sorts and cuts, keeping the interface's ordering promise.
//
// By name and then by id, so a limit cuts the same place twice: two equal names
// are ordered by something that cannot be equal, and without the second key a
// page would shuffle between two identical calls.
func page(hits []recall.Node, limit int) recall.Found {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Name != hits[j].Name {
			return hits[i].Name < hits[j].Name
		}
		return hits[i].ID < hits[j].ID
	})
	total := len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if hits == nil {
		hits = []recall.Node{}
	}
	return recall.Found{Nodes: hits, Total: total}
}

// Find returns the entities of one load whose name contains name.
//
// regexp() and not anyoftext(): the interface promises a case-insensitive
// SUBSTRING, and Dgraph's full-text match is tokens. A search for "node_conn"
// has to find "node_connections", which token matching does not — and a search
// for "ada" must not match "Adam" by stemming, which it might.
//
// The needle is quoted. A regexp is code, and a name typed by a person is data:
// an entity called "C++ (2011)" searched for verbatim is an unbalanced group
// that Dgraph refuses, and one containing ".*" would match the whole load.
func (l *Loader) Find(ctx context.Context, load, name string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("dgraph: limit = %d is not a number of anchors", limit)
	}
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return recall.Found{Nodes: []recall.Node{}}, err
	}
	filter := ""
	if name != "" {
		filter = " AND regexp(" + l.pred(keyName) + ", /" + quoteRegexp(name) + "/i)"
	}
	q := "{ q(func: " + l.scope(load, kindEntity) + filter + ") {\n" + l.entityFields() + "} }\n"
	var out struct {
		Q []node `json:"q"`
	}
	if err := l.queryInto(ctx, q, &out); err != nil {
		return recall.Found{}, fmt.Errorf("dgraph: find %q in load %q: %w", name, load, err)
	}
	hits := make([]recall.Node, 0, len(out.Q))
	for _, n := range out.Q {
		hits = append(hits, l.anchor(n, load))
	}
	return page(hits, limit), nil
}

// quoteRegexp escapes what a regular expression reads as syntax.
//
// The needle comes from whoever typed the question and is meant literally. Two
// things go wrong without this and they go wrong differently: an unbalanced
// bracket in a name — "C++ (2011)" — is refused and the caller sees an error
// about their own data, while a "." or a ".*" is accepted and quietly matches
// far more than they asked for. The second is the one worth the escaping.
func quoteRegexp(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$/`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Types is the vocabulary of one load: every entity type and how many carry it.
//
// @groupby, which is the aggregation the other stores mostly do not have — the
// qdrant and cortexdb connectors both scan and count in Go. Here the alpha
// does it, which is the difference between moving a load's worth of names
// across the wire and moving one row per type.
func (l *Loader) Types(ctx context.Context, load string) ([]recall.TypeCount, error) {
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return nil, err
	}
	q := "{ q(func: " + l.scope(load, kindEntity) + ") @groupby(" + l.pred(keyType) + ") { count(uid) } }\n"
	data, err := l.query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("dgraph: types of load %q: %w", load, err)
	}
	// The predicate name is the JSON key, and it carries this Loader's prefix,
	// so the row cannot be a struct with a tag. Decoding generically and
	// picking the one key that is not "count" is what keeps the prefix
	// configurable.
	var out struct {
		Q []struct {
			Groupby []map[string]json.RawMessage `json:"@groupby"`
		} `json:"q"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("dgraph: types of load %q: decoding the groups: %w", load, err)
	}
	var res []recall.TypeCount
	for _, q := range out.Q {
		for _, g := range q.Groupby {
			var tc recall.TypeCount
			for k, v := range g {
				if k == "count" {
					_ = json.Unmarshal(v, &tc.Count)
					continue
				}
				_ = json.Unmarshal(v, &tc.Type)
			}
			res = append(res, tc)
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Type < res[j].Type })
	return res, nil
}

// OfType returns the entities of one load of exactly this type.
//
// Exactly, and the contrast with Find is deliberate: Find takes what somebody
// typed and folds case, this takes what Types returned. A type is declared by
// an ontology, so "Person" and "person" are two types and matching them
// together would report a vocabulary the load does not have.
func (l *Loader) OfType(ctx context.Context, load, typ string, limit int) (recall.Found, error) {
	if limit <= 0 {
		return recall.Found{}, fmt.Errorf("dgraph: limit = %d is not a number of entities", limit)
	}
	ok, err := l.finished(ctx, load)
	if err != nil || !ok {
		return recall.Found{Nodes: []recall.Node{}}, err
	}
	q := "{ q(func: " + l.scope(load, kindEntity) +
		" AND eq(" + l.pred(keyType) + ", " + literal(typ) + ")) {\n" + l.entityFields() + "} }\n"
	var out struct {
		Q []node `json:"q"`
	}
	if err := l.queryInto(ctx, q, &out); err != nil {
		return recall.Found{}, fmt.Errorf("dgraph: entities of type %q in load %q: %w", typ, load, err)
	}
	hits := make([]recall.Node, 0, len(out.Q))
	for _, n := range out.Q {
		hits = append(hits, l.anchor(n, load))
	}
	return page(hits, limit), nil
}
