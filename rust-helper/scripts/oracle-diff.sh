#!/usr/bin/env bash
# Compare the helper's derived dead set against rustc's dead_code lint — the
# measuring instrument for magma's Rust/Go parity claim.
#
# Divergence directions are NOT equally severe:
#   helper says dead, oracle says live -> FALSE DEAD CODE (fatal)
#   helper says live, oracle says dead -> conservative (report only)
#
# The helper emits nodes+edges only, never reachability (see model.rs); this
# script computes reachability itself (BFS from `root` functions over `calls`)
# and is therefore the only place that derives a dead set to compare.
#
# REFUSES (exit 5) if `cargo check` emits zero `compiler-message` entries.
# `cargo check` only diagnoses crates it recompiles; on a warm `target/` it
# silently replays cached artifacts and says nothing at all — indistinguishable
# from a genuinely clean workspace unless this is checked explicitly. Reporting
# agreement in that case would be a false green. See the refusal branch below.
#
# Normalisation (functions the oracle cannot render a verdict on, so they are
# excluded from BOTH directions, counted, and reported — never silently
# dropped):
#   - generated:true (helper-flagged build-script/macro output)
#   - test:true (a #[test] item: with cfg(test) off, `cargo check` never
#     compiles it at all, so the lint is silent on it either way)
#
#   NOTE: an earlier revision also excluded any function whose module path
#   contained a `test`/`tests` segment, as a heuristic for code nested inside
#   a #[cfg(test)] module. Removed: it fired on none of the three required
#   fixtures (zero empirical coverage) and a targeted repro
#   (`mod tests { pub fn genuinely_live_helper() }` called from `main`, with
#   NO #[cfg(test)] gate) showed it silently excluding a genuinely-live
#   production function purely on module-name coincidence — an invisible
#   false negative, the dangerous direction. A visible FATAL for a real
#   #[cfg(test)]-gated-but-not-#[test]-attributed helper is preferred over
#   silently hiding a wrong exclusion; that case is not currently normalised
#   and will show as report-only or FATAL depending on reachability.
#
#   - source carries #[allow(...dead_code...)], #[no_mangle], #[used], or
#     #[export_name...] immediately above the function (attributes that make
#     genuinely-dead code invisible to the lint by design)
#   - a trait-impl method whose enclosing trait is independently reported
#     dead_code by the oracle, AND (the self type is independently reported
#     dead_code too, OR the self type cannot receive its own dead_code
#     diagnostic in the first place — a builtin like `i32`, or a type defined
#     outside this workspace). Empirically verified (isolated cargo-check
#     probes, not assumed): rustc's dead_code lint DOES flag an individually-
#     dead trait-impl method when its trait and type are otherwise live, but
#     SUPPRESSES that per-method diagnostic when the trait+type cluster is
#     itself already unreachable (reports only the trait/type, not each
#     method — cascade suppression, presumably to avoid redundant noise once
#     the whole cluster is flagged). Inherent-impl methods do NOT get this
#     treatment (verified: struct+method both reported).
#
#     Task 13: this used to key on the *bare name* of the trait/type scraped
#     from rustc's diagnostic text via a source-scanning brace matcher here.
#     Names collide within a crate (two `impl X for Y` blocks named `Amb` in
#     different modules is enough), so a name-keyed gate could suppress a
#     genuinely-live method that merely shared a name with a dead one — the
#     exact divergence-hiding failure this measuring instrument exists to
#     catch. The sound key is the (file, line) of the trait's and self type's
#     own declarations, which rustc's diagnostics and the helper's `helper.json`
#     (`functions[].trait_impl`, emitted by `enumerate.rs`) both address the
#     same way and which cannot collide. That field also carries whether the
#     self type is a workspace-local item at all (`self_type_decl` is absent
#     for a builtin/foreign self type) — the builtin case a bare-name AND-gate
#     could never satisfy, since e.g. `i32` never gets its own "never used"
#     diagnostic no matter how dead the impl is. With the field present, no
#     source scanning is needed here at all: the (file, line) keys are looked
#     up directly against the same oracle-dead-span map used for the ordinary
#     per-function check.
set -euo pipefail

