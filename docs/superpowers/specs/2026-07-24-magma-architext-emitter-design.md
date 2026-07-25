# Design: Magma → Architext code-graph emitter (Spec A)

Date: 2026-07-24
Status: proposed
Initiative: Magma ↔ Architext bridge (Spec A of two — see Spec B: `2026-07-24-architext-code-graph-layer-design.md`)

## Problem

Architext's architecture model is **authored** — a human or an LLM writes `nodes.json` /
`flows.json` / `views.json` as prose claims about how a system is structured. Those claims
drift from the code, and nothing in Architext measures the drift. Magma already computes the
**objective** truth that would measure it: a deterministic, LLM-free call graph — same repo
+ SHA → same graph every time. But magma emits that graph only as `graph.json` and Obsidian
markdown; Architext cannot read it, and the graph carries no per-function detail (parameters,
return types, doc) rich enough to reason about at the architecture level.

This spec covers the **magma side** of the bridge: enrich the graph with per-function
signature data, and add an output adapter that writes an Architext-readable
**objective code-graph layer** — a machine-authored counterpart that sits beside the
human-authored architecture layer without touching it.

## Goals

- Enrich each node with **signature metadata**: parameters (name + type), result types, the
  doc-comment's first sentence, and graph-derived fan-in / fan-out.
- Emit a new **`magma-code-graph/1`** JSON artifact Architext can ingest (contract frozen
  jointly with Spec B before either side builds against it).
- Represent both tiers of the approved **hybrid** model in one file: fine (functions + calls)
  and coarse (module rollups + inter-module edges) for drill-down.
- **Dual write**: into the target repo's `docs/architext/data/` (where `architext serve`
  reads) **and** mirrored into the Obsidian vault's `.magma/` machine layer.
- Stay **deterministic** (same repo + SHA → same bytes) and **idempotent** (freshness-skip).
- **Refuse honestly**: an uncomputable graph yields a refused code-graph, never a fake one.
- Hold the **80% LOC coverage** bar; the new adapter is fully unit-tested.

## Non-goals

- **No wall-clock time in the JSON** — byte-determinism holds for freshness and clean diffs.
  (Any "last validated" timestamp is a viewer concern in Spec B, never in this artifact.)
