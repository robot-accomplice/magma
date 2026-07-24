# Design: Obsidian markdown graph layer

Date: 2026-07-24
Status: proposed
Issue: robot-accomplice/magma#11

## Problem

magma writes its map into an **Obsidian vault**, but Obsidian only renders `.md`
notes with `[[wikilinks]]`. The `graph.json` / `_dead.json` / `_test-only.json`
files are inert there: no note to open, no graph view, no backlinks. To a user the
folder looks empty. The value proposition — *relational questions become lookups
you can walk* — requires the call graph to exist as **linked markdown**.

## Goals

- Emit an Obsidian-navigable markdown representation of the call graph.
- A top-level **repository-state dashboard** (metrics the graph reveals).
- Preserve the machine contract (JSON) for the audit gate / CI / freshness.
- Stay deterministic (same repo + SHA → same bytes) and idempotent (freshness-skip).
- magma stays independent — markdown + wikilinks are plain text; nothing here
  couples magma to Claude or to any specific tool beyond "Obsidian renders it".

## Non-goals

- No wall-clock time in the **graph / JSON contract** — that stays byte-deterministic
  (same repo + SHA → same bytes) so freshness and clean diffs hold. The *one*
  deliberate exception is a clearly-labeled "Last validated" build timestamp in the
  human `Overview.md` dashboard (see below); it never enters the JSON.
- No code bodies in notes (pointers, not payloads).
- Not changing the JSON wire contracts (`codemap-graph/1`, `codemap-rows/1`).

## Vault layout

Written under `<vault-path>/<folder-name>/`. The whole folder is magma-owned
territory, so notes get plain, descriptive names — no leading-underscore sigil to
puzzle over (that was an old codemap holdover for sort-order + "machine-generated"
marking, both moot now that the JSON lives in `.magma/`):

```
Overview.md                     top-level repository-state dashboard (the landing note)
Dead code.md                    index: dead (unreachable) functions
Test-only code.md               index: test-only functions
Packages.md                     index: packages -> their function notes
nodes/<pkg-rel>/<Symbol>.md     per-function notes (the graph itself)
.magma/                         machine layer, hidden from Obsidian (dot-folder)
  graph.json  _dead.json  _test-only.json   the JSON contracts (names fixed by the wire contract)
  manifest.json                 list of md files magma wrote (for clean regeneration)
```

Obsidian ignores dot-folders (like `.obsidian`), so `.magma/` keeps the vault view
clean while the gate/CI/freshness still read `<folder>/.magma/*.json`. The JSON
files keep their `_dead.json` / `_test-only.json` names — those are fixed by the
`codemap-rows/1` wire contract the gate consumes, and they're hidden anyway.

## `Overview.md` — the repository-state dashboard

The landing note. Obsidian-rich: headings, tables, callouts, tags, links, and a
small mermaid diagram. Sourced entirely from the graph — every number is derivable,
none invented. Sections:

1. **Provenance callout** — repo name, language, `tree` (SHA, or a `> [!warning]`
   callout when `-dirty`), commit date (from git, deterministic), magma version, and
   **Last validated** (wall-clock, local time — when magma last confirmed this map
   matches the repo, whether by rebuild or fresh-check). This is the sole
   non-deterministic field and it lives only here, never in the JSON.
2. **Size** — functions (nodes), methods vs funcs, calls (edges) with static/dynamic
   split, packages.
3. **Reachability** — reachable / production-reachable / dead / test-only counts,
   each linking to its index note. A `> [!caution]` callout if dead > 0.
4. **Surface** — exported function count; entry points (each `main` linked to its note).
5. **Hotspots / heat** — a native **mermaid `pie`** chart showing call concentration:
   the top-N most-called functions (by in-degree) as slices plus an `(others)` slice
   for the remaining inbound calls, so a reader instantly sees whether a few functions
   absorb most of the graph's calls. Because mermaid slices aren't clickable, it is
   paired with compact ranked lists (most-called by in-degree, most-calling by
   out-degree) of `[[links]]` + counts for navigation. `N` is configurable via
   `--hotspots N` (default 10). This is a "things the graph can tell us" metric that
   reading cannot cheaply produce. (The reachability composition in §3 can likewise be
   a mermaid pie.)
6. **Graph view** — always a recipe (works in core Obsidian): "open the graph
   (⌘/Ctrl-G) and paste this filter to scope it to this map: `path:"<folder-name>/nodes"`"
   (a substring match, robust to where the vault root sits). When `--graph-link` is
   passed, additionally emit a one-click `obsidian://…graph:open` deep link (requires
   the Advanced URI plugin; omitted by default since a dead link is worse than none).
7. **Fidelity, explained** — plain English: "Edges are an **RTA call graph**: direct
   calls are exact; dynamic calls through interfaces or function values are
   approximated." Resolves the "`rta` is opaque" problem.
8. **Refusals** — if the graph or a view refused, a `> [!failure]` callout with the
   reason (never a silent empty).

A small mermaid graph shows entry points → the packages they reach (top level only,
so it stays legible even for large repos).

## Per-function note

Path `nodes/<pkg-rel>/<Symbol>.md`, where `<pkg-rel>` is the module-relative package
path (e.g. `internal/backend`) and `<Symbol>` is the pretty name (`BuildGraph`,
`Meta.Computed`). Within a Go package these names are unique, so the path is unique.

