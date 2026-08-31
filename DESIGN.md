# Alchemy — design

A service that turns files into knowledge: entities, relations, chunks and
vectors, ready to be queried. You hand it a document or a table and the models
to use; it hands back a graph.

Ordinary text files, unstructured data and structured data all become one
graph — so an agent can reason over data it can **trust and explain**.

Those last two words are the product, and they are why this is not a wrapper
around an LLM prompt. An agent that answers from a graph is only as good as
that graph, and a graph nobody can audit is a confident answer with no way to
check it. So every edge here carries where it came from and how it was
produced, and every result carries the numbers needed to distrust it (§5).

**Trustworthy** means an edge is either derived deterministically from
something that already stated it, or inferred under a declared ontology and
checked against it. **Explainable** means every entity and relation can name
the source, the chunk and the producer it came from — an agent citing this
graph can show its work.

It is written to be sold or deployed on its own, which is the constraint that
shapes everything below.

---

## 1. What it is not

**It is not a database.** It returns its output and forgets it. See §4.
A clustered deployment shares a job store (§8.3), which holds work in
progress and still never holds a graph.

**It is not DataIntelligence.** DI ingests structured sources into a warehouse
under a governed semantic layer, so a consultant can ask for a metric by
dimension with RBAC, RLS and masking applied. Its product is rows you are
allowed to see. Alchemy's product is a graph an agent can walk. The overlap is
one step — inferring how source fields map to a model — and the two do it for
different reasons.

**It is not a knowledge graph generator you point at a folder and trust.**
That is the thing everyone wants and the thing this document spends most of its
length arguing against. See §5.

---

## 2. Where the pieces come from

Nothing here starts from zero. Four working implementations already exist,
each solving a different corner, and each carrying a lesson its author wrote
down after being bitten. Alchemy is the shape those four already imply.

| Source kind | Existing implementation | How it works | Determinism |
|---|---|---|---|
| Tabular — CSV, TSV, SQL dump | CortexDB `pkg/importflow` | LLM infers column→field mapping; writes RAG + KG | Medium |
| DDL — `CREATE TABLE` | oss-agent `internal/schemaimport` | Table→entity, foreign key→relation. **No LLM** | **High** |
| Prose — docs, PDF | oss-agent `internal/extract` | LLM extracts triples, constrained to a Domain vocabulary | **Low** |
| Existing graph — knowledge-graph.json | oss-agent `internal/graphimport` | Nodes/edges → ontology graph; summaries → vectors | High |
| Warehouse rows | DI `crates/di-ingest` | Source→mapping→rules→DB | Medium |

The last row is listed for completeness and is **not** absorbed: its product is
warehouse rows under governance, which is DI's job and stays there.

### 2.1 The three lessons, in the words of the people who learned them

These are quoted rather than paraphrased because each one cost real debugging,
and each one is a requirement on this design.

**Determinism beats inference wherever it is available.** From `schemaimport`:

> Deterministic, no LLM — the database schema *is* the ground-truth ontology.

A `CREATE TABLE` already states the entity and a `FOREIGN KEY` already states
the relation. Asking a model to infer what is written down is strictly worse:
slower, more expensive, and occasionally wrong about something that was never
ambiguous.

**A guess that does not announce itself is a bug with a three-month fuse.**
From `di-ingest`:

> 子配这一步是这一层唯一会静悄悄出错的地方 — `id` 同时是 `order_id` 和
> `product_id` 的子串，取哪一个只看列在源里的先后顺序，而两种取法都会跑得干干净净。
> 一个猜错的映射不会报错，它只会让一整张表对不上账，然后在三个月后由一个人手工发现。

Every inferred mapping is reported. This is why `Result.Guesses` exists and why
it is not optional.

**Extraction must be constrained by a vocabulary, and the vocabulary must be
partitioned by provenance.** From oss-agent's `domain.Vocabulary`:

> The fields above are the PROSE vocabulary: an LLM reads documentation and
> emits Cluster, Node, DEPLOYED_ON. They are pasted into the extractor's prompt
> ("Use ONLY these entity types"), which is exactly why a code vocabulary cannot
> live there — telling a prose extractor it may emit `function` and `calls`
> invites it to invent code structure out of documentation.

This is the mechanism that took one real graph's compliance from 74% to 94%.
It is not a refinement; unconstrained extraction produces a graph that is large,
plausible, and wrong in ways nobody notices until they act on it.

---

## 3. Shape

```
                ┌─ tabular    CSV · TSV · SQL dump
                ├─ ddl        CREATE TABLE            ← deterministic
  Source ───────┼─ document   PDF · md · txt · html   ← the hard one
                └─ graph      knowledge-graph.json
                      │
                      ▼
              ┌───────────────┐
              │  Extractor    │ ◄── Ontology (vocabulary, partitioned by
              └───────────────┘      provenance; see §2.1)
                      │
                      ▼
              ┌───────────────┐
              │   Verifier    │ ── every edge checked against the ontology;
              └───────────────┘    sources that disagree become conflicts
                      │
                      ▼
              ┌───────────────┐
              │ Review (HITL) │ ── optional, EXCEPT for conflicts: a job that
              └───────────────┘    finds one holds until a person decides (§7.3).
                      │            Then violations, then guesses. Never the
                      │            deterministic.
                      │
                      ▼
              ┌───────────────┐
              │   Embedder    │ ── after review, so vectors describe the text
              └───────────────┘    that survived it
                      │
                      ▼
    Result { entities, relations, chunks, vectors,
             conflicts[], violations[], guesses[], provenance }
```

