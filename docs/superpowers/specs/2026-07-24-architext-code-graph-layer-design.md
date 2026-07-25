# Design: Architext code-graph layer — schema + graph view (Spec B)

Date: 2026-07-24
Status: proposed
Initiative: Magma ↔ Architext bridge (Spec B of two — see Spec A: `2026-07-24-magma-architext-emitter-design.md`)
Repo: `~/code/architext` (dev version 1.7.9). This spec travels to that repo when its dev begins.

## Problem

Architext's data model is entirely **architecture-level and human-authored**: every
`nodes.json` entry is a coarse component (`service`, `module`, `data-store`,
`trust-boundary`…) with prose fields (`summary`, `responsibilities`, `owner`, `guarantees`)
and a kebab-case id; flows and views are hand-curated lanes. It has **nowhere to hold a
fine-grained call graph** and no way to render one. So the objective truth magma produces
(Spec A's `magma-code-graph/1` — thousands of function nodes with signatures) cannot be
ingested or shown, and the drift between what the architecture layer *claims* and what the
code *does* stays invisible.

This spec covers the **Architext side**: expand the schema to hold magma's objective
code-graph as a **separate layer**, build a **graph-view renderer** that ingests and presents
it with drill-down, wire validation, and dogfood the whole thing by documenting magma's own
architecture. The two layers sitting side by side are what makes the future **differential**
(claims vs reality) possible.

## Goals

- **Schema expansion**: a new `code-graph.schema.json` (`magma-code-graph/1`) that validates
  magma's artifact, wired into `architext validate` as an **optional** file.
- **New graph-view renderer** in the viewer: interactive call graph with **hybrid drill-down**
  — module rollup → expand to functions — plus a node inspector (signature, reachability
  badges, fan-in/out) and static-vs-dynamic edge styling.
- **Layer separation**: the code-graph layer never overwrites the human `nodes/flows/views`
  layer; both are first-class and independently editable.
- **Dogfood**: stand up Architext in the magma repo and hand-author magma's own architecture,
  giving an immediate testbed where claims and reality coexist.
- Name and scope — but do not build — the **differential + refactoring-heuristics** milestone.

## Non-goals

- **The differential engine is out of scope here** (see §"Next milestone"). It is deferred, not
  dropped: it depends on both the `magma-code-graph/1` contract (Spec A) and this layer being
  frozen and stable first — building it against moving schemas would be rework (Rule 16:
  dependency/sequencing).
- **No change to the existing human-layer schemas.** The code-graph schema is additive.
- **No magma coupling.** Architext reads a frozen JSON contract; it knows nothing of how magma
  computes the graph, just as magma knows nothing of the viewer.
- **No fabricated code-graph data.** If `code-graph.json` is absent, the viewer shows the
  architecture layer alone; if it is a refusal, the viewer shows the refusal reason — never a
  synthesized graph.

## Schema expansion

`viewer/schema/code-graph.schema.json`, `$id` `magma-code-graph/1`, validating the artifact
frozen in Spec A: envelope (provenance + `computable`/`not_computable_reason`), `functions[]`
(id, symbol, pkg, file, line, kind, flags, reachability, `signature`, `doc`, fan), `calls[]`,
`modules[]`, `module_calls[]`. Key differences from the existing schemas, all deliberate:

- **Ids are magma slugs, not the human `^[a-z][a-z0-9-]*$` prose ids.** The code-graph schema
  defines its own id `$def`; it does not reuse `nodes.schema.json`'s. The two id-spaces are
  distinct on purpose — cross-links between them are the differential's job, not this layer's.
- **`computable:false` is valid** (the whole artifact may be a refusal), so `functions` et al.
  are `["array","null"]`. This lets `architext validate` accept an honest refusal as valid data.

Validation wiring (`architext validate`): load and schema-check `code-graph.json` **only when
present**; its absence is not an error (a repo that has never run magma is still valid). Add a
`code-graph` topic to `architext explain`.

## Graph-view renderer (viewer/)

> **Confirmed against the Architext source (cross-session, 2026-07-24).** The viewer is
> **Leptos 0.6 CSR (Rust → WASM)**, not React/Vite. Discovery is **manifest-driven**: an
> optional `manifest.files.codeGraph` key, auto-registered by `architext sync`/`doctor` when
> `code-graph.json` is present — not a bare unmanaged file drop; `validate` and the viewer both
> resolve the data URL from the manifest. Static vs dynamic edges are distinguished by
> **marker/colour** (connectors stay solid; dashing is reserved for sequence views). Architext's
> scope this cycle is all-in-one: schema + a dedicated code-graph internal-ref validation pass
> (`calls`→`functions`, `module.function_ids`→`functions`, `module_calls`→`modules`) + manifest
> auto-registration + the `Mode::CodeGraph` view. Versioning on their side: manifest data schema
> 1.5.0→1.6.0 (additive optional key), CLI 1.7.9→1.8.0; magma's `contract_version` stays
> independent. The authoritative implementation lives in the architext repo; this section is
> magma's design-time draft of it, corrected to those facts.

A new `Mode::CodeGraph` view in the Leptos viewer, driven by `code-graph.json` (resolved via the
manifest's `files.codeGraph` key — a machine layer, not a curated `views.json` entry). Behavior:

- **Hybrid drill-down.** Default zoom shows `modules[]` + `module_calls[]` (coarse). Expanding a
  module reveals its `functions[]` and intra-module `calls[]`. This is why the artifact carries
  both tiers (Spec A) — the view never has to aggregate at render time.
- **Node inspector.** Selecting a function shows its `signature` (params name+type, result
  types), `doc`, `fan_in`/`fan_out`, and reachability **badges**: `dead` (not reachable),
  `test-only` (reachable but not prod-reachable), `generated`, `root`. Each badge carries the
  **candidate-not-verdict** caveat from magma (reflection / cgo / external entry points can
  hide callers) — surfaced as a tooltip, not asserted as fact.
- **Edge styling.** `static` vs `dynamic` calls are visually distinct; `dynamic` is labeled as
  the RTA over-approximation, matching magma's `fidelity`.
- **Empty / refused states.** No manifest `files.codeGraph` entry → architecture layer only, with a hint to run magma.
  Refused file → show `not_computable_reason` verbatim. Never invent nodes.

Renderer decomposition (clean-code / clean-architecture, below): parsing/validation, the
view-model transform, and the Leptos components are separate modules; the components receive a
ready view-model and hold no ingestion logic.

## Dogfood: Architext for magma itself

Run `architext sync ~/code/magma` and hand-author magma's own **architecture layer**
(`nodes/flows/views.json`) — magma is a small, well-understood codebase (CLI → detect →
backend → contract → renderers), so this is a bounded, honest first model. Running magma on
itself then drops `code-graph.json` beside it. Result: one repo carrying both layers — the
canonical testbed for the differential, and a live end-to-end check that Spec A's artifact and
this schema/renderer actually agree.

## `/clean-architecture` — architectural boundaries (mandated)

- **The two layers are the primary boundary.** Human architecture data and machine code-graph
  data are separate files, separate schemas, separate id-spaces, separate ingestion paths. No
  code path lets one overwrite the other. This separation is a domain boundary, not just a file
  split — and it is what the differential later reads across.
- **Renderer layering (Dependency Rule).** Ingestion (schema-validate + parse `code-graph.json`)
  → view-model transform → presentation components. Arrows point inward toward the view-model;
  Leptos components (outer detail) depend on the view-model, never the reverse, and contain no
  parsing. Swapping the graph library touches only the presentation ring.
- **Boundary DTOs.** The raw JSON is mapped to a typed view-model at ingestion; components never
  see raw contract JSON. Framework types (Leptos, the graph lib) never leak inward to the
  transform.
- **Testability by design.** The view-model transform is testable with an in-memory parsed
  artifact — no DOM, no browser.

## `/clean-code` — code-quality gate (mandated)

- **Small, single-responsibility modules.** One module validates+parses, one transforms, one
  renders — never a "graph service" that does all three (that God-object is exactly what the
  layering forbids).
- **Names by domain.** `CodeGraph`, `ModuleRollup`, `FunctionNode`, `ReachabilityBadge` — not
  `GraphData`/`GraphInfo`.
- **No flag arguments / no speculative config.** Build the drill-down and inspector the vision
  calls for; no unused rendering options (YAGNI).
- **No magic literals.** Contract-version string, badge thresholds, edge-kind constants centralized.
- **Tests encode why.** e.g. "a `test`-flagged function that is `reachable` but not
  `prod_reachable` renders the `test-only` badge, because that is precisely magma's test-only
  definition" — not merely asserting a class name.
- **Pre-merge self-review.** Re-invoke `/clean-code` and `/clean-architecture` on the diff;
  fix inline. Respect Architext's rule that copied viewer/schema/package files in *target*
  repos are package-owned — this work lands in the Architext **source** repo, the correct place
  to edit them.

## Testing

- **Schema**: valid artifact passes; a refusal (`computable:false`, null arrays) passes;
  malformed (missing `id`, wrong id-space, negative fan) fails; absence is accepted by
  `architext validate`.
- **View-model transform**: coarse/fine drill-down mapping; badge derivation for each
  reachability combination; static/dynamic edge classification; empty and refused states.
- **Dogfood as integration check**: magma's own `code-graph.json` validates against the new
  schema and renders (the real cross-repo contract handshake).

## Next milestone (named, deferred — Rule 16 justification)

**Claims-vs-reality differential + refactoring heuristics.** With both layers frozen, compare
the human architecture layer against the objective code-graph:

- **Claimed dependencies vs observed edges** — a `nodes.json` dependency with no supporting
  `calls`/`module_calls` edge is an unsubstantiated claim; an observed edge across a
  `trust-boundary` with no decision/risk is an undocumented reality.
- **Orphaned components** — an architecture node whose `sourcePaths` map to only dead /
  test-only functions.
- **God-functions / refactor candidates** — outliers by `fan_in`/`fan_out`.

**Why deferred, not done now:** it consumes both the `magma-code-graph/1` contract (Spec A) and
this layer's view-model; building it before those are frozen means re-doing it when they move.
It is sequenced immediately after this spec ships, not dropped.

## Sequencing

**Contract-first**, shared with Spec A: freeze `magma-code-graph/1` → build schema + validation
wiring → view-model transform → renderer → dogfood (magma's own repo) as the end-to-end check.
Magma (Spec A) produces the artifact; this layer consumes it; the differential reads across both.
