# Task: emit `_interfaces.json` and `_duplicates.json` row files

**Status: not started. Requested by the roboticus/slop-ferret consumer, approved by Jon on
2026-07-31 — slop-ferret's family lexicon is meant to grow over time, and these two families
currently cannot be map-seeded at all.**

## Why

slop-ferret's gate (`~/.claude/skills/slop-ferret/scripts/gate.py`) expects four row files at the
map root; magma emits two. The consequence is not cosmetic: the gate is what makes the family
worklist FAIL-CLOSED ("prove you looked"), so a sweep that cannot read them cannot enforce it. The
roboticus v1.7.0 sweep had to say so on the face of its report and skipped families D and E
entirely.

| file | magma today | family it seeds |
|---|---|---|
| `_dead.json` | ✅ emitted | — |
| `_test-only.json` | ✅ emitted | — |
| `_duplicates.json` | ❌ never emitted | D — duplicated implementation |
| `_interfaces.json` | ❌ never emitted | E — single-implementation interface |

The consumer explicitly did NOT fabricate the missing two, on the grounds that inventing map rows
is faking a map. That was the right call and is the standard this task has to meet: a row file that
is present but wrong is worse than one that is absent.

## Location — decided

Row files stay under `<vault>/<name>/.magma/`. `main.go:21` already documents that path as the one
"the audit gate reads directly", and the `.magma/` prefix is what keeps machine files out of the
Obsidian vault view. **`gate.py` moves to read `.magma/`, magma does not move.** Confirmed with the
consumer's own framing: "either side can move; I need to know which you intend."

## `_interfaces.json` — tractable, do this first

Family E is "an interface with exactly one implementation", which is a real slop signal: the
abstraction is not earning its keep.

The Go backend already has everything needed and throws it away. `load()` uses
`packages.LoadAllSyntax`, so full `types` info is present, and `golang.go` already reaches into
`types.Named`/`types.Pointer` for receiver naming. Implementation counting is
`types.Implements(T, iface)` over the module's own named types.

Design decisions to make explicitly, not by accident:
- **Scope to the module's own interfaces and own implementors** (the same `inModule` filter node
  collection already applies). An interface implemented once locally but also by a dependency is
  not a single-impl interface.
- **Zero implementations is not the same finding as one** — decide whether both belong in this file
  or only exactly-one, and say which in the note.
- `Row` is currently `{symbol, file, line}`. A single-impl interface row wants to name the
  implementor too. **Extending `Row` is a contract change** — `codemap-rows/1` would need a bump,
  and the consumer's gate fails CLOSED on an unknown contract string, which reads as a total outage
  until they add it. Either keep to the existing three fields (implementor discoverable via the
  graph) or coordinate the bump with them FIRST. Do not bump silently.

## `_duplicates.json` — harder, and do not fake it

Family D is "duplicated implementation". **magma has no notion of similarity today**, and inventing
one badly is the failure mode this whole project exists to avoid: a duplicate row that is not
actually a duplicate is a refactor order for code that should be left alone, the same class of
harm as a false dead-code row being a deletion order.

Before writing any detector, decide what "duplicate" means and write it into the note:
- Identical AST modulo identifiers? Identical token stream? A similarity threshold?
- A threshold means false positives are tunable rather than absent — that needs an explicit
  precision/recall stance, and a stated bar the consumer can weight against (their gate already
  weights a candidate's pre-filing bar heavier when `fidelity` is weaker).
- **`fidelity` is load-bearing here, not decorative.** If duplicate detection is fuzzier than the
  call graph, that must be visible in the file's own `fidelity` value rather than inheriting `rta`.

If a defensible metric cannot be settled, emitting nothing remains correct. Shipping
`_interfaces.json` alone is a real improvement and unblocks family E; a bad `_duplicates.json`
would be a regression.

## Constraints

- **Both files must carry the same envelope as the existing two** — `contract_version`, `sha`,
  `tree`, `fidelity`, `reachability_computable`, `generator`, `rows[]`. The consumer's gate reads
  exactly those.
- **Do not bump `codemap-rows/1` without telling the consumer the new string first.** Their gate
  fails closed on an unknown contract, which is correct behaviour and also means a bump reads as an
  outage until they add the value.
- A refused/non-computable map must emit these two the same way it emits the existing two, so the
  gate sees a consistent set rather than a partial one.

## Related

The consumer also reported that dirty-tree maps record a `sha` that cannot be returned to (verified:
`c48904b9` and `f449ae06` no longer resolve, while the clean `bd8afdd6`/`ca0cb873` do). That is a
separate decision, pending, and needs Architext's ack because `sha` is a required field they read.
