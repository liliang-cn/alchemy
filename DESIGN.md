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
              │   Verifier    │ ── every edge checked against the ontology
              └───────────────┘
                      │
                      ▼
    Result { entities, relations, chunks, vectors,
             guesses[], violations[], provenance }
```

The Verifier is a stage, not a flag. An extraction nobody checked is an
extraction nobody should act on.

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

### What this does not promise

It does not promise the graph is *correct*. A model can produce a
well-typed edge that is simply wrong about the world, and no amount of
vocabulary checking catches that. What is promised is that a wrong edge is
**attributable** — you can see it was inferred, by which model, from which
chunk of which file — and therefore checkable, correctable, and excludable.

An agent that says "SuperAI uses CortexDB, per architecture.pdf page 4,
extracted rather than declared" is doing something a confident sentence cannot.

---

## 6. API sketch

```
POST /v1/jobs                 multipart: file + job spec (models, ontology, hints)
  → 202 { job_id }
GET  /v1/jobs/{id}            → { state, progress, stage, counts }
GET  /v1/jobs/{id}/result     → Result
DELETE /v1/jobs/{id}          → forget it (see §4 — nothing is kept anyway)
```

Async because a PDF with OCR is minutes, not milliseconds, and a synchronous
API would push every caller into a timeout they have to work around.

Models are supplied per job, not configured globally: a buyer's LLM, embedding
and OCR endpoints are their business, and a service that hardcodes them is one
that only works in the environment it was built in.

---

## 7. Open questions

1. **Chunking is not neutral.** Chunk boundaries decide what an extractor can
   see at once, and a relation whose two ends land in different chunks is a
   relation nobody extracts. Needs a real answer before documents ship.
2. **Entity resolution across sources.** The same customer arrives from a CSV
   and from a contract PDF. CortexDB's ontology has `objectType + primaryKey`
   for exactly this; whether alchemy resolves or merely reports the collision is
   undecided.
3. **Cost.** Extraction over a large corpus is a lot of model calls. Whether the
   service estimates before running, or streams a cost as it goes, is a product
   decision that affects the API.

---

## 8. Status

Design only. Nothing is built. This document exists so the first line of code
answers to something.
