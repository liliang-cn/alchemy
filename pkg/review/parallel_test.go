package review_test

import (
	"testing"

	"github.com/liliang-cn/alchemy/pkg/alchemy"
	"github.com/liliang-cn/alchemy/pkg/review"
	"github.com/liliang-cn/alchemy/pkg/verify"
)

var (
	ddlA = alchemy.Provenance{Source: "a.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
	ddlB = alchemy.Provenance{Source: "b.sql", Chunk: -1, Producer: alchemy.ProducerDDL}
)

// A decision about one of several parallel edges acts on that edge and not on
// its sibling.
//
// A Ref names records by what they say, and until edges had a key two parallel
// edges from one source said exactly the same thing: same ends, same type, same
// provenance. So one Ref named both, and a reviewer answering a question about
// the destination end of a connection would silently delete the source end too
// — the same defect as the false conflict, one layer down, and worse, because
// this one loses data instead of asking a pointless question.
func TestADecisionAboutOneParallelEdgeLeavesItsSiblingAlone(t *testing.T) {
	entities := []alchemy.Entity{
		{ID: "table:node_connections", Type: "Table", Name: "node_connections", Provenance: ddlA},
		{ID: "table:nodes", Type: "Table", Name: "nodes", Provenance: ddlA},
	}
	relations := []alchemy.Relation{
		// b.sql is listed first so that the incumbent claim is b.sql's and the
		// dissenting one — the side a rejection acts on — is a.sql's, which is
		// the source that states both ends.
		{From: "table:node_connections", To: "table:nodes", Type: "REFERENCES", Key: "fk_dst",
			Attributes: map[string]any{"columns": "target_name"}, Provenance: ddlB},
		{From: "table:node_connections", To: "table:nodes", Type: "REFERENCES", Key: "fk_src",
			Attributes: map[string]any{"columns": "node_name_src"}, Provenance: ddlA},
		{From: "table:node_connections", To: "table:nodes", Type: "REFERENCES", Key: "fk_dst",
			Attributes: map[string]any{"columns": "node_name_dst"}, Provenance: ddlA},
	}
	rep := verify.Check(verify.Input{Entities: entities, Relations: relations})
	if len(rep.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want exactly the one about fk_dst", rep.Conflicts)
	}
	res := alchemy.Result{Entities: rep.Entities, Relations: rep.Relations, Conflicts: rep.Conflicts}
	items := review.Queue(rep, res, review.Options{Reviewing: true})
	if len(items) != 1 || len(items[0].Targets) != 1 {
		t.Fatalf("item = %+v, want one target: the a.sql record the question is about", items[0])
	}

	out, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: items[0].ID, Verb: review.VerbReject, By: "ana"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var keys []string
	for _, r := range out.Relations {
		keys = append(keys, r.Key)
	}
	if len(keys) != 2 || keys[0] != "fk_dst" || keys[1] != "fk_src" {
		t.Fatalf("keys = %q, want b.sql's fk_dst and a.sql's fk_src to survive", keys)
	}
}

// A merge collapses edges that have become the same edge. Two parallel edges
// never became the same edge, and collapsing them would delete one end of a
// connection because somebody answered a question about two node names.
func TestAMergeDoesNotCollapseParallelEdges(t *testing.T) {
	entities := []alchemy.Entity{
		{ID: "package:doc", Type: "Package", Name: "doc", Provenance: chunk1},
		{ID: "package:doc package", Type: "Package", Name: "doc package", Provenance: chunk2},
		{ID: "table:nodes", Type: "Table", Name: "nodes", Provenance: ddlA},
	}
	relations := []alchemy.Relation{
		{From: "package:doc", To: "table:nodes", Type: "REFERENCES", Key: "fk_src", Provenance: ddlA},
		{From: "package:doc", To: "table:nodes", Type: "REFERENCES", Key: "fk_dst", Provenance: ddlA},
	}
	finding := alchemy.Duplicate{
		Signal:  alchemy.DuplicateNameAffix,
		Subject: "package:doc ~ package:doc package",
		Left:    alchemy.DuplicateSide{ID: "package:doc", Type: "Package", Name: "doc", Provenance: chunk1},
		Right:   alchemy.DuplicateSide{ID: "package:doc package", Type: "Package", Name: "doc package", Provenance: chunk2},
	}
	rep := verify.Report{Entities: entities, Relations: relations, Duplicates: []alchemy.Duplicate{finding}}
	res := alchemy.Result{Entities: entities, Relations: relations, Duplicates: []alchemy.Duplicate{finding}}
	items := review.Queue(rep, res, review.Options{Reviewing: true})

	out, _, err := review.Apply(res, items, []review.Decision{
		{ItemID: items[0].ID, Verb: review.VerbEdit, By: "ana", Edit: &review.Edit{Into: "package:doc"}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(out.Relations) != 2 {
		t.Fatalf("relations = %+v, want both ends kept", out.Relations)
	}
}
