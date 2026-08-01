# Task: make a dirty-tree map's provenance honest

**Status: not started. Shape approved by Jon 2026-07-31. Gated on Architext's ack — this changes a
required field they read.**

## The defect, verified

Reported by the roboticus/slop-ferret consumer and confirmed against the repo:

```
c48904b9  does NOT resolve      f449ae06  does NOT resolve     <- maps built from dirty trees
bd8afdd6  RESOLVES              ca0cb873  RESOLVES             <- maps built from clean trees
```

A consumer pins a record to `sha` — "this sweep covered the map at X; re-derive by checking out X
and re-running." When the tree was dirty the map was built from X **plus uncommitted changes**, so
that promise cannot be kept. Empirically it is worse than theoretical: dirty means work in flight,
and in-flight commits get amended or rebased away, so a dirty map's `sha` is disproportionately
likely to stop resolving entirely.

magma already records the truth in `tree: "c48904b9-dirty"`. Consumers gate on `sha`, not `tree`,
so they record an unreachable boundary and never notice.

## The second defect, found while discussing the first

`gitmeta.go:36-39`:

```go
tree := sha
if strings.TrimSpace(status) != "" {
    tree = sha + "-dirty"
}
```

Nothing about the working tree's CONTENT enters the stamp. **Two dirty runs at the same commit
mapping different code produce byte-identical `sha` AND `tree`.** They are indistinguishable.
Architext's layout cache is keyed on `(sha, tree, tier)`, so two such maps collide in it today.

## The shape

When the tree is dirty, `sha` becomes `<sha>+<diffhash>`. Clean trees are completely unchanged.

That buys three things at once:
- **Not mistakable for a git object.** A consumer that tries to resolve it fails, rather than
  silently recording an unreachable boundary. Fail-closed at use-time.
- **Distinguishable.** Different dirty state gives a different id, so the Architext cache collision
  above goes away.
- **Still provenance.** The base commit is right there in the string.

**A content hash, not a wall-clock timestamp** — considered and rejected for the identity field.
A timestamp makes two runs of an *untouched* dirty tree produce different artifacts, breaking the
byte-identical property this project has verified repeatedly and making the Architext cache miss
every time. A diff hash is deterministic for a given working-tree state, which is exactly the
property wanted.

If "the moment in time" is also wanted, it belongs in **its own field**, not inside `sha`. There is
no run-timestamp field today: `contract.Meta` carries `CommitDate`, which is the *commit's* date,
not the run's. Adding one is a further two-sided change.

## Implementation notes — the non-obvious parts

**1. The hash input must honour the same exclusions as the dirty check.** `Load(repo, ignore...)`
already excludes magma's own artifacts from `git status`, and `gitmeta.go:16-19` explains why: so
magma's own output never makes the NEXT run see a dirty tree and flip the stamp. **The hash must
apply the identical exclusions**, or magma's own writes change the hash on every run and reintroduce
precisely that bug one layer down.

**2. Untracked file content is the hard part, and must not be papered over.** `git diff HEAD` does
not cover untracked files at all, and `git status --porcelain` names them without their content —
so a new untracked `.go` file (which WILL be compiled into the map) would not move the hash if you
only hash those two. Either:
   - hash `status --porcelain` + `diff HEAD` and **document in the field's doc comment** that
     untracked file content is not covered, only its presence; or
   - additionally hash the contents of `git ls-files -o --exclude-standard`, which closes it.

   Pick one deliberately and say which. A silent partial hash is the failure mode this whole
   project exists to avoid.

**3. Separator.** `+` is chosen because it cannot appear in a hex sha and does not collide with the
existing `-dirty` convention on `tree`. Keep `tree` as `<sha>-dirty` unless there is a reason to
move it too — one identity field changing is easier for consumers to absorb than two.

## Gate before landing

`sha` is a required field Architext reads. By this project's own governance rule — new/changed
**field** semantics is two-sided, new **values** are one-sided — this needs their ack first. It is
arguably a value change rather than a field change, since the field stays present and stays a
string; say so when asking, and let them decide whether their validator cares.

The roboticus consumer should be told too: their gate reads `sha` and would start recording a
composite. That is the intended behaviour, but it should not surprise them.