The Verifier is a stage, not a flag: an extraction nobody checked is an
extraction nobody should act on. Review is a flag, and off by default — most
imports do not deserve a person (§5c).

---

## 4. It returns; it does not store

**Decision: alchemy outputs JSON and writes to no database.**

The alternative — bundling CortexDB — is less work for us and is what makes
the thing unsellable. A buyer with Neo4j, pgvector or Qdrant already has a
graph store and a vector store; being told they must also adopt ours to use an
import service is a reason to write their own.

The cost is honest and ours to pay: our own projects gain a thin write layer.
That is a few hundred lines in one place, against a product nobody outside can
adopt.

A pluggable `Sink` interface was considered and deferred. An interface defined
before there are two real consumers is a guess about a shape, and a wrong
interface is harder to change than no interface. When a second consumer exists,
its needs will define the interface. Until then the JSON is the contract.

### 4.1 The deferral ended, and what it bought

Four consumers now exist — Neo4j, pgvector, Qdrant and CortexDB — each written
against the JSON alone, none able to see the others. The wait was worth what it
was supposed to be worth: they disagreed about things an interface written
first would have guessed wrong, and they agreed about things it would have
missed.

**Where they disagreed.** All four needed to know when two edges are the same
edge, and all four answered differently: a hash of the whole assertion, the
record's position in the slice, a composite of the endpoints and the key, and
the tuple the store itself already used. The same result therefore had
different edge cardinality in four stores. That is not four mistakes; it is one
missing field, and no amount of thinking about it in advance would have
produced the evidence that it was missing. `Relation` had no identity because
`Entity.ID` had always been there and nobody noticed the asymmetry.

**Where they agreed, without coordinating.** Two of them wrote near-identical
functions to check that a result's vectors share one width and name real
chunks. Two independently invented domain-separated content fingerprints to
answer "have I loaded this already", with the same reasoning about `Entity.ID`
meaning nothing across runs. Two independently invented the same convention for
attribute values a store cannot hold natively. All four had to reach into
another package to ask whether a result was held, which means a fifth would
eventually forget and ship a self-contradicting graph past §7.3.

That is the shape of the interface, and it is a fact rather than a preference:
**what every sink had to write for itself belongs above the interface, and what
they each answered differently belongs to the store.** Pre-flight refusal,
result identity, and the streaming envelope are the first; batching mechanics,
index policy, dimension binding and the query surface are the second.

One thing only one of them needed is in anyway. A vector store cannot hold a
traversal, so loading a graph into Qdrant loads its records and not its shape —
and a connector that returned success without saying so would be lying by
omission. Every load reports what the store could not keep. It is in the
interface because a guarantee that only holds where it is convenient is not a
guarantee, and because the next store nobody has thought of will need it too.

---

## 5. Scope of the first release

**Decision: the first release includes documents (PDF) and entity extraction.**

This is the ambitious answer and it carries the risk this document keeps
naming, so the scope is stated as obligations rather than features.

### In

- **Sources**: tabular, ddl, document, graph.
- **Document text**: PDF with a text layer, markdown, plain text, HTML.
  Scanned PDFs need OCR; if the OCR model is not supplied, a scanned page is
  **reported as unread**, never silently returned as empty text. (harness-rs hit
  exactly this: pdf-extract returns nothing for a scan, and the fallback sent
  raw PDF bytes through `from_utf8_lossy` to the model — an OCR that looked like
  it worked.)
- **Extraction under an ontology**. Supplying an ontology is **required** for
  document sources. There is no unconstrained mode. The 74%→94% story is the
  argument, and shipping the ungoverned version would mean shipping the 74%.
- **Verification**: every entity type and relation type checked against the
  ontology; every violation returned in `Result.Violations` with the chunk it
  came from.
- **Provenance**: every entity and relation carries what produced it — which
  source, which chunk, deterministic or inferred, which model.
- **A service**: HTTP API, async jobs with progress, authentication, and a
  report format. Not a library with an HTTP wrapper added later.

### Out, deliberately

- Storage of any kind (§4).
- Warehouse loading and governance — that is DI.
- Automatic ontology generation. An ontology inferred by a model, then used to
  constrain that same model's extraction, checks nothing. The ontology is an
  input.
- Incremental re-import and change detection. Second release.

### The obligation that makes the ambitious scope defensible

A release that returns a large graph and no way to judge it is worse than one
that returns less. So:

