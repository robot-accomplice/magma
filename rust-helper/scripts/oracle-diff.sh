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
#   - a trait-impl method whose enclosing trait AND self type are BOTH
#     independently reported dead_code by the oracle. Empirically verified
#     (three isolated cargo-check probes, not assumed): rustc's dead_code
#     lint DOES flag an individually-dead trait-impl method when its trait
#     and type are otherwise live, but SUPPRESSES that per-method diagnostic
#     when the trait+type are themselves already unreachable (reports only
#     the trait/type, not each method — cascade suppression, presumably to
#     avoid redundant noise once the whole cluster is flagged). Inherent-impl
#     methods do NOT get this treatment (verified: struct+method both
#     reported). Detected by a bounded upward brace-scan for the enclosing
#     `impl Trait for Type {` line; low-confidence scans (hit an unrelated
#     `}` before finding one) do NOT exclude — a spurious FATAL that a human
#     dismisses is preferred over silently hiding a real one.
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
( cd "$REPO_ABS" && cargo check --workspace --message-format=json 2>"$WORK/cargo.stderr" ) \
  > "$WORK/cargo.jsonl" || true
tail -20 "$WORK/cargo.stderr" >&2 || true

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

# Trait/type names the oracle independently reports as dead_code — feeds the
# trait-impl cascade-suppression check below.
jq -r 'select(.reason=="compiler-message") | select(.message.code.code=="dead_code")
       | .message.message | select(test("^trait `"))
       | capture("^trait `(?<name>[A-Za-z_][A-Za-z0-9_]*)`").name' \
  "$WORK/cargo.jsonl" | sort -u | jq -R -s 'split("\n") | map(select(length > 0))' \
  > "$WORK/dead-traits.json"
jq -r 'select(.reason=="compiler-message") | select(.message.code.code=="dead_code")
       | .message.message | select(test("^(struct|enum|union) `"))
       | capture("^(struct|enum|union) `(?<name>[A-Za-z_][A-Za-z0-9_]*)`").name' \
  "$WORK/cargo.jsonl" | sort -u | jq -R -s 'split("\n") | map(select(length > 0))' \
  > "$WORK/dead-types.json"

# Attribute-based normalisation: for every non-generated, non-test function,
# look at the source lines immediately above its declaration line for a
# contiguous run of attributes/doc-comments, and flag it if that run contains
# a liveness-affecting attribute. Grouped by file so each file is read once.
echo "== scanning for liveness-affecting attributes and enclosing impls ==" >&2
jq -r '.functions[] | select(.generated | not) | select(.test | not)
       | [.id, .file, .line, .kind] | @tsv' "$WORK/helper.json" \
  | sort -t $'\t' -k2,2 -k3,3n > "$WORK/scan-targets.tsv"

# Upward scan is bounded per lookup: methods sit directly inside an impl
# block, so the enclosing header is normally within a few dozen lines even in
# a large file. Free functions (kind=func) are skipped entirely — they can
# never be inside an impl block.
MAX_SCAN=1000

: > "$WORK/excluded-attrs.jsonl"
: > "$WORK/impl-info.jsonl"
current_file=""
lines=()
while IFS=$'\t' read -r id file line kind; do
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

  if [[ "$kind" == "method" ]]; then
    depth=0
    j=$((line - 2))
    scanned=0
    impl_trait=""
    impl_type=""
    while (( j >= 0 )) && (( scanned < MAX_SCAN )); do
      l="${lines[$j]:-}"
      scanned=$((scanned + 1))
      if (( depth == 0 )) && [[ "$l" =~ ^[[:space:]]*impl(\<[^\>]*\>)?[[:space:]]+([A-Za-z_][A-Za-z0-9_]*)[^{]*[[:space:]]for[[:space:]]+([A-Za-z_][A-Za-z0-9_:]*) ]]; then
        impl_trait="${BASH_REMATCH[2]}"
        raw_type="${BASH_REMATCH[3]}"
        impl_type="${raw_type##*:}"
        break
      elif (( depth == 0 )) && [[ "$l" =~ ^[[:space:]]*impl(\<[^\>]*\>)?[[:space:]]+([A-Za-z_][A-Za-z0-9_]*) ]]; then
        impl_type="${BASH_REMATCH[2]}"
        break
      fi
      opens="${l//[^{]/}"
      closes="${l//[^}]/}"
      depth=$(( depth + ${#closes} - ${#opens} ))
      if (( depth < 0 )); then depth=0; fi
      j=$((j - 1))
    done
    if [[ -n "$impl_trait" && -n "$impl_type" ]]; then
      jq -cn --argjson id "$id" --arg trait "$impl_trait" --arg type "$impl_type" \
        '{id: $id, trait: $trait, type: $type}' >> "$WORK/impl-info.jsonl"
    fi
  fi
done < "$WORK/scan-targets.tsv"
jq -s '.' "$WORK/excluded-attrs.jsonl" > "$WORK/excluded-attrs.json"
jq -s '.' "$WORK/impl-info.jsonl" > "$WORK/impl-info.json"

echo "== computing reachability and diffing against oracle ==" >&2
jq -nr \
  --slurpfile helper "$WORK/helper.json" \
  --slurpfile oracle "$WORK/oracle-dead.json" \
  --slurpfile excluded "$WORK/excluded-attrs.json" \
  --slurpfile impl_info "$WORK/impl-info.json" \
  --slurpfile dead_traits "$WORK/dead-traits.json" \
  --slurpfile dead_types "$WORK/dead-types.json" \
  -f "$SCRIPT_DIR/oracle-diff.jq" | tee "$WORK/report.txt"

echo "" >&2
echo "NOTE: this diff only covers functions the helper enumerated. A function" >&2
echo "the helper never emits as a node is invisible here — neither FATAL nor" >&2
echo "report-only, just absent — so FATAL:0 does not by itself mean full parity." >&2
echo "Known enumeration gaps: Task 9 (trait-declaration default-bodied methods)" >&2
echo "and Task 11 (include!-generated code, e.g. OUT_DIR build-script output)." >&2
echo "A propagated false-dead-code effect (a live enumerated function whose only" >&2
echo "caller is one of these invisible functions) IS still caught as a normal" >&2
echo "FATAL entry for that caller; what's uncovered is the invisible function's" >&2
echo "own status and untested chained-invisibility cases." >&2

fatal_count="$(grep -o '"fatal":[0-9]*' "$WORK/report.txt" | grep -o '[0-9]*$')"
if [[ "${fatal_count:-0}" -gt 0 ]]; then
  echo "oracle-diff: FATAL — helper reports dead code the oracle considers live" >&2
  exit 1
fi
