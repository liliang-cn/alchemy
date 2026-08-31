package pgvector

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liliang-cn/alchemy/pkg/recall"
)

// A claim that never had a chunk is not a citation that failed, and Cite has to
// say which.
//
// Mark renders a chunkless claim as [source] with no #n, so a caller holding
// one has no number to pass and passes the claim's own Chunk, which is -1. Cite
// is reached there legitimately. Until it had a third answer it reported that
// as ErrNoCitation — the sentence reserved for evidence that does not check
// out — and the cost was measured: across thirty runs of an agent over a graph
// loaded here, seven of thirteen citation attempts were against a chunkless
// source, and all seven were refused as unverifiable. Every one was a false
// alarm. §5b ranks a machine reading something that already asserted a fact
// above a model reading prose, so those were the store's most trustworthy
// records, and the tool was teaching its caller to discount them.
func TestAClaimThatNeverHadAChunkIsNotACitationThatFailed(t *testing.T) {
	f := newFixture(t)
	l := f.open(t, Config{})
	ctx := context.Background()
	const load = "recall-nochunk"
	if _, err := l.Load(ctx, pack("SuperAI uses CortexDB.", 100, 122), LoadOptions{ID: load}); err != nil {
		t.Fatalf("load: %v", err)
	}

	// The claim is taken out of the walk rather than written here, so the -1
	// under test is the one this store returned and not one a test invented.
	claims, err := l.Claims(ctx, load, "e3")
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	var c recall.Claim
	for _, x := range claims {
		if x.Chunk < 0 {
			c = x
			break
		}
	}
	if c.Source == "" {
		t.Fatalf("no chunkless claim among %v; this fixture is meant to hold one", claims)
	}
	// Mark is what hands a caller its argument, and it renders this one without
	// an index — which is the whole reason Cite is reached with a negative one.
	if m := recall.Mark(c.Source, c.Chunk); strings.Contains(m, "#") {
		t.Fatalf("Mark(%q, %d) = %q; a chunkless claim carries no index to cite", c.Source, c.Chunk, m)
	}

	_, err = l.Cite(ctx, load, c.Source, c.Chunk)
	if !errors.Is(err, recall.ErrNoChunk) {
		t.Errorf("Cite of a chunkless claim = %v, want ErrNoChunk", err)
	}
	// Named separately, because the two are what got conflated: they say
	// opposite things about how far to trust the claim.
	if errors.Is(err, recall.ErrNoCitation) {
		t.Errorf("Cite of a chunkless claim reported ErrNoCitation: %v; "+
			"the claim did not fail to check out, it never had text under it", err)
	}

	// The same source IS chunked in this load and chunk 0 of it resolves, so
	// the answer is decided by the marker the caller holds and not by whether
	// the file was ever chunked.
	if got, err := l.Cite(ctx, load, c.Source, 0); err != nil || got.Text == "" {
		t.Errorf("Cite(%s#0) = %+v, %v; the source under test is one this load did chunk", c.Source, got, err)
	}

	// The load is still checked first. Answering "this claim has no text, and
	// that is fine" for an import that is not here would hand back an ordinary
	// answer for the one mistake the load parameter exists to catch.
	if _, err := l.Cite(ctx, load+"-typo", c.Source, c.Chunk); !errors.Is(err, recall.ErrNoLoad) {
		t.Errorf("Cite of a chunkless claim in an unknown load = %v, want ErrNoLoad: "+
			"the load is checked before the marker is", err)
	}
}