> **Every returned graph is accompanied by the numbers needed to distrust it:**
> how many edges were deterministic vs inferred, how many violated the ontology,
> how many chunks produced nothing, and which mappings were guessed.

If those numbers cannot be produced for a source kind, that source kind is not
in the release.

---

## 5b. What "trustworthy and explainable" is made of

The two words in §0 are a promise, and a promise with no mechanism is
marketing. Three mechanisms make it true, and each is refusable — if any one
cannot be delivered for a source kind, that source kind does not ship.

### Every edge names its producer

Each entity and relation carries provenance:

```json
{ "from": "SuperAI", "to": "CortexDB", "type": "USES",
  "provenance": {
    "source": "architecture.pdf", "chunk": 14,
    "producer": "llm-extract", "model": "gemini-3.6-flash-high",
    "ontology": "sds@3", "confidence": 0.82 } }
```

`producer` is the field that matters. `ddl` and `graph-import` mean a machine
read something that already said this. `llm-extract` means a model decided it.
An agent citing the graph can say which, and a person auditing it can filter to
the half that was guessed.

### Every graph reports its own quality

The result carries counts, not just content:

```json
{ "counts": {
    "entities": 412, "relations": 1180,
    "deterministic": 890, "inferred": 290,
    "violations": 17, "chunks_empty": 23, "guesses": 4 } }
```

This is the number that makes the difference between a graph you can act on and
one you merely have. A run with 1180 edges and 400 violations is a failure that
looks like a success, and without this block nobody would know.

### Nothing is extracted without a vocabulary to check it against

For document sources the ontology is required (§5). The extractor is
constrained by it, and the verifier checks the output against it — the same
list on both sides of the model. An edge whose type is not in the ontology is
not silently dropped and not silently kept: it is returned in `violations` with
the chunk that produced it, so the failure is visible and locatable.

This is the mechanism from §2.1 stated as a product guarantee: the graph is
never more permissive than the ontology you declared.

### The corpus is what says what the vocabulary is missing

The rule above runs one way: the ontology is written first and everything is
checked against it. That is right for a model, which is what §2.1 is about, and
it is the wrong order for a person. Somebody who knows a fact had to hand-edit
a vocabulary document and re-run before they could state it — stating one team
of four people meant adding an entity type and three relation types by hand,
first, in JSON.

So a result carries `proposals`: the types a source used that the ontology does
not declare, one entry per type rather than one per record, with the ends the
type was observed between, who used it, how often, and one record to go and
look at. The same assertion produces six violations and three proposals; a
four-hundred-thousand-record import missing one type produces four hundred
thousand and one.

It proposes and never applies, which is the same line every other finding here
sits on. "MEMBER_OF was used four times, always from Person to Team, by
liliang" is a fact about the corpus. Whether that is what the type *means* is a
judgement, and §2.1 is the argument that a plausible judgement nobody made is
the failure that survives review. An end whose own type is also undeclared is
left out rather than proposed alongside — a line proposing two undeclared
things at once is a line nobody can accept as one thing.

Proposals exist exactly when violations do, so a run that declared no
vocabulary produces none: it is not missing one. The way to ask "what
vocabulary would this graph need" is to supply an empty ontology, which makes
it a question rather than a side effect of not asking — and keeps every
ungoverned result ever loaded at the content address it already has.

### A fact has to be able to go out of date

The vocabulary says which edges may exist. Until it could also say how many, a
corpus could not disagree with itself about a fact that changed hands.

Measured, on this design's own evaluation corpus: a company profile saying
"Ada is Chief Technology Officer" and a correction saying Bruno is, in one job,
produced a graph holding both — `conflicts: 0`, job succeeded, nothing to see.
Two edges of one type between different pairs are two facts, and nothing in the
ontology could say a company has one CTO.

So a relation type may declare `at_most_one_in` or `at_most_one_out`, and a
second edge at the constrained end of a node is `cardinality` — the only
conflict kind that compares two claims about a *node* rather than about one
edge. It is a conflict and not a repair: §7.3 stops the job and a person
decides, because choosing the later record would be picking a winner on arrival
order, and a correction can itself be wrong.

Two more things were missing with it, and all three are one problem.

**Two producers can name one thing differently.** `verify` declined a
same-name-different-id duplicate signal on the grounds that "entityID is a
function of type and name, so equal names are already one node". That is true
of ids this pipeline mints and false of ids a source supplies: a graph import
brings the document's, an assertion brings the asserter's, and `org:northgate`
never met `organization:northgate`. The signal now fires when the two producers
differ, which leaves the case the original argument was protecting —
`public.users` and `audit.users`, two tables one schema declared — exactly as
it was.

**A correction states two facts and only one of them is an edge.** That Bruno
holds the office, and that Ada no longer does. `Result.supersessions` is where
the second one goes: what is retired, what replaces it, why, and who says so.
Alchemy never acts on it — §4 means it holds no graph, and a producer that
could delete another producer's fact by naming it would be §2.1 with write
access — but the statement survives the pipeline, so a store can act on it
deliberately and a reader months later can see that somebody said the old
answer was over, and name them.