REPO="${1:?usage: oracle-diff.sh <workspace-root>}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HELPER="$(cd "$SCRIPT_DIR/.." && pwd)/target/release/magma-rust-helper-spike"
REPO_ABS="$(cd "$REPO" && pwd)"

if [[ ! -x "$HELPER" ]]; then
  echo "error: helper binary not found at $HELPER — run 'cargo build --release' first" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "== running helper on $REPO_ABS ==" >&2
# H6: the helper's documented no-roots refusal exits 2. Under `set -e`, a
# direct (non-subshell, non-conditional) invocation that exits non-zero
# kills the script right here, before the `.computable == false` check
# below ever runs — so the dedicated rc-3 refusal path was unreachable and
# a refusal instead surfaced as an unexplained script abort. `set +e`
# around just this invocation lets the exit code be inspected explicitly.
set +e
"$HELPER" "$REPO_ABS" >"$WORK/helper.json" 2>"$WORK/helper.stderr"
helper_rc=$?
set -e
cat "$WORK/helper.stderr" >&2

if jq -e '.computable == false' "$WORK/helper.json" >/dev/null 2>&1; then
  echo "helper refused: $(jq -r '.reason' "$WORK/helper.json")"
  exit 3
fi

if [[ $helper_rc -ne 0 ]]; then
  echo "" >&2
  echo "error: helper exited $helper_rc without a computable:false refusal envelope —" >&2
  echo "an unexpected failure (crash, panic, bad args), not a documented refusal." >&2
  exit 1
fi

# H8: executed_target_code was emitted but nothing ever checked it. If build
# scripts didn't run and/or the proc-macro server didn't expand macros, any
# node or edge sourced from that generated code is unverified this run — a
# silent gap this harness's whole job is to surface, not absorb.
if ! jq -e '.executed_target_code == true' "$WORK/helper.json" >/dev/null 2>&1; then
  echo "" >&2
  echo "REFUSED: helper reports executed_target_code=false — build scripts did not run" >&2
  echo "and/or the proc-macro server did not expand macros this run. Any dead_code" >&2
  echo "claim touching macro- or build-script-generated code is unverifiable, so this" >&2
  echo "comparison cannot be trusted." >&2
  exit 8
fi

echo "== running oracle (cargo check) on $REPO_ABS ==" >&2
# -v (verbose) makes cargo print one "Fresh <pkg>" / "Compiling <pkg>" /
# "Checking <pkg>" progress line per unit to stderr, alongside the JSON
# stream on stdout. This is the ONLY reliable signal for "did rustc actually
# run for this crate this invocation" — message-format=json output cannot
# tell you that (see the freshness check below for why).
# --all-targets (H4): plain `cargo check --workspace` compiles only default
# targets (lib/bin), skipping examples/, tests/, benches/ — but the helper
# enumerates functions from ALL of them. That mismatch produced both false
# FATALs (helper enumerates a fn in an example the oracle never compiled, so
# it can never appear in oracle-dead.jsonl) and fake agree_live (same fn,
# reachable/dead status never actually verified by rustc). --all-targets
# closes the gap by widening the oracle to match the helper's enumeration
# scope, rather than narrowing the comparison with a new exclusion.
set +e
( cd "$REPO_ABS" && cargo check --workspace --all-targets -v --message-format=json 2>"$WORK/cargo.stderr" ) \
  > "$WORK/cargo.jsonl"
cargo_rc=$?
set -e
tail -20 "$WORK/cargo.stderr" >&2 || true

# H2: the oracle's own exit status used to be discarded (`|| true`), and
# nothing examined whether cargo check actually completed. rustc aborts
# before the dead_code lint pass runs at all on a type/compile error, so a
# non-compiling crate emits ZERO dead_code diagnostics — every enumerated
# function would then be booked agree_live, a false green for the one tool
# whose job is measuring parity, not manufacturing it. Refuse unless cargo
# check both exited zero AND emitted no `level:"error"` compiler-message
# (belt-and-suspenders: some configurations can report errors without a
# nonzero process exit).
cargo_error_count="$(jq -r 'select(.reason=="compiler-message") | select(.message.level=="error") | 1' \
  "$WORK/cargo.jsonl" 2>/dev/null | wc -l | tr -d ' ')"
