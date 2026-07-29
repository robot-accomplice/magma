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
"$HELPER" "$REPO_ABS" >"$WORK/helper.json" 2>"$WORK/helper.stderr"
cat "$WORK/helper.stderr" >&2

if jq -e '.computable == false' "$WORK/helper.json" >/dev/null 2>&1; then
  echo "helper refused: $(jq -r '.reason' "$WORK/helper.json")"
  exit 3
fi

echo "== running oracle (cargo check) on $REPO_ABS ==" >&2
# -v (verbose) makes cargo print one "Fresh <pkg>" / "Compiling <pkg>" /
# "Checking <pkg>" progress line per unit to stderr, alongside the JSON
# stream on stdout. This is the ONLY reliable signal for "did rustc actually
# run for this crate this invocation" — message-format=json output cannot
# tell you that (see the freshness check below for why).
( cd "$REPO_ABS" && cargo check --workspace -v --message-format=json 2>"$WORK/cargo.stderr" ) \
  > "$WORK/cargo.jsonl" || true
tail -20 "$WORK/cargo.stderr" >&2 || true

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
echo "oracle: all $(wc -l <<<"$workspace_packages" | tr -d ' ') workspace-local crate(s) actually recompiled this run — proceeding" >&2

# Oracle dead set, keyed by "file:line" of each dead_code diagnostic's primary
# span (not by name — two functions can share a name, but never a file+line).
# No filtering on the diagnostic's item kind (function/method/struct/trait/
# field...): a non-function diagnostic's primary span line can only collide
# with a helper function's declaration line by coincidence, which normal
# source layout makes vanishingly unlikely, and this key is only ever probed
# against the helper's own function set below.
jq -c 'select(.reason=="compiler-message")
       | select(.message.code.code=="dead_code")
       | .message.spans[]? | select(.is_primary)
       | {file: .file_name, line: .line_start}' \
  "$WORK/cargo.jsonl" > "$WORK/oracle-dead.jsonl"
jq -s '.' "$WORK/oracle-dead.jsonl" > "$WORK/oracle-dead.json"

# Attribute-based normalisation: for every non-generated, non-test function,
# look at the source lines immediately above its declaration line for a
# contiguous run of attributes/doc-comments, and flag it if that run contains
# a liveness-affecting attribute. Grouped by file so each file is read once.
#
# The trait-impl cascade-suppression check needs NO source scanning — Task 13
# moved that to `enumerate.rs`, which emits each trait-impl method's own
# `trait_impl.{trait_decl,self_type_decl}` (file, line) directly in
# helper.json, computed from rustc's own HIR rather than scraped from source
# text. oracle-diff.jq looks those up against the same oracle-dead-span map
# built above — no separate scan, no name matching, no collision risk.
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
    if [[ "$l" =~ ^[[:space:]]*(#\[|///|//!) ]]; then
      attrs="$l"$'\n'"$attrs"
      i=$((i - 1))
      continue
    fi
    break
  done
  reason=""
  if [[ "$attrs" =~ \#\[[[:space:]]*allow\([^\)]*dead_code ]]; then
    reason="allow(dead_code)"
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

fatal_count="$(grep -o '"fatal":[0-9]*' "$WORK/report.txt" | grep -o '[0-9]*$')"
if [[ "${fatal_count:-0}" -gt 0 ]]; then
  echo "oracle-diff: FATAL — helper reports dead code the oracle considers live" >&2
  exit 1
fi