### What this does not promise

It does not promise the graph is *correct*. A model can produce a
well-typed edge that is simply wrong about the world, and no amount of
vocabulary checking catches that. What is promised is that a wrong edge is
**attributable** — you can see it was inferred, by which model, from which
chunk of which file — and therefore checkable, correctable, and excludable.

Attributable is what makes correction possible, which is why §5c exists: a
person can review what the models proposed. That is the only mechanism here
that produces correctness, and it is optional because most imports do not need
it.

An agent that says "SuperAI uses CortexDB, per architecture.pdf page 4,
extracted rather than declared" is doing something a confident sentence cannot.

---

## 5c. Review: the machine proposes, a person decides

**Decision: extraction output can be reviewed and corrected by a human before
it is accepted. The review step is optional.**

This is what closes the gap §5b is careful to leave open. Provenance makes a
wrong edge attributable; review is how it gets fixed. Together they are a
different claim from either alone: not "the model was right", but "the model
proposed, and what you have was checked."

### Why it is optional — and what is not

Most imports do not deserve a person. A DDL import is deterministic — there is
nothing to review, and putting a queue in front of it would be ceremony. A
nightly re-import of a table whose mapping was approved last month should not
ask again.

So review is a mode, not a stage everything passes through. The default is off,
and a caller that never turns it on gets exactly the service described above.

**With one exception: a conflict always stops the job.** Two sources that
disagree are a question, not a quality score, and a question has to be asked of
someone. See §7.3 — it is the one place this design refuses to let a caller opt
out of a person.

### What is worth reviewing is already computed

The review queue is not a new analysis. §5b already produces the ranking:

| First | `violations` | An edge whose type is not in the ontology. Something is wrong: the ontology, the extractor, or the document. |
| Then | `guesses` | An inferred field mapping. One wrong guess misaligns a whole table (§2.1). |
| Then | low-confidence `inferred` | The model was unsure and said so. |
| Never | `deterministic` | A `CREATE TABLE` said it. Asking a person to confirm what the schema states is how you teach them to click Approve without reading. |

**Conflicts rank above all of them.** Two sources that disagree are the one
thing no amount of per-source checking can resolve, and the one thing a person
is genuinely better at than the machine:

- the same entity arriving from a CSV and a contract PDF with different
  attributes — is it one customer or two? (this is open question §7.2, and
  review is the answer to it: report the collision, let a person decide)
- the same relation asserted in one direction by a schema and the other by
  prose — a foreign key says orders→customer, a document says the customer owns
  the orders
- a deterministic edge contradicted by an inferred one — which is worth surfacing
  precisely *because* the deterministic side almost always wins, and the
  exception is where the interesting bug lives

A conflict is never resolved silently, and never by precedence alone. The
result carries `conflicts[]` whether or not review is on, so a caller running
unattended still learns that its sources disagree rather than receiving
whichever edge happened to be written last.

That last row matters. A queue that includes the obvious is a queue people stop
reading, and then the review is worse than none — it launders unchecked output
as reviewed.

### What a reviewer can do

Per item: **accept**, **reject**, **edit** (retype an entity, redirect an edge,
rename), or **always** — accept this and stop asking about ones like it.

`always` is the one that earns its keep. Reviewing a thousand extractions one
at a time is not a workflow anybody sustains; reviewing the twelve *kinds* of
mistake in them is. A rule is recorded with the decision that produced it, so
a later reader can see why the rule exists.

### What this costs the design

**It makes the service stateful, which §4 said it was not.**

That contradiction is real and is resolved by scope rather than by pretending:
a job under review is held until it is accepted or expires, and what is held is
the pending result and the decisions made on it — never a knowledge base. The
service still stores no graph. It holds work in progress, the way a print queue
holds a document without becoming a filesystem.

Two consequences to design for rather than discover:

- **A held job needs a lifetime.** Un-reviewed work expires; the expiry is
  configurable and reported in the job state. Otherwise the "stateless" service
  quietly grows a database of abandoned reviews.
- **Decisions are part of the result.** An accepted graph carries who accepted
  what, so the provenance of a reviewed edge says `llm-extract, reviewed by X`
  rather than losing the fact that a model proposed it. Review adds to
  provenance; it does not overwrite it.

### Where the embedding model sits in this

Vectors are not reviewable and should not be in the queue — nobody can eyeball
a 768-dimensional vector. They are recomputed for whatever text survives
review, which is the only sensible ordering: embedding rejected content wastes
the call, and embedding before edits means the vectors describe text that has
since changed.

---

## 6. Interface

**Decision: gRPC is the service. Anything else is a gateway in front of it.**

Three reasons, in order of weight:

**Review is a conversation, not a poll.** A person working a queue wants items
as they are found and wants their decisions to take effect on work still
running — an extractor that has already learned "this is not an entity" should
stop proposing it in the next chunk. That is a bidirectional stream. Modelled
over HTTP it becomes polling plus a submit endpoint, which is the same thing
with latency and more state on both sides.

