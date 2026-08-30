package cortexdb

import (
	"context"
	"errors"
	"testing"

	cdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// declare activates an ontology in the target store that declares the same
// object type names alchemy's ontology uses. The primary key is a parameter
// because it, and not the enforcement mode, is what decides whether CortexDB
// keeps this connector's node ids.
func declare(t *testing.T, l *Loader, enforcement cdb.OntologyEnforcement, primaryKey string) {
	t.Helper()
	props := []cdb.OntologyProperty{{
		APIName: primaryKey, DisplayName: primaryKey, Required: true,
		DataType: cdb.OntologyDataType{Kind: cdb.OntologyDataString},
	}}
	if _, err := l.db().SaveOntologySchema(context.Background(), cdb.OntologySaveRequest{
		Activate: true, Schema: cdb.OntologySchema{
			SchemaID: "sds", Name: "sds", Enforcement: enforcement,
			ObjectTypes: []cdb.OntologyObjectType{
				{APIName: "System", DisplayName: "System", PrimaryKey: primaryKey, Properties: props},
				{APIName: "Person", DisplayName: "Person", PrimaryKey: primaryKey, Properties: props},
			},
		},
	}); err != nil {
		t.Fatalf("save ontology: %v", err)
	}
}

// A strict schema declares the properties an object may carry and rejects the
// rest — and no buyer's schema declares alchemy's dozen. So §5b's provenance
// cannot be written into a strictly-enforced store at all, and the load is
// refused before the first write instead of dying in the middle of the third
// batch with "object type \"System\" has no property \"_chunk\"".
func TestAStrictSchemaIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-O0"})
	declare(t, l, cdb.OntologyEnforcementStrict, "id")

	if _, err := l.Load(context.Background(), fixture()); !errors.Is(err, ErrStrictOntology) {
		t.Fatalf("Load into a strict store: err = %v, want ErrStrictOntology", err)
	}
	if got := countRows(t, l, "SELECT COUNT(*) FROM documents"); got != 0 {
		t.Fatalf("%d documents written by a refused load, want none", got)
	}
}

// The one place CortexDB can silently undo this connector's central decision.
//
// When the target store's active schema declares an object type with a
// name-shaped primary key, CortexDB keys the node on the entity's *name* —
// entity:system:superai — and this run's namespace is gone. Two imports of two
// documents that both mention SuperAI become one node. That is entity
// resolution, which §5 defers to a second release, being done by the store on
// the way in and reported to nobody.
//
// It is not the enforcement mode that decides it, either. "Vocabulary" sounds
// like the mode that leaves writes alone — CortexDB's own comment says it "does
// not gate writes" — but identity still consults it, which is why this test uses
// that mode. A connector that had guarded on the mode instead of on the ids
// would have let it through.
//
// The connector cannot stop CortexDB from doing it and does not try: it is a
// reasonable answer to CortexDB's own question. It asks CortexDB where the nodes
// went — the upsert returns the ids it used — and refuses the load when they are
// not where the run asked for them. Asked rather than re-derived, because the
// rule is CortexDB's and a copy of it would go stale in silence.
func TestAnOntologyThatRekeysEntitiesIsRefusedRatherThanObeyed(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-O"})
	declare(t, l, cdb.OntologyEnforcementVocabulary, "name")

	if _, err := l.Load(context.Background(), fixture()); !errors.Is(err, ErrRekeyed) {
		t.Fatalf("Load into a name-keyed store: err = %v, want ErrRekeyed", err)
	}
}

// With a primary key alchemy's entities cannot state, a vocabulary schema falls
// back to the caller's own node id and the load goes through — and CortexDB
// still canonicalises the spelling of the node type, which is why the type
// alchemy's ontology declared is kept verbatim beside it. Two names for one
// thing is a question a reader must be able to ask.
func TestAVocabularySchemaKeepsOurIDsAndOurDeclaredType(t *testing.T) {
	l := openLocal(t, Options{RunID: "run-O3"})
	declare(t, l, cdb.OntologyEnforcementVocabulary, "id")

	if _, err := l.Load(context.Background(), fixture()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	props := nodeProps(t, l, entityNodeID("run-O3", "e1"))
	if props["_declared_type"] != "System" {
		t.Fatalf("_declared_type = %#v, want the type alchemy's ontology declared", props["_declared_type"])
	}
}
