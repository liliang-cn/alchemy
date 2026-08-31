package rdf

import "strconv"

// The vocabulary, in one place, because a predicate spelled in a writer and
// again in a query is a name with two homes and the query is the one that fails
// silently by matching nothing.
//
// The standard terms are used where a standard term says exactly what alchemy
// means and nowhere else. A store that invented al:label beside rdfs:label
// would hold a graph no other RDF tool can read, which is most of what buying
// a triple store was for; a store that reached for owl:sameAs because it was
// nearby would hold a graph other tools read *wrongly*, which is worse. See
// closeMatch and the ontology file for the two places that line was drawn.

const (
	rdfNS  = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	rdfsNS = "http://www.w3.org/2000/01/rdf-schema#"
	owlNS  = "http://www.w3.org/2002/07/owl#"
	skosNS = "http://www.w3.org/2004/02/skos/core#"

	// alNS is alchemy's own vocabulary and it is FIXED, where the data IRIs
	// come from Options.Base and are the buyer's.
	//
	// The split is deliberate. Two customers' loads describe different things
	// and must not collide, so their subjects are theirs. But "this edge's
	// producer" is the same predicate in every alchemy store there will ever
	// be, and a namespace derived from the base URL would give each deployment
	// its own spelling of it — so a query written against one store would
	// silently match nothing against another, and nothing federated could ever
	// join two.
	//
	// example.* is reserved by RFC 2606 and can never be registered, so this
	// IRI cannot one day resolve to somebody else's document.
	alNS = "http://alchemy.example/ns#"
)

// The standard terms this connector emits.
const (
	rdfType        = rdfNS + "type"
	rdfProperty    = rdfNS + "Property"
	rdfsClass      = rdfsNS + "Class"
	rdfsLabel      = rdfsNS + "label"
	rdfsComment    = rdfsNS + "comment"
	skosAltLabel   = skosNS + "altLabel"
	skosCloseMatch = skosNS + "closeMatch"
	owlFunctional  = owlNS + "FunctionalProperty"
	owlInverseFn   = owlNS + "InverseFunctionalProperty"
)

// The classes this connector's own records carry. Every one of them exists so
// that a read can say what it wants rather than what it does not: a query for
// entities matches al:Entity, and a chunk, a finding and the load marker are
// excluded because they are not that, rather than because somebody remembered
// to list them.
//
// neo4j does the opposite — it excludes its internal labels by name — and its
// own comment says what that costs: "a kind missing from here is not an error
// anywhere", and a test has to hold the list complete. An inclusion list cannot
// go stale by omission.
const (
	clLoad         = alNS + "Load"
	clEntity       = alNS + "Entity"
	clChunk        = alNS + "Chunk"
	clViolation    = alNS + "Violation"
	clDuplicate    = alNS + "Duplicate"
	clGuess        = alNS + "Guess"
	clUnread       = alNS + "Unread"
	clConflict     = alNS + "Conflict"
	clRuleSet      = alNS + "RuleSet"
	clRule         = alNS + "StandingRule"
	clSupersession = alNS + "Supersession"
	clModelCall    = alNS + "ModelCall"
	// clRelationType is on every predicate this connector mints for an
	// alchemy relation type, and it is what makes the one-hop walk an
	// inclusion list. See recall.go.
	clRelationType = alNS + "RelationType"
	clEntityType   = alNS + "EntityType"
)

// The predicates. The load marker's first, because they are what decides
// whether anything else may be read at all.
const (
	pLoad       = alNS + "load"
	pDigest     = alNS + "digest"
	pComplete   = alNS + "complete"
	pStartedAt  = alNS + "startedAt"
	pFinishedAt = alNS + "finishedAt"

	pID   = alNS + "id"
	pType = alNS + "type"

	// The provenance predicates. They are declared here with every other name
	// this package writes, and provFields is the one place that says which
	// alchemy.Provenance field each one carries and how it is read back.
	pSource     = alNS + "source"
	pChunk      = alNS + "chunk"
	pProducer   = alNS + "producer"
	pStated     = alNS + "stated"
	pModel      = alNS + "model"
	pOntology   = alNS + "ontology"
	pChunking   = alNS + "chunking"
	pConfidence = alNS + "confidence"
	pReviewedBy = alNS + "reviewedBy"
	pRuleSet    = alNS + "ruleSet"
	pRuledBy    = alNS + "ruledBy"
	// asserter and assertedAt rather than by and at: al:by would read as the
	// agent of any statement it is attached to, and these are specifically the
	// person alchemy.ProducerHuman names and the date they said it.
	pAsserter   = alNS + "asserter"
	pAssertedAt = alNS + "assertedAt"

	pIndex    = alNS + "index"
	pText     = alNS + "text"
	pStart    = alNS + "start"
	pEnd      = alNS + "end"
	pStrategy = alNS + "strategy"
	pHeading  = alNS + "heading"

	pKind      = alNS + "kind"
	pDetail    = alNS + "detail"
	pSubject   = alNS + "subject"
	pSignal    = alNS + "signal"
	pLeftName  = alNS + "leftName"
	pRightName = alNS + "rightName"
	pLeft      = alNS + "left"
	pRight     = alNS + "right"
	pStatement = alNS + "statement"
	pAbout     = alNS + "about"

	pField       = alNS + "field"
	pChosenAs    = alNS + "chosenAs"
	pAlternative = alNS + "alternative"
	// pAttribute names one attribute an ontology declares an entity type may
	// carry. It is not pAlternative, although both are lists of strings on a
	// declaration: an alternative is a mapping that was not chosen and an
	// attribute is a field the type is allowed, and one predicate for both would
	// make a query for either return the other.
	pAttribute   = alNS + "attribute"
	pReason      = alNS + "reason"
	pLocator     = alNS + "locator"
	pRetires     = alNS + "retires"
	pReplacedBy  = alNS + "replacedBy"
	pRule        = alNS + "rule"
	pName        = alNS + "name"
	pTold        = alNS + "told"
	pStage       = alNS + "stage"
	pCalls       = alNS + "calls"
	pTokens      = alNS + "tokens"
	pRelationKey = alNS + "key"
	// pJSONAttribute names the attribute keys whose values had to be written as
	// JSON text because RDF has no literal for an object or an array. See
	// attrPairs: a conversion nobody can see from the data is a quiet rewrite.
	pJSONAttribute = alNS + "jsonAttribute"
	pDeclaredFrom  = alNS + "fromType"
	pDeclaredTo    = alNS + "toType"
	pAtMostOneIn   = alNS + "atMostOneIn"
	pAtMostOneOut  = alNS + "atMostOneOut"
	pBothWays      = alNS + "bothWays"
	pOntologyID    = alNS + "ontologyID"
)