That paragraph is about a job that is *still running*, and for a while it was
read as though it were about review. The difference cost something real: a
buyer could fetch a held graph over HTTP and could not say "this edge is wrong"
over HTTP, because the only way to answer a queue was a bidirectional stream.
A job stopped at `NEEDS_REVIEW` has nothing still arriving — the findings are a
finite list and the decisions are a batch — so `ListFindings` and `Decide` are
that case and they are ordinary HTTP routes. `Review` stays untranslated and
its path stays 501, because the property it has and they do not is a decision
reaching an extraction that has not happened yet. Same decisions, same code,
same provenance; what differs is when they may be sent.

**Progress on a long job is a stream.** A PDF corpus with OCR is minutes.
Server-streaming says so natively; over HTTP it is either polling or SSE, and
SSE is a stream pretending to be a response.

**It matches what it will sit beside.** CortexDB is already gRPC, and a buyer
integrating both should not learn two transport styles for one pipeline.

```protobuf
service Alchemy {
  // Unary for things that are one question.
  rpc CreateJob(CreateJobRequest) returns (Job);
  rpc GetJob(GetJobRequest) returns (Job);
  rpc GetResult(GetResultRequest) returns (Result);
  // A large result is not one message; see §8.4.
  rpc StreamResult(GetResultRequest) returns (stream ResultPage);
  rpc DeleteJob(DeleteJobRequest) returns (google.protobuf.Empty);

  // Upload is client-streaming: a corpus is not a field in a message.
  rpc UploadSource(stream SourceChunk) returns (Source);

  // Progress is server-streaming: stages, counts, and conflicts as they are
  // found rather than at the end.
  rpc WatchJob(WatchJobRequest) returns (stream JobEvent);

  // Review is bidirectional: items out, decisions in, on one connection, so a
  // decision reaches an extraction that has not run yet.
  rpc Review(stream ReviewDecision) returns (stream ReviewItem);
  // A job can reach NEEDS_REVIEW without review mode being on: a conflict
  // always requires a person (§7.3). Review is how it gets unblocked, which is
  // why this rpc is not gated on the job having asked for review.
}
```

A REST/JSON gateway is generated from the same definitions, because a buyer
evaluating the product should be able to curl it, and because browsers exist.
The gateway is a translation, never a second source of truth about what the
service does.

Models are supplied per job, not configured globally: a buyer's LLM, embedding
and OCR endpoints are their business, and a service that hardcodes them only
works in the environment it was built in. The chunking strategy (§7.1) is a job
input for the same reason — the person who knows the corpus is the caller.

---

## 7. Decisions that were open

### 7.1 Chunking — offer the known strategies, let the caller choose

*Decided.* Chunk boundaries decide what an extractor can see at once, and a
relation whose two ends land in different chunks is a relation nobody extracts.
That makes chunking a decision about the corpus, not a detail — and the person
who knows the corpus is the caller, not us.

So the strategies are named, their trade-offs are stated, and the caller picks:

| Strategy | Splits on | Suits | Costs |
|---|---|---|---|
| `fixed` | N tokens, fixed overlap | anything; the predictable baseline | cuts mid-sentence and mid-fact |
| `sentence` | sentence boundaries, packed to a budget | prose, reports | a fact spanning a paragraph can still split |
| `paragraph` | blank lines | documents already written in units | wildly uneven chunk sizes |
| `heading` | markdown/HTML headings, section as chunk | manuals, specs, wikis | a long section exceeds any context |
| `semantic` | embedding distance between adjacent blocks | corpora with no reliable structure | costs an embedding pass before extraction |
| `whole` | no split | short documents that fit | fails loudly, not silently, when they do not |

Default is `heading` falling back to `paragraph` then `fixed`, because most of
what people import has structure and ignoring it is the one choice that is
wrong for every corpus rather than some.

Two rules that hold whichever is chosen:

- **Overlap is the cheap insurance against the split-relation problem** — a
  relation cut in half by a boundary is recovered when the next chunk starts
  before the previous one ended. Overlap is configurable and non-zero by default.
- **The chunking used is part of the provenance.** A graph re-extracted under a
  different strategy is a different graph, and a reader comparing two runs needs
  to know which one they are looking at.

### 7.2 Cost — not a constraint

*Decided: cost is not optimised for.* Quality wins. The service does not
degrade an extraction to save calls, does not silently sample a corpus, and
does not pick a cheaper model than the one it was given.

This is a real product position and it has a consequence worth stating rather
than discovering: **a large corpus can be expensive, and the caller must not be
surprised by it.** Not optimising for cost is not the same as hiding it. So:

- the job reports how many model calls it made, by model and stage, in the
  result
- `WatchJob` streams a running count, so a job whose bill is growing faster than
  expected can be cancelled while it runs rather than after it finishes

Estimating before running was considered and rejected: an estimate over an
unread corpus is a guess, and a wrong guess about money is worse than no number
at all. Reporting the real one as it accrues is honest and is enough.

### 7.3 A conflict always requires a person

*Decided.* Review is optional. **Conflicts are not.**

