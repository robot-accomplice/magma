# Task: rust-helper structural debt — do this BEFORE the next family

**Status: not started. Blocking-ish — every new family costs more than the last.**

Split out of the Phase B handover on 2026-07-31 because it is snowballing: the fixes are landing
faster than the structure is absorbing them, and this session added to the problem twice while
closing families B/F/G.

## The evidence, not the impression

```
file           total  comment   code   cmt%        longest fn
enumerate.rs    1067      390    677    36%    push()        178 lines
walk.rs          933      325    608    34%    walk()        316 lines
main.rs          912      292    620    32%    analyze_one() 173 lines
roots.rs         267      161    106    60%
model.rs         259      142    117    54%
```

**The snowball, concretely.** `walk()` is a flat chain of `if let Some(x) = ast::X::cast(n.clone())`
blocks, one per desugaring form, in a single 316-line function. Every family bolts another block
onto it:

| session | what it added to `walk()` |
|---|---|
| family B (first pass) | operators, `await`, `for`, format args — 4 blocks |
| family A | function-as-value — 1 block |
| family D | a `truncated: &mut bool` parameter threaded through the recursion |
| this session, family B residue | `?` conversion, `Drop` — 2 more blocks, plus `BodyCtx` |

`BodyCtx` was introduced **this session specifically to stop `walk()` growing an 11th positional
parameter**. That is the pressure showing through: a partial, local fix applied because the real
one was out of scope. The next family will face the same choice.

## The three specific debts

**1. `walk()` at 316 lines, one block per form.** The dispatch is flat and order-dependent, every
form shares one scope, and the macro-descent recursion threads everything through by hand. This is
the most-modified code on the branch.

**2. `push` / `push_const` / `push_static` are near-copies** — 178 / 98 / 83 lines, the two `push_*`
differing by only 47. The justification is still in the source:

> *"a concurrently active task's `walk.rs` desugaring work, and keeping this function untouched
> avoids any merge risk. The small duplication of the traversal shape ... is the accepted cost."*

**That reason is dead.** It was a parallel-agent scheduling artifact from a session that ended days
ago. The duplication is now permanent and justified by nothing. Any node-metadata change (this
session's Family F relocation, `test_entry`) has to be made in three places and kept consistent by
hand — `test_entry` genuinely was, and a fourth caller would have been missed.

**3. Four functions carry `#[allow(clippy::too_many_arguments)]`, two added this session.** The
signatures thread `db, sema, vfs, root, target_dir` positionally everywhere. Silencing the lint was
the wrong call and is called out here rather than left looking considered: the lint was correct.

## What is NOT a problem

The 32–60% comment density. It carries measured numbers, rejected alternatives, and why-not-X
reasoning that has nowhere else to live, and it is the reason this branch has been auditable at all.
**Do not "clean up" the comments as part of this task.** If anything the ratio argues for moving
design rationale into these plan docs, which is a separate call.

## Constraints — this is a refactor, so the bar is "provably nothing changed"

- **The oracle gate is the safety net and must stay untouched.** 16/16 fixtures, byte-identical
  output before and after. Capture a full helper JSON for several fixtures first and diff it.
- **No behaviour change, no new families, no exclusion movement** in the same task. If a defect is
  found mid-refactor, stop and land it separately with its own RED-first fixture.
- **Do not reformat wholesale.** A mass `cargo fmt` was applied on 2026-07-31 (`a291feb`) to make
  CI gateable; the user's call was that reformatting files you did not write is not on. Restructure
  the code, and let the formatting fall out of that — do not run the formatter as a change of its own.
- Re-verify against `roboticus-rust` (10,921 functions, 24,611 calls) at the end: node/edge counts
  and the emitted set must match exactly.

## Note on `a291feb`

Left in place. Reverting it conflicts against three later commits and would ADD a second 392-line
formatting diff rather than remove one; nothing was pushed, so dropping it from history is possible
but was not done without approval. **This task supersedes the question for `walk.rs`/`enumerate.rs`,
since it rewrites them anyway.** The CI `cargo fmt --check` gate depends on the tree staying
formatted — if `a291feb` is dropped, drop that gate step with it.

## Suggested sequencing

1. Snapshot helper output for every fixture (the diff target).
2. `push`/`push_const`/`push_static` first — the duplication is mechanical and the win is immediate.
3. The `db/sema/vfs/root/target_dir` context struct — removes all four `#[allow]`s.
4. `walk()` last, and only then: split the per-form dispatch so a family is an addition rather than
   an edit to a 316-line function.
5. Re-run the gate and the roboticus comparison; the emitted graph must be identical.