if [[ $cargo_rc -ne 0 || "${cargo_error_count:-0}" -gt 0 ]]; then
  echo "" >&2
  echo "REFUSED: cargo check did not complete cleanly (exit $cargo_rc, ${cargo_error_count:-0}" >&2
  echo "error diagnostic(s)). rustc aborts before the dead_code lint pass on a compile" >&2
  echo "error, so a non-compiling crate emits ZERO dead_code diagnostics and every" >&2
  echo "enumerated function would be booked agree_live — a false green for the tool" >&2
  echo "whose job is measuring parity, not manufacturing it." >&2
  exit 6
fi

# A crate cargo considers "Fresh" (fingerprint unchanged since the last
# check) is never handed to rustc at all this invocation — cargo just
# replays that fact, silently. It emits NO compiler-message for it (not even
# "still clean"), but it can *also* emit no compiler-artifact/other-reason
# difference either — verified directly: testdata/multi_impl is a
# genuinely warning-free crate, and a truly FRESH `cargo check` on it also
# produces zero `compiler-message` entries (nothing was ever wrong to
# report), so counting compiler-message alone cannot distinguish "just
# checked, clean" from "never re-checked, silently cached" — both look
# identical on stdout. Only -v's stderr progress line tells them apart:
# "Checking multi_impl ..." (real compile happened) vs "Fresh multi_impl ..."
# (skipped, nothing this run confirms or denies about its dead_code status).
#
# So the actual freshness check is per *workspace-local package*: every
# workspace member must show a "Compiling"/"Checking" line this run, or its
# functions' dead_code silence is not evidence of anything.
workspace_packages="$(cd "$REPO_ABS" && cargo metadata --no-deps --format-version=1 2>/dev/null | jq -r '.packages[].name')"
checked_packages="$(awk '/^[[:space:]]*(Compiling|Checking)[[:space:]]+/ { print $2 }' "$WORK/cargo.stderr" | sort -u)"

stale_packages=""
while IFS= read -r pkg; do
  [[ -z "$pkg" ]] && continue
  if ! grep -qxF "$pkg" <<<"$checked_packages"; then
    stale_packages="$stale_packages$pkg"$'\n'
  fi
done <<<"$workspace_packages"

if [[ -n "$stale_packages" ]]; then
  echo "" >&2
  echo "REFUSED: at least one workspace-local crate was NOT actually recompiled" >&2
  echo "by cargo check this run — cargo replayed a cached (\"Fresh\") result for" >&2
  echo "it instead of invoking rustc, so its dead_code status this run is" >&2
  echo "UNKNOWN, not confirmed clean. This is not the same as \"the compiler" >&2
  echo "found nothing wrong\": a genuinely fresh, warning-free crate ALSO emits" >&2
  echo "zero compiler-message entries, so message-count alone cannot tell a" >&2
  echo "trustworthy clean result apart from a silently-skipped one — only" >&2
  echo "cargo's own \"Fresh\" vs \"Checking\" progress line can. Reporting" >&2
  echo "agreement here would be a false green for the one tool whose job is" >&2
  echo "measuring parity, not claiming it." >&2
  echo "" >&2
  echo "Stale (not recompiled this run):" >&2
  echo "$stale_packages" | sed 's/^/    /' >&2
  echo "Fix: force fresh compilation, then re-run this harness against the same" >&2
  echo "workspace:" >&2
  echo "    cargo clean --manifest-path \"$REPO_ABS/Cargo.toml\"" >&2
  echo "(whole workspace — \"cargo clean -p <crate>\" is not sufficient, it only" >&2
  echo "clears one crate and leaves the rest of the dependency graph cached, which" >&2
  echo "is often enough to leave the workspace crates themselves stale too.) This" >&2
  echo "is expensive on a large workspace: a cold \`cargo check\` can take minutes" >&2
  echo "to tens of minutes. That cost is the price of a trustworthy oracle." >&2
  exit 5
