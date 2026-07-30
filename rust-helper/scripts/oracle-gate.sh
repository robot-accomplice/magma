#!/usr/bin/env bash
# Thin runner around oracle-diff.sh that turns its output into a real CI
# gate (Task 16).
#
# The problem this closes: oracle-diff.sh's own exit code is driven purely
# by "is the FATAL count > 0", and two fixtures (libonly, collision) carry
# FATALs that are honest and adjudicated — they are SUPPOSED to be non-zero
# forever. That makes oracle-diff.sh's exit code useless as a gate: a run
# red for an adjudicated reason is indistinguishable from one red for a NEW
# reason, so red stops carrying information.
#
# The fix: compare the OBSERVED FATAL set against a committed baseline
# (testdata/<crate>/oracle-expected.json) and gate on EXACT match, not on
# count.
#
#   - A FATAL appearing that isn't in the baseline -> fail (new problem).
#   - A baselined FATAL that stops appearing -> ALSO fail. This is the
#     non-obvious half and the one this harness has to get right: a FATAL
#     silently vanishing almost always means an exclusion widened and now
#     covers something it shouldn't — exactly the failure mode this plan
#     has hit three times (see oracle-diff.sh's own header comment on the
#     module-path heuristic it had to remove, and Task 13's two rounds of
#     collision fixes). A max-count or "observed <= expected" check would
#     wave this through as an improvement; an exact-set diff cannot.
#
# This script adds NO exclusion and NO suppression of any kind to the
# harness itself — it only compares oracle-diff.sh's own output to a
# declared baseline. oracle-diff.sh's dead-code derivation is untouched.
#
# Review-fix additions (post-implementation, all reporting/coverage, no
# mechanism change):
#   - The FATAL set alone can't see an exclusion silently widening without
#     also changing which entries are FATAL (e.g. a fresh #[allow(dead_code)]
#     on an already-baselined entry moves it from considered/agree_dead into
#     excluded, FATAL set unchanged) — the baseline also pins the SUMMARY
#     counts (considered/excluded/agree_dead/agree_live/fatal) and a
#     divergence in any of them fails the gate too, naming which count moved.
#   - A (file,line,column) key matching on both sides is necessary but not
#     sufficient — if the symbol/pkg at that key differs, that's a stale
#     baseline entry masking a genuinely new FATAL, not a match.
#   - PASS no longer discards oracle-diff.sh's report: a green gate still
#     prints every adjudicated FATAL (with its reason) and the full
#     underlying report, so a reader of a passing run learns exactly what
#     this fixture is carrying and why, not just "PASS".
#   - The refusal path (rc 3 or 5) now actually prints oracle-diff.sh's
#     captured output before its own message, instead of citing output that
#     was silently discarded.
set -euo pipefail

REPO="${1:?usage: oracle-gate.sh <workspace-root>}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ABS="$(cd "$REPO" && pwd)"
BASELINE="$REPO_ABS/oracle-expected.json"

if [[ ! -f "$BASELINE" ]]; then
  echo "error: no baseline at $BASELINE" >&2
  echo "every fixture gated by oracle-gate.sh needs one, even if its FATAL set is empty:" >&2
  echo '  {"fatal": []}' >&2
  exit 1
fi