Frontmatter:
```yaml
---
pkg: github.com/robot-accomplice/magma/internal/backend
file: internal/backend/golang.go
line: 37
kind: method            # func | method
exported: true
tags: [magma/node]      # plus magma/dead, magma/test-only, magma/root, magma/generated as applicable
---
```

Body:
```markdown
`internal/backend/golang.go:37`

## Calls
- [[internal/backend/load]]
- [[internal/backend/collectNodes]]
- ...
```

- **Calls** are the node's outgoing edges (deduped), each a path-suffix wikilink so
  Obsidian resolves it unambiguously. A dynamic edge is annotated `(dynamic)`.
- **"Called by"** needs no section — Obsidian **backlinks** provide it for free.
- If a callee's note was not generated (a narrowed `--depth`/`--from` run), the link
  is an unresolved "ghost" note — a visible signal that the graph continues.
- No code bodies. The `file:line` is the pointer to the source.

## Index notes

- `Dead code.md` / `Test-only code.md`: one bullet per symbol. Links to the function
  note when it exists; otherwise `file:line` text. Each opens with a one-line
  explanation of what the class means and the candidate-not-finding caveat.
- `Packages.md`: packages as `[[links]]`/headings, each listing its function notes,
  so the vault is browsable by package.

## The walk & CLI flags

- **Default (no walk flags):** a note for **every** function in the module — the
  complete graph. This is "default is all".
- `--depth N`: switch to a forward walk from entry points, emitting notes only for
  functions within N call-levels. Indexes still list everything.
- `--from <symbol>`: root the walk at a specific function instead of the entry
  points (combines with `--depth`).
- Existing `--force` (rebuild despite freshness) unchanged.

Dead code is unreachable from entry points, so under `--depth`/`--from` it is
index-listed only; under the default "all" it gets a note like any other function.

## Regeneration (reconciliation)

`.magma/manifest.json` records every md path magma wrote this run. On the next run,
magma deletes md files listed in the prior manifest that it did not produce again
(stale nodes), and **never** touches files it didn't write (human/foreign notes are
reported, not deleted). Writes are contained to `<folder>` — a note path that is a
symlink or escapes the folder is refused. This mirrors the reconciliation the old
codemap proved out.

## Freshness

Unchanged in spirit: a fresh map (same clean tree + same magma version, all expected
outputs present) skips the expensive rebuild. The freshness check reads
`.magma/graph.json` (moved from the folder root). "Expected outputs present" now
includes `Overview.md` and the manifest.

On a fresh-skip, magma still re-renders **only `Overview.md`** from the cached
`.magma/graph.json` (parse + render, no analysis) to bump the "Last validated"
timestamp — so the dashboard honestly reflects "confirmed current at now" while the
graph itself (and every other note) is untouched.

## Determinism

The graph and every note *except* the `Overview.md` "Last validated" line are
deterministic: no wall-clock time, stable ordering (sorted nodes/edges, sorted bullet
lists). Commit date comes from `git show -s --format=%cI` on HEAD (a property of the
commit, deterministic for a SHA), surfaced via `gitmeta`. Same repo + SHA → byte-
identical JSON and byte-identical notes, with the single, deliberate, clearly-labeled
exception of the Overview's build timestamp. The freshness check keys only on the
deterministic JSON (`tree` + `generator`), so the timestamp never affects skip logic.

## CLI / report changes

- New flags: `--depth N`, `--from <symbol>` (walk narrowing), `--hotspots N`
  (dashboard hotspot count, default 10), `--graph-link` (emit the one-click
  Advanced-URI graph link). Help text + README updated.
- The terminal panel gains a `notes` count and de-jargons fidelity to
  `RTA call graph` with the one-line explanation as a footnote.

## Testing

- **Pure renderers** (the bulk): functions that turn a `contract.Graph` (+ views)
  into each markdown artifact are pure `Graph -> string` (or `-> map[path]string`)
  and unit-tested against small fixtures — exact note bodies, frontmatter, link
  targets, dashboard metrics (including hotspot ordering), refusal callouts.
- **Naming/linking**: uniqueness and path-suffix link resolution tested on a fixture
  with same-named symbols across packages.
- **Walk**: `--depth`/`--from` selection tested on a fixture with a known call chain.
- **Reconciliation**: write map, remove a function, regenerate, assert the stale note
  is deleted and a planted foreign note survives.
- **Determinism**: render twice, assert byte-identical.
- Coverage stays >= 80%.

## Architecture / boundaries

New package `internal/notes` (the markdown renderer): depends only on `contract`
(entities), never on `backend`/`main`. `main` orchestrates: build graph -> write
`.magma/*.json` -> render notes -> reconcile via manifest -> print panel. This keeps
the renderer a pure, independently-testable unit (Clean Architecture: presentation
adapter over the entity graph).

## Scope / versioning

Fold into the (unreleased) v0.1.0 — a vault tool invisible in the vault isn't done.
JSON wire contracts unchanged. A `magma-notes/1` marker in `Overview.md` frontmatter
versions the note format for the future.

## Known limitations (v1, labeled not hidden)

- Cross-map link ambiguity: two maps in one vault sharing a `<pkg-rel>/<Symbol>`
  suffix could resolve to either. Acceptable for v1; a later version can prefix links
  with the folder name.
- The mermaid dashboard graph is entry-point→package only; the full graph lives in
  Obsidian's own graph view over the notes.