- **No signature enrichment for non-Go languages.** Magma only enriches what its own parser
  computes; other backends refuse until their parser exists (magma decision #5).
- **No differential logic here.** Comparing claims to reality is the next milestone, on the
  Architext side, and depends on both layers being frozen first (deferred per Rule 16 —
  sequencing/dependency, see Spec B §"Next milestone").
- **No change to the existing wire contracts** (`codemap-graph/1`, `codemap-rows/1`) or the
  Obsidian markdown layer. This adds an artifact; it does not alter existing ones.

## The contract: `magma-code-graph/1`

One file, `code-graph.json`, byte-deterministic at a SHA (sorted; no timestamps). The
envelope reuses magma's existing provenance so a refused graph propagates unchanged:

```jsonc
{
  "contract_version": "magma-code-graph/1",
  "generator": "magma vX.Y.Z",
  "language": "go",
  "module": "github.com/robot-accomplice/magma",
  "sha": "ce1a52a",
  "tree": "clean",                    // "clean" | "dirty"
  "fidelity": "rta",                  // what an edge MEANS (carried through, not reinterpreted)
  "computable": true,
  "not_computable_reason": "",        // set + everything below null when computable=false

  "functions": [                      // FINE tier — one per magma node
    {
      "id": "internal-backend-buildgraph",   // stable slug: slugify(pkg + symbol)
      "symbol": "BuildGraph", "pkg": "internal/backend", "file": "internal/backend/golang.go",
      "line": 44, "kind": "func", "exported": true,
      "test": false, "root": false, "generated": false,
      "reachable": true, "prod_reachable": true,
      "signature": {
        "params":  [{ "name": "repo", "type": "string" }],
        "results": [{ "type": "contract.Graph" }, { "type": "error" }]
      },
      "doc": "BuildGraph loads the repo twice and returns its call graph.",
      "fan_in": 3, "fan_out": 11
    }
  ],
  "calls": [                          // FINE tier — one per magma edge
    { "from": "…", "to": "…", "site_file": "…", "site_line": 51, "kind": "static" }
  ],

  "modules": [                        // COARSE tier — package rollup
    {
      "id": "internal-backend", "pkg": "internal/backend",
      "function_ids": ["internal-backend-golang-buildgraph", "…"],
      "counts": { "functions": 14, "dead": 0, "test_only": 2 },
      "fan_in": 4, "fan_out": 9
    }
  ],
  "module_calls": [                   // COARSE tier — aggregated inter-module edges
    { "from": "internal-backend", "to": "internal-contract", "count": 27, "has_dynamic": true }
  ]
}
```

### Id stability (determinism's linchpin)

`id` must be stable across runs at a SHA and unique within the file. Derivation:
slugify `pkg` + `symbol` (+ a disambiguating suffix from the signature only on collision —
overloaded-by-receiver methods). Never derive an id from array position or map-iteration
order. Ids are the join key Spec B and the future differential rely on; churn here breaks both.

### Refusal

`code-graph.json` is always written. When the graph is not computable (unsupported language,
no production main, code that does not type-check), `computable:false` + `not_computable_reason`
is set and `functions/calls/modules/module_calls` are `null` — a refusal is never mistaken for
an empty repository. This mirrors `Graph.Refuse` exactly.

## Magma-side changes

### 1. Node enrichment (`internal/contract` + `internal/backend/golang.go`)

Add to `contract.Node`:

```go
Signature Signature `json:"signature,omitempty"` // params + results
Doc       string    `json:"doc,omitempty"`        // first sentence of the doc comment
FanIn     int       `json:"fan_in"`
FanOut    int       `json:"fan_out"`
```

`Signature` / `Param` are new domain types (Params `[]Param{Name,Type}`, Results
`[]Result{Type}`). The Go backend reads these from the SSA `d.fn.Signature` it already holds
in `collectNodes`; type strings are rendered **module-relative and clean**, reusing the
existing `prettyName` qualifier approach so `*github.com/…/contract.Graph` renders `contract.Graph`.
`FanIn` / `FanOut` are counted from the edge set (no new analysis). `Doc` comes from the
declaration's AST doc comment, first sentence only (pointers, not payloads — consistent with
the markdown layer's no-code-bodies rule).

These fields are additive; the existing `codemap-graph/1` gains optional fields under a
tolerant reader, and the markdown/JSON layers are unchanged in behavior.

### 2. Output adapter (`internal/architext/`) — a boundary, not a tangle

A **new package** whose sole job is `Graph → magma-code-graph/1 JSON`, with the coarse tier
aggregated from the fine tier. It is an **interface adapter** (clean-architecture): it depends
on `contract.Graph`; `contract` and the backend depend on **nothing** in `internal/architext`.
The dependency arrow points inward only. Swapping Architext's format, or adding another
consumer's format later, touches only this package.

Shape (small, single-responsibility units — clean-code):

```
internal/architext/
  emit.go        Emit(g contract.Graph) CodeGraph          // pure: graph -> DTO, no I/O
  rollup.go      rollup(functions, calls) ([]Module, []ModuleCall)
  ids.go         nodeID(n), moduleID(pkg)                  // deterministic slugs
  write.go       Write(cg CodeGraph, dests ...string) error // the only I/O
```

`Emit` is pure and table-testable with an in-memory `Graph` — no filesystem, no repo. `Write`
is the sole I/O seam. This is the testability-by-design boundary: the mapping is verified with
fakes, never by running an analysis.

### 3. Dual write + flag + freshness (`main.go`)

- New flag `--architext[=DIR]`. Default destination: the **target repo's**
  `docs/architext/data/code-graph.json`. Given `=DIR`, that directory instead.
- Always also mirror into the vault's `<folder>/.magma/code-graph.json` (the "both" decision),
  so the machine layer travels with the map.
- Containment guard: the repo-side destination is validated the same way vault output is
  (no traversal, no symlink escape) before any write — reuse the existing `prepareOutput`
  containment logic rather than re-implementing it.
- Freshness: extend the manifest and `isFresh` so a stale/missing `code-graph.json` forces a
  rebuild, and a fresh one is skipped, exactly like the existing markdown assets. Refused maps
  must always re-attempt (never report "already fresh") — the invariant fixed in commit 30d5273
  applies to this artifact too.

## Determinism & honesty invariants

1. `code-graph.json` is byte-identical for the same repo + SHA (sorted arrays; stable ids;
   no timestamps).
2. Every field is derivable from the graph — none invented. Fan-in/out are counts; `doc` is a
   verbatim first sentence.
3. A candidate is not a verdict: reachability rows remain candidates (reflection, `encoding/json`
   interfaces, cgo, external entry points). Spec B presents them with that caveat intact.
4. Honest refusal over fake data.

## `/clean-architecture` — architectural boundaries (mandated)

- **Dependency rule.** `internal/architext` is an outer-ring adapter. Arrows point inward:
  adapter → `contract` (entities) and the backend, never the reverse. A grep for
  `internal/architext` imports inside `contract/` or `backend/` must return nothing; that check
  is part of the merge gate.
- **Boundary DTOs.** The graph crosses into the adapter as `contract.Graph` (a plain struct),
  and leaves as the `CodeGraph` DTO. No Architext concept (schema ids, viewer shapes) leaks
  back into magma's core.
- **Screaming structure.** Package name `architext` names the *destination format*, an output
  concern — it reads as an adapter, not as core analysis.
- **Proportional.** Magma is a CLI, not a four-layer service; we add exactly one adapter package
  and four small files, not a ports-and-adapters framework.

## `/clean-code` — code-quality gate (mandated)

- **Small units, one thing each.** `emit`, `rollup`, `nodeID`, `Write` are separate; none mixes
  I/O with mapping (Command-Query Separation: `Emit` queries, `Write` commands).
- **Names reveal intent.** `nodeID`, `moduleID`, `fanIn` — no `data`/`info` noise.
- **No boolean/flag arguments** into the mapping functions; no speculative configurability
  beyond the single `--architext[=DIR]` flag the vision requires (YAGNI).
- **No magic literals.** The contract version string, default `docs/architext/data` path, and
  file name are named constants in one place.
- **Tests encode why.** Fixture assertions state the *reason* (e.g. "an interface method is
  `dynamic`, so its call edge must carry `kind:\"dynamic\"`"), not just the value.
- **Pre-merge self-review.** Re-invoke `/clean-code` and `/clean-architecture` on the diff
  before merge; fix findings inline.

## Testing (80% LOC bar held)

- `internal/architext`: pure `Emit` tests over hand-built `contract.Graph` fixtures covering
  every field (params/results rendering, doc first-sentence extraction, fan-in/out counts,
  static vs dynamic edges), rollup correctness (module counts = sum of members), id stability
  (same graph twice → identical ids; collision → deterministic disambiguation), and refusal
  passthrough. `Write` tested against a temp dir (dual destination, containment rejection).
- `backend/golang`: extend the existing `testdata` fixture module so `BuildGraph` populates
  signature/doc/fan fields; assert exact params/results for a known function.
- `main`: `--architext` flag parse + freshness (stale forces rebuild, fresh skips, refusal
  re-attempts).

## Sequencing

**Contract-first.** Freeze `magma-code-graph/1` (this §"The contract") jointly with Spec B,
then implement: node enrichment → adapter (`Emit`/`rollup`/`ids`) → `Write` + flag + freshness
→ tests to bar. Spec B builds its schema and graph view against the same frozen contract.