A job that finds a conflict does not finish. It reaches `NEEDS_REVIEW` and stays
there until someone resolves it — whether or not the caller asked for review
mode.

The alternative was to return `conflicts[]` and let an unattended caller carry
on. That is wrong, for the reason that is the whole thesis of this document: a
graph that contradicts itself is worse than no graph, because an agent reading
it will answer from whichever edge it happened to traverse — confidently, with a
citation. The contradiction does not surface at the moment of harm. It surfaces
months later as one wrong answer nobody can explain.

So the split is:

| | Review off | Review on |
|---|---|---|
| Deterministic edges | accepted | accepted |
| Inferred edges | accepted, marked inferred | queued |
| Violations | returned, graph delivered | queued |
| **Conflicts** | **job holds — a person must decide** | queued |

Violations are deliberately on the other side of that line. A violation is one
source saying something the ontology does not allow: attributable, excludable,
and the rest of the graph is usable without it. A conflict is two sources both
claiming to be right, with nothing in the data to decide between them. That is
not a quality metric; it is a question, and questions have to be asked of
someone.

**What this means for an unattended pipeline.** A nightly import that hits a
conflict does not silently produce a bad graph, and does not silently produce
nothing — it produces a held job and says so. The operator's options are the
same as anyone's: resolve it, or tell the service how to resolve conflicts of
that shape next time (§5c's `always`), which is how a pipeline that started
attended becomes one that runs itself without ever having guessed.

Two mechanics follow:

- **A held job's expiry is longer than a reviewed one's.** Optional review work
  can expire cheaply; a job blocked on a real question should outlive a long
  weekend. It still expires — §5c on not growing a database of abandoned reviews
  — but the timer respects that someone has to be found.
- **`WatchJob` emits a conflict when it is found, not at the end.** An operator
  watching a two-hour import should learn in minute three that it will need
  them, not at minute one hundred and twenty.

### 7.4 Still open

Nothing blocking. The chunking defaults (§7.1) and the held-job expiry above are
guesses that should be revisited once there is a real corpus, and a real
operator to be annoyed by them.

---

## 8. Scale: high-volume concurrent import, and clustering

**Decision: the job is the unit of ownership, the chunk is the unit of
parallelism, and the cluster coordinates through a shared job store rather than
through each other.**

Two things break a naive scale-out, and they are the reason this section is a
design decision rather than an ops note.

### 8.1 A job cannot be sharded, because a conflict is global to it

§7.3 makes conflicts the one thing that always stops a job. A conflict is two
sources disagreeing — and only something that sees *both* can notice. Spread one
job's sources across five nodes and the disagreement between source 1 and source
4 is visible to nobody: every node finishes cleanly, and the merged graph
contradicts itself. That is precisely the failure this design exists to prevent,
arriving through the back door of a scheduler.

So:

- **One node coordinates a job end to end.** It holds the accumulating identity
  index and is the only place conflicts are decided.
- **Chunk extraction fans out.** A chunk is an independent LLM call against a
  vocabulary; that is embarrassingly parallel and is where the wall-clock is.
- **The merge is on the coordinator**, and conflict detection is keyed by entity
  identity rather than compared pairwise — an O(n²) scan is a plausible-looking
  implementation that dies at the volume this section is about.

The cost is honest: a single enormous job is bounded by one node's coordination.
That is acceptable because the throughput case is *many* jobs, and the latency
case is chunks within one. A job so large that its own merge is the bottleneck
should be split by the caller, who knows what the corpus is.

### 8.2 The bottleneck is the caller's model endpoint, not our CPU

This is the one that surprises people. Extraction is a network call to a model
the caller supplied (§6), and that endpoint has a rate limit. Ten nodes each
running "8 concurrent" is 80 in flight against an endpoint that permits 20 — the
cluster's own success at scaling out is what triggers the 429s.

So **model concurrency is a cluster-wide budget, not a per-node setting.** It is
declared per model endpoint, leased from the shared store like any other
resource, and a node that cannot get a slot waits rather than tries.

Two consequences worth designing for rather than discovering:

- **Retries are part of the budget.** A retry storm after a rate-limit is a
  cluster attacking its own dependency. Backoff is coordinated through the same
  lease, not chosen independently by each node.
- **§7.2 said cost is not optimised for. That is about quality, not waste.**
  Declining to use a cheaper model is a product position; paying twice for the
  identical call after a crash is a bug. Extraction results are cached by
  content address — hash of (chunk text, model, ontology version, prompt
  version) — so a resumed job does not re-buy what it already has. The cache is
  keyed on everything that would change the answer, which is why the prompt
  version is in the key: a cache that survives a prompt change is a cache that
  returns the old prompt's opinion.

### 8.3 What clustering actually requires

Deliberately small. The service does not gossip, does not elect a leader, and
does not embed a consensus library.

- **A shared job store.** In-memory for a single node (the default, and what a
  buyer evaluating the product runs), and one real implementation — Postgres —
  for a cluster. This does not contradict §4: it stores *jobs*, never graphs.
  The print queue analogy from §5c is the same one, now on shared paper.
- **Leases with heartbeats.** A node that dies mid-job must not take a held job
  with it. The lease expires, another node picks the job up, and the
  content-addressed cache (§8.2) means the re-run costs the chunks that had not
  finished rather than all of them.
- **At-least-once, made safe by idempotency.** A lease that expires because a
  node was merely slow means two nodes briefly work the same job. That must be
  survivable rather than prevented, because preventing it is the part that needs
  consensus. Writes are idempotent by job ID and chunk index; the second writer
  loses harmlessly.

Requiring Postgres for the clustered mode is a real deployment cost and is worth
it: inventing our own replicated state is the kind of thing that works in
testing and loses a job at 3am in someone else's data centre.

### 8.4 Volume changes two things about the interface

- **A big result is not one message.** gRPC's default message limit is 4MB and a
  large import blows through it. `GetResult` stays for jobs that fit; a
  server-streaming `StreamResult` returning pages of entities and relations is
  what a large job uses. A caller should never have to discover this by
  receiving a truncation.
- **A big source is not held in memory.** `UploadSource` is already
  client-streaming (§6); the received bytes are spooled to disk and every reader
  works from a stream. A 10GB dump that is parsed by reading it into a string is
  a service that dies on the first real customer.

**Admission control, not optimism.** A queue that accepts everything is a queue
that OOMs. The service declares its capacity and refuses beyond it with a clear
"try later" rather than accepting work it cannot hold — a rejected job is an
operator's problem for a minute, an accepted job that dies is their problem for
an afternoon.

### 8.5 What is deferred

Autoscaling policy, multi-region, and any scheduling cleverer than "least loaded
node that can get a model slot". The first is the operator's, the second needs a
customer with the problem, and the third is a guess about a workload nobody has
measured yet.

---
## 9. Status

**Built, 2026-08-29/30.** §1–§7 are implemented and §6's gateway with them; §8
is implemented for a single node and designed for more.

Twenty-three packages, 600+ tests, `go test ./... -race` green, six direct
dependencies: a PDF reader, gRPC, protobuf, grpc-gateway with the generated
API annotations it needs, and a Postgres driver. The pipeline runs end to end
against real model endpoints, over gRPC and over HTTP.

The last of those six is in tension with §4 and this sentence is where that
stays visible. §8.3's clustered stores put pgx in the *core* module, so a
buyer who wants neither Postgres nor the REST gateway compiles both anyway —
which is the thing §4 refuses to do to them with a graph store, done to them
with a job store instead. The four graph consumers are a separate module for
exactly this reason; these are not, and that is a debt rather than an
argument.

This paragraph said "three dependencies (a PDF reader, gRPC, protobuf)" for
as long as it took somebody to hash go.sum for an unrelated reason. It was
true when it was written. Every measurable sentence in this section is now a
check in `internal/claims` that `go test ./...` runs, so the next one fails
instead of ageing.

What is real rather than claimed:

- **§7.3 holds.** Two schemas that each parse cleanly and only disagree with
  each other stop the job at `NEEDS_REVIEW`; `GetResult` refuses; the question
  is on the review stream. Verified on a running binary, over both transports.
- **§5's counts are computed and add up** — `Deterministic + Inferred ==
  Relations` is a test, not a convention.
- **§7.2's running cost is a stream**, and a real run reports what it spent by
  model and stage.
- **§6's gateway is generated**, reproducibly, from this file's own service
  definition. `Review` is refused with 501 rather than translated, because a
  bidirectional stream has no honest shape over HTTP — the one place the
  transport decision in §6 turned out to be load-bearing rather than
  preferential.

§8.3's clustered mode is built too: `job.PG`, `budget.Postgres` and
`cache.Postgres`, all tested against a real database rather than a mock, with a
conformance suite the in-memory stores pass as evidence the second
implementation is faithful. The cluster-wide concurrency bound of §8.2 is
measured across two nodes rather than asserted. Leases heartbeat, and liveness
is part of the `Lease` interface rather than an optional one a wrapper can drop
by writing nothing — a mistake this codebase made once, one level up, and paid
for in a silently unreported bill.

§6's first reason for gRPC is now true rather than aspirational: a decision
taken while a job runs reaches the chunks that have not been extracted yet,
both as a standing answer in the prompt and as a filter on what comes back. The
cost is stated where it lands — a run during which a decision arrives is no
longer reproducible from its inputs, so every record names the rules it was
proposed under, and a run with no mid-run decisions is still byte-identical at
every concurrency.

A fact somebody knows and no document states now has a way in that says so.
`Assert` takes a small graph, a name and a reason, stamps every record
`human` with the asserter and the date, checks it against the ontology like
anything else, and returns it — one call, synchronously, because an assertion
has no chunking, no model and no embedding and there is nothing to poll. Until
it existed the only route was to write the fact into a file and import it,
which arrived stamped `graph-import` — "an existing graph already asserted it"
— and cost the record the one thing that made it worth admitting: a person who
can be asked. `human` is deterministic, and that is the substantive claim
rather than bookkeeping: what makes an `llm-extract` edge inferred is not that
a machine wrote it down, it is that nobody can be asked about it.

**The limit of that, stated because it is the next thing somebody will hit.**
Conflict detection is per run. An assertion that merely *adds* a fact is one
call; an assertion that *contradicts* something already extracted does not
meet it, because the two records are in two results and nothing compares them.
Contesting an existing claim means running both sources in one job, which is
where the conflict is found and where §7.3 holds it. Fixing that properly
would mean the service holding graphs so a new claim could be checked against
an old one, and that is §4 — so the honest answer today is that alchemy
corrects a corpus by re-running it, not by patching it.

### The read side, and what it is for

§4 still holds: alchemy stores nothing. What it has is a companion module,
`connectors/`, that puts a result into somebody else's store and reads it back
out — and the second half is much newer than the first.

Five stores: Neo4j, pgvector, Qdrant, CortexDB, and any SPARQL endpoint that
speaks RDF-star. All five implement `sink.Sink` and pass one conformance suite.
Three implement `recall.Reader`, which is seven primitives — find an anchor,
walk one hop, resolve a citation, ask what is unanswered, ask what contributed,
read the vocabulary, read out one class — each taking the load as a parameter
rather than as an option, because the default is where the bug was.

Those five were measured rather than designed: they are what building one
context pack by hand needed and nothing more. The evidence that this is the
right way to arrive at a primitive is that the fifth arrived after the first
four were in use. An agent was asked who the CTO was and answered from a node
two sources had silently been merged into — two mentions of a bare first name,
joined on string equality — while the graph carefully reported the join it had
*refused* to make on a fuller name one row over. Both agent runtimes stated it
as settled, six runs out of six, and none of the first four primitives could
have told them otherwise. So the graph reported the joins it declined and said
nothing about the ones it made, and only half of identity was visible — the
wrong half, because the other one has already been acted on. `Contributions` is
that hole. It reports and does not judge: a primitive that answered "risky"
would be doing the judging §2.1 reserves for a person.

**What the read side is checked against.** Four Halcyon sources through the
pipeline, into Neo4j, out through `recall.Reader`, into a ReAct agent on two
unrelated runtimes — one Go, one Rust — five questions, three runs each. The
answers were identical in content on both sides: the same people, the same
citations, the same stated-versus-inferred split, the same refusal where the
graph does not say. Where the graph is the constraint the runtime is invisible,
which is the result that experiment was for.

It also produced three defects that belonged to the graph rather than to either
agent, and all three were one shape — a tool demanding something its own output
does not contain.

- `Unanswered` treated `"all"` as a literal substring while its description told
  the model to pass `"all"`. Twenty-nine runs in thirty wrote "no unresolved
  identity questions" against a store holding thirteen of them.
- `Cite` had two outcomes where the data has three. A record whose producer did
  not work in chunks has nothing to quote, which is not a citation that failed;
  seven of thirteen attempts were refused with the sentence reserved for
  evidence that does not check out, and §5b ranks those records *above* a model
  reading prose.
- `Mark` renders a chunkless claim as `[team.json]` with no `#n`, and `Cite`
  required an index.

**And one that was a rule change nothing carried into the stores.** Two records
under one ID that agree about the node are corroboration and not a collision —
`preflight` says so — but each connector had met the second record on its own
terms: two kept the last write, one would have failed at a primary key, and the
triple store put both sources on one annotation with nothing saying which went
with which. Two of the five refused such a result outright, so the one thing
this product exists to produce, a graph merged from several documents, could not
be loaded into them at all. The fold is now in `sink`, once, and the case is in
the conformance suite all five run — which is where it should have been found,
except that the suite drives the envelope and the refusal sat in the connector's
own `Load`.

**And the two that were still missing when the paragraph above was written.**
Asked what kinds of thing a graph held, an agent made eighty-three tool calls —
an anchor search per letter of the alphabet, because a substring search is
genuinely the only way to enumerate with one — and produced a table that was
right about the total and wrong in five places under it: thirteen types where
the load has fourteen, four of the counts off by one or two, and one row reading
"1–2" because it could not tell. Asked to list every person it named thirteen of
twenty-one, having said twenty in the table a minute earlier — one graph, two
runs, two answers that do not agree with each other, neither hedged. A total
that comes out right because the errors cancel is the worst version of this: it
is the number a reader would spot-check. `Types` and `OfType` are that hole, and
the count is on the vocabulary rather than left to a second call because the
count is what tells a caller which limit to pass — which is the difference
between reading a class out and truncating it. Walking deeper than one hop was
the same shape one field over and needed no new method: `Claims` returned names
and took an ID, so the fix was to return the ID as well.

What is still not built: a graph store of any kind inside alchemy, which is §4
and deliberate. On the read side, nothing is a measured gap today — which is a
statement about what has been measured, not a claim that seven is the number.

The document this began as was written so the first line of code would answer
to something. It did.
