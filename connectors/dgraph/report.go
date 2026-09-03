package dgraph

// Report is what one load wrote, and what it could not.
//
// Every field that counts a loss is here because §5 obliges a graph to carry
// "the numbers needed to distrust it", and a connector that dropped something
// quietly would be the exact defect the rest of this design refuses. The two
// losses this store has are its own and are named as such.
type Report struct {
	Load   string
	Digest string

	Entities   int
	Relations  int
	Chunks     int
	Violations int
	Duplicates int
	Guesses    int
	Unread     int
	// Supersessions is how many retirements were filed beside the graph. A
	// count of claims recorded and never of records removed: alchemy states a
	// retirement and does not perform one.
	Supersessions int

	// Batches is how many round trips this load made. An operator holding a
	// load that died halfway needs to know how much work it had done.
	Batches int

	// MergedRelations is how many assertions could not be kept apart.
	//
	// A Dgraph edge is (subject, predicate, object) and its facets are one set,
	// so two records asserting the same edge cannot both keep their provenance
	// — measured: the second write's facets replace the first's and the server
	// answers Success. The first is kept and the rest counted here, rather than
	// written and lost. Where a property graph pays nothing for two records,
	// this is what a facet costs.
	MergedRelations int

	// SkippedVectors is how many embeddings were left behind. Dgraph holds no
	// embeddings this connector could use, and dropping them without saying so
	// would be a silent loss.
	SkippedVectors int
}