// countPreds is alchemy.Counts as predicates on the load marker. §5: "every
// returned graph is accompanied by the numbers needed to distrust it", and a
// graph in a triple store whose quality numbers are in a file on somebody's
// laptop is a graph you merely have.
var countPreds = struct {
	Entities, Relations, Chunks, Vectors, Deterministic, Inferred,
	Violations, Conflicts, Guesses, Duplicates, ChunksEmpty, ChunksUnread, Dropped string
}{
	alNS + "countEntities", alNS + "countRelations", alNS + "countChunks", alNS + "countVectors",
	alNS + "countDeterministic", alNS + "countInferred", alNS + "countViolations",
	alNS + "countConflicts", alNS + "countGuesses", alNS + "countDuplicates",
	alNS + "countChunksEmpty", alNS + "countChunksUnread", alNS + "countDropped",
}

// The data IRIs. Every one of them is built here and nowhere else, so
// escapeSegment is applied exactly once per untrusted part and a caller cannot
// forget it.

// loadIRI is both the named graph a load is written into and the subject of its
// own marker.
//
// One IRI for both is what makes "is this load finished" and "read this load"
// the same graph pattern rather than two round trips: the marker lives inside
// the graph it describes, so a query that opens GRAPH <g> can assert
// <g> al:complete true in the same block. It is also what makes Replace one
// statement — DROP GRAPH takes the records and the marker together, and a store
// that dropped the data while keeping the marker would report a finished load
// over an empty graph.
func (l *Loader) loadIRI(load string) string {
	return l.opts.Base + "load/" + escapeSegment(load)
}

// entityIRI is load-scoped, and that is this connector's central decision, the
// same one neo4j makes with (run, id).
//
// alchemy.Entity.ID is stable within one result and says nothing across runs.
// A named graph would not be enough on its own: an IRI is global in RDF, so the
// same IRI in two graphs is the same resource, and any query over the union of
// graphs — which is what a default-graph query is in most stores — would fuse
// two unrelated things that happened to share a counter. Joining nodes across
// loads is entity resolution, which §5 defers to a second release; doing it on
// a within-load identifier would be doing it wrong and calling it done.
func (l *Loader) entityIRI(load, id string) string {
	return l.loadIRI(load) + "/entity/" + escapeSegment(id)
}

func (l *Loader) chunkIRI(load string, index int) string {
	return l.loadIRI(load) + "/chunk/" + escapeSegment(strconv.Itoa(index))
}

// recordIRI names one of this connector's own records — a finding, a rule set,
// a retirement. The sequence number is the record's position in the result,
// which is what makes a replay write the same IRI and therefore the same
// triples: RDF is a set, so an idempotent load is one that produces the same
// subjects, and a random or time-based name would turn every re-load into a
// second copy of every finding.
func (l *Loader) recordIRI(load, kind string, seq int) string {
	return l.loadIRI(load) + "/" + kind + "/" + strconv.Itoa(seq)
}

// classIRI and relIRI are deliberately NOT load-scoped.
//
// A type is the ontology's and not the import's. Two loads under one vocabulary
// that both hold Systems mean the same class by it, and giving each load its
// own <.../load/x/type/System> would leave a buyer unable to ask "every System
// in this store" — which is the question a class is for. It is safe in a way
// entity identity is not: an ontology type name is declared and shared by
// construction, where an entity ID is a within-result counter.
//
// The consequence is stated rather than hidden: dropping one load leaves the
// class declarations another load also wrote, because they are the same triples
// in another graph. A graph is a set and RDF has no reference counting; the
// alternative is a load's DROP removing vocabulary another load is still using.
func (l *Loader) classIRI(typ string) string {
	return l.opts.Base + "type/" + escapeSegment(typ)
}

// relIRI mints the predicate for one alchemy relation type. §5b's mapping is
// that a relation type is an IRI predicate — which is the thing RDF is best at
// and the reason USES reads as a verb in every query over this store.
func (l *Loader) relIRI(typ string) string {
	return l.opts.Base + "rel/" + escapeSegment(typ)
}

// attrIRI mints the predicate for one free-form attribute key.
//
// Under its own path rather than mixed with the relation types, because the two
// are different claims: a relation type is declared by an ontology and checked
// against it, and an attribute key is whatever the source called a column.
// Sharing a namespace would let an attribute named USES arrive as a relation
// predicate and be walked as a claim about the world.
func (l *Loader) attrIRI(key string) string {
	return l.opts.Base + "attr/" + escapeSegment(key)
}