fi
# `wc -l <<<""` reports 1, not 0 (the heredoc still supplies one trailing
# newline for an empty string) — counting non-empty lines directly avoids
# misreporting "1 workspace-local crate" when there are none.
workspace_package_count="$(grep -c . <<<"$workspace_packages" || true)"
echo "oracle: all ${workspace_package_count:-0} workspace-local crate(s) actually recompiled this run — proceeding" >&2

# H7: a crate-level `#![allow(dead_code)]` or `#![allow(unused)]` silences
# rustc's dead_code lint for the ENTIRE crate. The FATAL direction still
# fails safe (a genuinely-dead function the helper flags just has no oracle
# diagnostic to agree with, so it surfaces as a spurious FATAL rather than a
# silent false green), but report_only collapses to nothing and agree_live
# is manufactured from an oracle that was told to say nothing at all — not
# real parity evidence. This does NOT exclude anything (that would repeat
# the exact mistake H1 exists to fix); it only discloses the fact loudly so
# a reader of this crate's report knows what its counts do and don't prove.
echo "== scanning for crate-level dead_code suppression ==" >&2
crate_level_allow="$(cd "$REPO_ABS" && cargo metadata --no-deps --format-version=1 2>/dev/null \
  | jq -r '.packages[].targets[] | select(.kind[] as $k | $k=="lib" or $k=="bin") | .src_path' \
  | sort -u \
  | while IFS= read -r src; do
      if [[ -f "$src" ]] && grep -qE '^[[:space:]]*#!\[[[:space:]]*allow\([^)]*(dead_code|unused)' "$src"; then
        echo "$src"
      fi
    done)"
if [[ -n "$crate_level_allow" ]]; then
  echo "" >&2
  echo "DISCLOSURE: crate-level #![allow(dead_code)] or #![allow(unused)] found. The" >&2
  echo "oracle's dead_code lint is suppressed for the WHOLE crate, so report_only/" >&2
  echo "agree_live counts touching it are not independent evidence of parity — only" >&2
  echo "that the oracle was told to say nothing. FATAL still fires normally." >&2
  echo "$crate_level_allow" | sed 's/^/    /' >&2
fi

# Oracle dead set, keyed by "file:line:column" of each dead_code diagnostic's
# primary span — column, not just line, because two distinct declarations can
# legitimately share ONE source line (a trait's methods all declared inline:
# `trait Tr { fn a(&self); fn b(&self); }`) and rustc still gives each its own
# diagnostic with a distinct column pointing at that exact name token.
#
# This used to be file:line alone, which is unsound for the same reason two
# earlier revisions of the trait-impl cascade check were unsound (see the
# header comment above): keyed on the wrong granularity, a diagnostic whose
# span merely LANDS on the same line as some other declaration can be
# misread as being ABOUT that other declaration. Verified directly with a
# one-line trait body (`trait Tr { fn live_m(&self); fn dead_m(&self); }`):
# the `dead_m` diagnostic's primary span is `{line: 2, column: 41}` — line 2
# is ALSO the trait's own declaration line (`Tr` itself starts at column 15
# on that same line) and, in a hand-written multi-method-per-line trait, the
# line of an unrelated LIVE method's declaration too. A file:line-only key
# would treat that diagnostic as evidence about any of them; file:line:column
# can only ever match the one declaration whose own name token starts there.
# `enumerate.rs` emits each function's own (and each trait-impl's trait/self-
# type's own) column for exactly this purpose — see `model::Function::column`
# and `model::Loc`.
jq -c 'select(.reason=="compiler-message")
       | select(.message.code.code=="dead_code")
       | .message.spans[]? | select(.is_primary)
       | {file: .file_name, line: .line_start, column: .column_start}' \
  "$WORK/cargo.jsonl" > "$WORK/oracle-dead.jsonl"
jq -s '.' "$WORK/oracle-dead.jsonl" > "$WORK/oracle-dead.json"