# Every baseline entry must carry a non-empty reason. An entry without one
# defeats the point of a baseline (a bare, unexplained "this is expected" is
# exactly the kind of unadjudicated suppression this task exists to avoid
# introducing under a new name) — reject the whole file rather than silently
# accepting an unjustified entry.
unjustified="$(jq -r '
  [ (.fatal // [])[]
    | select((.reason // "") | gsub("\\s"; "") | length == 0)
    | "  \(.file // "?"):\(.line // "?"):\(.column // "?") (\(.symbol // "?"))"
  ] | join("\n")
' "$BASELINE")"
if [[ -n "$unjustified" ]]; then
  echo "error: baseline $BASELINE has entries with no stated reason:" >&2
  echo "$unjustified" >&2
  echo "every baselined FATAL needs a one-line reason — see other entries for the format." >&2
  exit 1
fi

# A copy-paste duplicate sharing a (file, line, column) key would otherwise
# collapse silently in oracle-gate.jq's `map({(key(.)): .}) | add` (the later
# entry wins, the earlier one vanishes with no error) — reject it here
# instead, at the source, before it ever reaches the diff.
dup_keys="$(jq -r '
  [ (.fatal // [])[] | (.file + ":" + (.line|tostring) + ":" + (.column|tostring)) ]
  | group_by(.) | map(select(length > 1) | .[0]) | .[]
' "$BASELINE")"
if [[ -n "$dup_keys" ]]; then
  echo "error: baseline $BASELINE has duplicate (file,line,column) keys — a copy-paste" >&2
  echo "duplicate would otherwise collapse silently instead of failing loudly:" >&2
  echo "$dup_keys" | sed 's/^/  /' >&2
  exit 1
fi

# The baseline must also pin the SUMMARY counts (Task 16 review fix 3), not
# just the FATAL set: an exclusion can silently widen (e.g. a fresh
# #[allow(dead_code)]) without changing which entries are FATAL at all, and a
# FATAL-only diff would wave that through as a clean PASS. Require the field
# up front rather than treating an absent one as "nothing to check".
if ! jq -e '(.summary // {}) as $s
    | (["considered","excluded","agree_dead","agree_live","fatal"] | all(. as $k | $s | has($k)))
  ' "$BASELINE" >/dev/null 2>&1; then
  echo "error: baseline $BASELINE has no \"summary\" object pinning considered/excluded/" >&2
  echo "agree_dead/agree_live/fatal counts (Task 16 review fix 3). Example:" >&2
  echo '  "summary": {"considered": 6, "excluded": 1, "agree_dead": 1, "agree_live": 2, "fatal": 3}' >&2
  exit 1
fi

echo "== running oracle-diff.sh on $REPO_ABS ==" >&2
OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT
set +e
"$SCRIPT_DIR/oracle-diff.sh" "$REPO_ABS" >"$OUT" 2>&1
rc=$?
set -e

# oracle-diff.sh's own refusal codes (helper refused to compute: 3; stale
# cache, not actually recompiled: 5) are not "FATAL count" outcomes at all —
# there is nothing to diff a baseline against, so propagate them unchanged
# rather than trying to interpret them as a FATAL-set mismatch.
if [[ $rc -eq 3 || $rc -eq 5 ]]; then
  echo "" >&2
  echo "oracle-diff.sh's own output:" >&2
  cat "$OUT" >&2
  echo "" >&2
  echo "oracle-gate: oracle-diff.sh refused (exit $rc) — see its output above; nothing to gate against the baseline" >&2
  exit "$rc"
fi

fatal_json_line="$(grep '^FATAL_JSON ' "$OUT" || true)"
if [[ -z "$fatal_json_line" ]]; then
  echo "" >&2
  echo "oracle-gate: oracle-diff.sh (exit $rc) produced no FATAL_JSON line — cannot diff against the baseline." >&2
  echo "oracle-diff.sh output:" >&2
  cat "$OUT" >&2
  exit 4
fi
observed="${fatal_json_line#FATAL_JSON }"
expected="$(jq -c '.fatal // []' "$BASELINE")"

diff_result="$(jq -n --argjson observed "$observed" --argjson expected "$expected" -f "$SCRIPT_DIR/oracle-gate.jq")"
missing_count="$(jq '.missing | length' <<<"$diff_result")"
new_count="$(jq '.new | length' <<<"$diff_result")"
mismatched_count="$(jq '.mismatched | length' <<<"$diff_result")"

# SUMMARY-count check (Task 16 review fix 3): an exclusion or classification
# can shift without changing the FATAL set at all (e.g. a fresh
# #[allow(dead_code)] on an already-baselined FATAL moves it from
# considered/agree_dead into excluded, leaving the FATAL set — and therefore
# the diff above — untouched). Pin the whole SUMMARY line, not just FATAL.
summary_line="$(grep '^SUMMARY ' "$OUT" || true)"
if [[ -z "$summary_line" ]]; then
  echo "" >&2
  echo "oracle-gate: oracle-diff.sh (exit $rc) produced no SUMMARY line — cannot verify baselined counts." >&2
  echo "oracle-diff.sh output:" >&2
  cat "$OUT" >&2
  exit 4
fi
observed_summary="${summary_line#SUMMARY }"
expected_summary="$(jq -c '.summary' "$BASELINE")"
summary_diff="$(jq -c -n --argjson obs "$observed_summary" --argjson exp "$expected_summary" '
  ["considered","excluded","agree_dead","agree_live","fatal"]
  | map(select($obs[.] != $exp[.]))
  | map({field: ., expected: $exp[.], observed: $obs[.]})
')"
summary_mismatch_count="$(jq 'length' <<<"$summary_diff")"

if [[ "$missing_count" -eq 0 && "$new_count" -eq 0 && "$mismatched_count" -eq 0 && "$summary_mismatch_count" -eq 0 ]]; then
  echo "oracle-gate: PASS — observed FATAL set for $REPO_ABS matches $BASELINE exactly ($(jq 'length' <<<"$expected") entries)" >&2
  echo "" >&2
  echo "Adjudicated FATAL(s) this fixture carries by design (exit 0 means \"the instrument" >&2
  echo "behaves as adjudicated\", not \"no FATALs\" — see reasons below):" >&2
  if [[ "$(jq 'length' <<<"$expected")" -eq 0 ]]; then
    echo "  (none)" >&2
  else
    jq -r '.[] | "  \(.pkg // "?")::\(.symbol)  (\(.file):\(.line):\(.column))\n    -- \(.reason)"' <<<"$expected" >&2
  fi
  echo "" >&2
  echo "oracle-diff.sh's own report:" >&2
  cat "$OUT" >&2
  exit 0
fi

echo "" >&2
echo "oracle-gate: FAIL — observed FATAL set diverges from $BASELINE" >&2

if [[ "$new_count" -gt 0 ]]; then
  echo "" >&2
  echo "NEW FATAL(s) not in the baseline (likely a real regression — investigate before adding" >&2
  echo "these to the baseline; do not paste a reason onto one you haven't matched to a cause):" >&2
  jq -r '.new[] | "  \(.pkg)::\(.symbol)  (\(.file):\(.line):\(.column))"' <<<"$diff_result" >&2
fi

if [[ "$missing_count" -gt 0 ]]; then
  echo "" >&2
  echo "Baselined FATAL(s) that DISAPPEARED (usually means an exclusion silently widened —" >&2
  echo "the failure mode this gate exists to catch; do not delete these from the baseline" >&2
  echo "without confirming why they stopped appearing):" >&2
  jq -r '.missing[] | "  \(.pkg // "?")::\(.symbol)  (\(.file):\(.line):\(.column))  -- \(.reason)"' <<<"$diff_result" >&2
fi

if [[ "$mismatched_count" -gt 0 ]]; then
  echo "" >&2
  echo "STALE key(s) — baseline and observed share a (file,line,column) but name a" >&2
  echo "DIFFERENT declaration (a source edit likely landed a new FATAL at the exact spot a" >&2
  echo "stale baseline entry vacated; do not treat this as a match):" >&2
  jq -r '.mismatched[] | "  \(.key): baseline says \(.expected.pkg // "?")::\(.expected.symbol), observed is \(.observed.pkg // "?")::\(.observed.symbol)"' <<<"$diff_result" >&2
fi

if [[ "$summary_mismatch_count" -gt 0 ]]; then
  echo "" >&2
  echo "SUMMARY count(s) diverged from the baseline (an exclusion or classification changed" >&2
  echo "without changing the FATAL set — the widening this gate exists to catch):" >&2
  jq -r '.[] | "  \(.field): expected \(.expected), observed \(.observed)"' <<<"$summary_diff" >&2
fi

echo "" >&2
echo "oracle-diff.sh's own report:" >&2
cat "$OUT" >&2
exit 1
