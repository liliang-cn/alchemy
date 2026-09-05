package main

// The corpus, and why it is shaped like this.
//
// The vocabulary is a subset of a real one: ai/domain.toml in the SDS
// repository, the storage/HA domain an operator copilot actually answers from.
// Entity and relation names are taken from it unchanged, including the
// lower-case relation spellings, which that file explains at length — the
// SCREAMING_SNAKE set it started with produced a graph that looked populated
// and expanded to nothing, because the walker compared edge types by exact
// string equality and DEPLOYED_ON is not deploys. Nothing here re-litigates
// that; it is borrowed as evidence, not as an example.
//
// One thing is added that domain.toml has no way to say, and it is the whole
// experiment: `promotes` is declared at_most_one_in. A DRBD resource is Primary
// on at most one node at a time — that is not a modelling preference, it is the
// invariant the whole system is built to keep, and it is the reason split-brain
// is a word.
//
// So the two documents below are not contrived to disagree. They are a runbook
// and an incident note, each correct when it was written, describing a resource
// that failed over between them. That is the CARDINALITY conflict: the only
// kind that makes a fact able to go out of date. Neither document is wrong; the
// older one is stale, and no amount of reading either one alone reveals it.
//
// The incident note also mentions Prometheus scraping a metrics endpoint. The
// vocabulary has no word for either. What the extractor does with that is not
// scripted — constrained extraction may decline to emit it, or emit it and have
// the verifier refuse it — and the demo reports which happened.

const runbook = `# sds-meta HA runbook

The sds-meta DRBD resource is promoted on node hp. Storage pool vg0 backs
sds-meta.

While hp holds it, sds-meta has state Primary.
`

const incidentNote = `# Incident 2026-08-17 — sds-meta failover

At 02:14 node hp lost quorum and drbd-reactor demoted it. Node dell promotes
sds-meta now, and sds-meta has state Primary on dell.

Prometheus scrapes the metrics endpoint on dell every 15 seconds.
`

// sdsOntology is the vocabulary both documents are read under.
//
// It is an input and never inferred. An ontology a model wrote, used to
// constrain that same model's extraction, checks nothing (DESIGN.md §5), which
// is why alchemy requires one for document sources and offers no unconstrained
// mode.
const sdsOntology = `{
  "id": "sds-demo@1",
  "parts": {
    "prose": {
      "entities": [
        {"name": "Node"},
        {"name": "DRBDResource"},
        {"name": "StoragePool"},
        {"name": "PromoterConfig"},
        {"name": "State"}
      ],
      "relations": [
        {"name": "promotes",  "from": ["Node", "PromoterConfig"], "to": ["DRBDResource"], "at_most_one_in": true},
        {"name": "backs",     "from": ["StoragePool"], "to": ["DRBDResource"]},
        {"name": "has_state", "from": ["DRBDResource", "Node"], "to": ["State"]}
      ]
    }
  }
}`
