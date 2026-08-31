package neo4j

import (
	"context"
	"testing"
)

// A walk can continue without going back through the anchor search.
//
// Measured on the question this interface exists for. An agent asked which
// products a team's people work on made thirteen tool calls against a graph
// loaded here, and eight were four Find/Claims pairs: Claims returned four
// neighbours by name, Claims takes an ID, so every second hop was spent turning
// a name it had just been given back into the identifier it had just been
// denied. Find is a substring search over a page that can be truncated, so that
// round trip is not merely wasteful -- it is where a walk can silently continue
// from the wrong node, or from none.
func TestAClaimCarriesTheIDTheNextHopTakes(t *testing.T) {
	l := liveLoader(t, Options{RunID: "recall-walk"})
	ctx := context.Background()
	const load = "recall-walk"
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122)); err != nil {
		t.Fatalf("load: %v", err)
	}

	claims, err := l.Claims(ctx, load, "e3")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if len(claims) == 0 {
		t.Fatal("no claims about e3; this fixture is meant to hold one")
	}
	var next string
	for _, c := range claims {
		// The end that is not the node asked about is the next hop.
		if c.FromID == "e3" {
			next = c.ToID
		} else {
			next = c.FromID
		}
		if next == "" {
			t.Fatalf("the claim %q carries no ID for its far end, so a walk stops here: %+v", c, c)
		}
		// The names are still what the claim reads as. A store that filled the
		// ID fields by putting identifiers in the name fields would pass the
		// check above and put "e1 -[USES]-> e2" in front of a person.
		if c.From == c.FromID && c.To == c.ToID {
			t.Errorf("both ends read as their own IDs (%+v); the names are what a claim is weighed in", c)
		}
	}

	// And the ID is the argument Claims itself takes -- which is the whole
	// point, and is not implied by it merely being non-empty.
	onward, err := l.Claims(ctx, load, next)
	if err != nil {
		t.Fatalf("Claims(%q) from an ID the previous hop returned: %v", next, err)
	}
	if len(onward) == 0 {
		t.Errorf("Claims(%q) returned nothing; the second hop reached a node with no claims, "+
			"so the ID did not name what the first hop said it did", next)
	}
}