# Attribute-based normalisation: for every non-generated, non-test function,
# look at the source lines immediately above its declaration line for a
# contiguous run of attributes/doc-comments, and flag it if that run contains
# a liveness-affecting attribute. Grouped by file so each file is read once.
#
# The trait-impl cascade-suppression check needs NO source scanning — Task 13
# moved that to `enumerate.rs`, which emits each trait-impl method's own
# `trait_impl.{trait_decl,self_type}` (file, line, column) directly in
# helper.json, computed from rustc's own HIR rather than scraped from source
# text. oracle-diff.jq looks those up against the same column-precise
# oracle-dead-span map built above — no separate scan, no name matching, and
# (per the column-precision above) no line-sharing collision either.
echo "== scanning for liveness-affecting attributes ==" >&2
jq -r '.functions[] | select(.generated | not) | select(.test | not)
       | [.id, .file, .line] | @tsv' "$WORK/helper.json" \
  | sort -t $'\t' -k2,2 -k3,3n > "$WORK/scan-targets.tsv"

: > "$WORK/excluded-attrs.jsonl"
current_file=""
lines=()
while IFS=$'\t' read -r id file line; do
  if [[ "$file" != "$current_file" ]]; then
    lines=()
    if [[ -f "$REPO_ABS/$file" ]]; then
      while IFS= read -r l || [[ -n "$l" ]]; do
        lines+=("$l")
      done < "$REPO_ABS/$file"
    fi
    current_file="$file"
  fi

  attrs=""
  i=$((line - 2)) # 0-based index of the source line just above the fn line
  while (( i >= 0 )); do
    l="${lines[$i]:-}"
    # H3: a doc comment is walked PAST (so an attribute sitting above a run
    # of doc comments is still found) but never admitted into `attrs` — the
    # prior version matched `///`/`//!` into the same bucket as `#[...]`,
    # so a doc comment merely MENTIONING e.g. "#[allow(dead_code)]" in
    # English prose silently excluded that function from both comparison
    # directions.
    if [[ "$l" =~ ^[[:space:]]*#\[ ]]; then
      attrs="$l"$'\n'"$attrs"
      i=$((i - 1))
      continue
    elif [[ "$l" =~ ^[[:space:]]*(///|//!) ]]; then
      i=$((i - 1))
      continue
    fi
    break
  done
  reason=""
  if [[ "$attrs" =~ \#\[[[:space:]]*allow\([^\)]*dead_code ]]; then
    reason="allow(dead_code)"
  elif [[ "$attrs" =~ \#\[[[:space:]]*allow\([^\)]*unused ]]; then
    # H7: `#[allow(unused)]` is the lint GROUP containing dead_code (a
    # narrower `#[allow(dead_code)]` is also still matched above), and was
    # missing from this list entirely.
    reason="allow(unused)"
  elif [[ "$attrs" =~ \#\[[[:space:]]*no_mangle ]]; then
    reason="no_mangle"
  elif [[ "$attrs" =~ \#\[[[:space:]]*used ]]; then
    reason="used"
  elif [[ "$attrs" =~ \#\[[[:space:]]*export_name ]]; then
    reason="export_name"
  fi
  if [[ -n "$reason" ]]; then
    jq -cn --argjson id "$id" --arg reason "$reason" '{id: $id, reason: $reason}' \
      >> "$WORK/excluded-attrs.jsonl"
  fi
done < "$WORK/scan-targets.tsv"
jq -s '.' "$WORK/excluded-attrs.jsonl" > "$WORK/excluded-attrs.json"

echo "== computing reachability and diffing against oracle ==" >&2
jq -nr \
  --slurpfile helper "$WORK/helper.json" \
  --slurpfile oracle "$WORK/oracle-dead.json" \
  --slurpfile excluded "$WORK/excluded-attrs.json" \
  -f "$SCRIPT_DIR/oracle-diff.jq" | tee "$WORK/report.txt"

echo "" >&2
echo "NOTE: this diff only covers functions the helper enumerated. A function" >&2
echo "the helper never emits as a node is invisible here — neither FATAL nor" >&2
echo "report-only, just absent — so FATAL:0 does not by itself mean full parity." >&2
echo "Known enumeration gap: Task 11 (include!-generated code, e.g. OUT_DIR" >&2
echo "build-script output)." >&2
echo "" >&2
echo "Known residual gap (Task 9): a trait-declaration method belonging to a" >&2
echo "trait whose ENTIRE cluster (trait + every impl + every impl's self type)" >&2
echo "is independently unreachable gets NO per-line dead_code diagnostic of" >&2
echo "its own from rustc — only the trait-impl-cascade exclusion above (which" >&2
echo "only ever applies to impl methods) intentionally normalises this same" >&2
echo "compiler behaviour. A trait-declaration method is not covered by that" >&2
echo "exclusion, so it can surface here as a FATAL even when the helper is" >&2
echo "correct (verified against testdata/libonly's hidden_trait::HidTr::hid_m:" >&2
echo "helper says dead, matching reality — nothing in the crate reaches" >&2
echo "HidTr at all — but rustc's only diagnostic for the whole dead cluster is" >&2
echo "\"trait \`HidTr\` is never used\" on the TRAIT's own line, never a second" >&2
echo "one on hid_m's line). Left unsuppressed deliberately per project policy:" >&2
echo "a disclosed FATAL that a human confirms is preferred over silently" >&2
echo "widening what this measuring instrument excludes." >&2
echo "A propagated false-dead-code effect (a live enumerated function whose only" >&2
echo "caller is one of these invisible functions) IS still caught as a normal" >&2
echo "FATAL entry for that caller; what's uncovered is the invisible function's" >&2
echo "own status and untested chained-invisibility cases." >&2

# H1 (second half): `considered == 0` (or a near-total exclusion) means
# nothing was actually compared, yet nothing previously guarded against
# reporting that as a clean, exit-0 run — comparing nothing is not evidence
# of agreement. Checked before the FATAL check so a vacuous run refuses
# instead of quietly "passing" with fatal:0.
considered_count="$(grep -o '"considered":[0-9]*' "$WORK/report.txt" | grep -o '[0-9]*$')"
excluded_count="$(grep -o '"excluded":[0-9]*' "$WORK/report.txt" | grep -o '[0-9]*$')"
total_count="$(grep -o '"total_functions":[0-9]*' "$WORK/report.txt" | grep -o '[0-9]*$')"

if [[ "${considered_count:-0}" -eq 0 ]]; then
  echo "" >&2
  echo "REFUSED: zero functions were considered in this comparison (excluded:" >&2
  echo "${excluded_count:-0} of ${total_count:-0} — see the normalisation-exclusions" >&2
  echo "breakdown above for why). A comparison against nothing is not evidence of" >&2
  echo "agreement." >&2
  exit 7
fi

# "Implausibly high": no fixture or real workspace observed on this branch
# exceeds a small fraction excluded (see testdata/*/oracle-expected.json
# summaries). A run this lopsided is far more likely to indicate an
# over-broad exclusion heuristic (H1's own root cause) than a genuinely
# generated-code-heavy crate, so it is refused rather than reported as
# agreement.
if [[ "${total_count:-0}" -gt 0 ]]; then
  excluded_pct=$(( excluded_count * 100 / total_count ))
  if [[ "$excluded_pct" -ge 90 ]]; then
    echo "" >&2
    echo "REFUSED: ${excluded_pct}% of enumerated functions (${excluded_count} of" >&2
    echo "${total_count}) were excluded from comparison — implausibly high. This" >&2
    echo "measuring instrument exists to catch a normalisation heuristic that" >&2
    echo "over-excludes; a run this lopsided is refused rather than reported as" >&2
    echo "agreement." >&2
    exit 7
  fi
fi

fatal_count="$(grep -o '"fatal":[0-9]*' "$WORK/report.txt" | grep -o '[0-9]*$')"
if [[ "${fatal_count:-0}" -gt 0 ]]; then
  echo "oracle-diff: FATAL — helper reports dead code the oracle considers live" >&2
  exit 1
fi
