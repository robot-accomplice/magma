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
# H5: oracle-diff.sh now emits TWO independent comparisons — plain-config
# (unchanged) and test-config (all-roots reachability vs a test-profile
# oracle). Every check this gate performs on `fatal`/`summary` is mirrored
# for `fatal_test`/`summary_test`, using the SAME oracle-gate.jq (it is
# already generic over an expected/observed FATAL-set pair, so it is called
# a second time rather than modified) — this gate's set-equality and
# two-directional strictness stays exactly as sound for the new direction as
# for the old one, per the same "new/missing/mismatched are all failures"
# rule.
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
# accepting an unjustified entry. Checked for BOTH `fatal` (plain-config) and
# `fatal_test` (test-config, H5) — the same requirement, same reason.
unjustified="$(jq -r '
  [ ((.fatal // []) + (.fatal_test // []))[]
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
# instead, at the source, before it ever reaches the diff. `fatal` and
# `fatal_test` are checked separately (each is diffed against its own
# observed set below, so a duplicate WITHIN one array is what matters — the
# same (file,line,column) legitimately appearing in both arrays is not a
# collision, since one only ever means "dead under plain-config" and the
# other "dead under test-config").
for arr in fatal fatal_test; do
  dup_keys="$(jq -r --arg arr "$arr" '
    [ (.[$arr] // [])[] | (.file + ":" + (.line|tostring) + ":" + (.column|tostring)) ]
    | group_by(.) | map(select(length > 1) | .[0]) | .[]
  ' "$BASELINE")"
  if [[ -n "$dup_keys" ]]; then
    echo "error: baseline $BASELINE has duplicate (file,line,column) keys in \"$arr\" — a" >&2
    echo "copy-paste duplicate would otherwise collapse silently instead of failing loudly:" >&2
    echo "$dup_keys" | sed 's/^/  /' >&2
    exit 1
  fi
done

# The baseline must also pin the SUMMARY counts (Task 16 review fix 3), not
# just the FATAL set: an exclusion can silently widen (e.g. a fresh
# #[allow(dead_code)]) without changing which entries are FATAL at all, and a
# FATAL-only diff would wave that through as a clean PASS. Require the field
# up front rather than treating an absent one as "nothing to check". Checked
# for both `summary` (plain-config) and `summary_test` (test-config, H5).
for obj in summary summary_test; do
  if ! jq -e --arg obj "$obj" '(.[$obj] // {}) as $s
      | (["considered","excluded","agree_dead","agree_live","fatal"] | all(. as $k | $s | has($k)))
    ' "$BASELINE" >/dev/null 2>&1; then
    echo "error: baseline $BASELINE has no \"$obj\" object pinning considered/excluded/" >&2
    echo "agree_dead/agree_live/fatal counts (Task 16 review fix 3; H5 for summary_test)." >&2
    echo "Example:" >&2
    echo "  \"$obj\": {\"considered\": 6, \"excluded\": 1, \"agree_dead\": 1, \"agree_live\": 2, \"fatal\": 3}" >&2
    exit 1
  fi
done

# H8: executed_target_code must be pinned true in every baseline. The
# harness now hard-refuses (oracle-diff.sh exit 8) whenever the helper
# reports executed_target_code=false, so a baseline reaching this point
# always corresponds to a run where it was true — a baseline that doesn't
# declare that invariant, or declares it false, is stale or wrong.
if ! jq -e '.executed_target_code == true' "$BASELINE" >/dev/null 2>&1; then
  echo "error: baseline $BASELINE does not pin \"executed_target_code\": true (Task H8)." >&2
  echo "oracle-diff.sh refuses (exit 8) whenever the helper reports" >&2
  echo "executed_target_code=false, so every baseline must declare the invariant" >&2
  echo "it depends on." >&2
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
# cache, not actually recompiled: 5; oracle/cargo check did not complete
# cleanly: 6; nothing considered or an implausible exclusion fraction: 7;
# executed_target_code false: 8 — Task H) are not "FATAL count" outcomes at
# all — there is nothing to diff a baseline against, so propagate them
# unchanged rather than trying to interpret them as a FATAL-set mismatch.
if [[ $rc -eq 3 || $rc -eq 5 || $rc -eq 6 || $rc -eq 7 || $rc -eq 8 ]]; then
  echo "" >&2
  echo "oracle-diff.sh's own output:" >&2
  cat "$OUT" >&2
  echo "" >&2
  echo "oracle-gate: oracle-diff.sh refused (exit $rc) — see its output above; nothing to gate against the baseline" >&2
  exit "$rc"
fi

fatal_json_line="$(grep '^FATAL_JSON ' "$OUT" || true)"
fatal_json_test_line="$(grep '^FATAL_JSON_TEST ' "$OUT" || true)"
if [[ -z "$fatal_json_line" || -z "$fatal_json_test_line" ]]; then
  echo "" >&2
  echo "oracle-gate: oracle-diff.sh (exit $rc) produced no FATAL_JSON/FATAL_JSON_TEST line — cannot diff against the baseline." >&2
  echo "oracle-diff.sh output:" >&2
  cat "$OUT" >&2
  exit 4
fi
observed="${fatal_json_line#FATAL_JSON }"
observed_test="${fatal_json_test_line#FATAL_JSON_TEST }"
expected="$(jq -c '.fatal // []' "$BASELINE")"
expected_test="$(jq -c '.fatal_test // []' "$BASELINE")"

diff_result="$(jq -n --argjson observed "$observed" --argjson expected "$expected" -f "$SCRIPT_DIR/oracle-gate.jq")"
missing_count="$(jq '.missing | length' <<<"$diff_result")"
new_count="$(jq '.new | length' <<<"$diff_result")"
mismatched_count="$(jq '.mismatched | length' <<<"$diff_result")"

# H5: the SAME diff, same script, run a second time for the test-config
# FATAL set — oracle-gate.jq needed no change to support this (it was
# already generic over one expected/observed pair).
diff_result_test="$(jq -n --argjson observed "$observed_test" --argjson expected "$expected_test" -f "$SCRIPT_DIR/oracle-gate.jq")"
missing_test_count="$(jq '.missing | length' <<<"$diff_result_test")"
new_test_count="$(jq '.new | length' <<<"$diff_result_test")"
mismatched_test_count="$(jq '.mismatched | length' <<<"$diff_result_test")"

# SUMMARY-count check (Task 16 review fix 3): an exclusion or classification
# can shift without changing the FATAL set at all (e.g. a fresh
# #[allow(dead_code)] on an already-baselined FATAL moves it from
# considered/agree_dead into excluded, leaving the FATAL set — and therefore
# the diff above — untouched). Pin the whole SUMMARY line, not just FATAL.
# Checked for both configs (H5).
summary_line="$(grep '^SUMMARY ' "$OUT" || true)"
summary_test_line="$(grep '^SUMMARY_TEST ' "$OUT" || true)"
if [[ -z "$summary_line" || -z "$summary_test_line" ]]; then
  echo "" >&2
  echo "oracle-gate: oracle-diff.sh (exit $rc) produced no SUMMARY/SUMMARY_TEST line — cannot verify baselined counts." >&2
  echo "oracle-diff.sh output:" >&2
  cat "$OUT" >&2
  exit 4
fi
observed_summary="${summary_line#SUMMARY }"
observed_summary_test="${summary_test_line#SUMMARY_TEST }"
expected_summary="$(jq -c '.summary' "$BASELINE")"
expected_summary_test="$(jq -c '.summary_test' "$BASELINE")"
summary_diff="$(jq -c -n --argjson obs "$observed_summary" --argjson exp "$expected_summary" '
  ["considered","excluded","agree_dead","agree_live","fatal"]
  | map(select($obs[.] != $exp[.]))
  | map({field: ., expected: $exp[.], observed: $obs[.]})
')"
summary_mismatch_count="$(jq 'length' <<<"$summary_diff")"
summary_test_diff="$(jq -c -n --argjson obs "$observed_summary_test" --argjson exp "$expected_summary_test" '
  ["considered","excluded","agree_dead","agree_live","fatal"]
  | map(select($obs[.] != $exp[.]))
  | map({field: ., expected: $exp[.], observed: $obs[.]})
')"
summary_test_mismatch_count="$(jq 'length' <<<"$summary_test_diff")"

if [[ "$missing_count" -eq 0 && "$new_count" -eq 0 && "$mismatched_count" -eq 0 && "$summary_mismatch_count" -eq 0 \
   && "$missing_test_count" -eq 0 && "$new_test_count" -eq 0 && "$mismatched_test_count" -eq 0 && "$summary_test_mismatch_count" -eq 0 ]]; then
  echo "oracle-gate: PASS — observed FATAL sets for $REPO_ABS match $BASELINE exactly" >&2
  echo "  (plain-config: $(jq 'length' <<<"$expected") entries, test-config: $(jq 'length' <<<"$expected_test") entries)" >&2
  echo "" >&2
  echo "Adjudicated plain-config FATAL(s) this fixture carries by design (exit 0 means \"the" >&2
  echo "instrument behaves as adjudicated\", not \"no FATALs\" — see reasons below):" >&2
  if [[ "$(jq 'length' <<<"$expected")" -eq 0 ]]; then
    echo "  (none)" >&2
  else
    jq -r '.[] | "  \(.pkg // "?")::\(.symbol)  (\(.file):\(.line):\(.column))\n    -- \(.reason)"' <<<"$expected" >&2
  fi
  echo "" >&2
  echo "Adjudicated test-config FATAL(s) (H5) this fixture carries by design:" >&2
  if [[ "$(jq 'length' <<<"$expected_test")" -eq 0 ]]; then
    echo "  (none)" >&2
  else
    jq -r '.[] | "  \(.pkg // "?")::\(.symbol)  (\(.file):\(.line):\(.column))\n    -- \(.reason)"' <<<"$expected_test" >&2
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
  echo "NEW plain-config FATAL(s) not in the baseline (likely a real regression — investigate" >&2
  echo "before adding these to the baseline; do not paste a reason onto one you haven't" >&2
  echo "matched to a cause):" >&2
  jq -r '.new[] | "  \(.pkg)::\(.symbol)  (\(.file):\(.line):\(.column))"' <<<"$diff_result" >&2
fi

if [[ "$missing_count" -gt 0 ]]; then
  echo "" >&2
  echo "Baselined plain-config FATAL(s) that DISAPPEARED (usually means an exclusion silently" >&2
  echo "widened — the failure mode this gate exists to catch; do not delete these from the" >&2
  echo "baseline without confirming why they stopped appearing):" >&2
  jq -r '.missing[] | "  \(.pkg // "?")::\(.symbol)  (\(.file):\(.line):\(.column))  -- \(.reason)"' <<<"$diff_result" >&2
fi

if [[ "$mismatched_count" -gt 0 ]]; then
  echo "" >&2
  echo "STALE plain-config key(s) — baseline and observed share a (file,line,column) but name" >&2
  echo "a DIFFERENT declaration (a source edit likely landed a new FATAL at the exact spot a" >&2
  echo "stale baseline entry vacated; do not treat this as a match):" >&2
  jq -r '.mismatched[] | "  \(.key): baseline says \(.expected.pkg // "?")::\(.expected.symbol), observed is \(.observed.pkg // "?")::\(.observed.symbol)"' <<<"$diff_result" >&2
fi

if [[ "$summary_mismatch_count" -gt 0 ]]; then
  echo "" >&2
  echo "Plain-config SUMMARY count(s) diverged from the baseline (an exclusion or" >&2
  echo "classification changed without changing the FATAL set — the widening this gate" >&2
  echo "exists to catch):" >&2
  jq -r '.[] | "  \(.field): expected \(.expected), observed \(.observed)"' <<<"$summary_diff" >&2
fi

if [[ "$new_test_count" -gt 0 ]]; then
  echo "" >&2
  echo "NEW test-config FATAL(s) (H5) not in the baseline (likely a real regression —" >&2
  echo "investigate before adding these to the baseline; do not paste a reason onto one you" >&2
  echo "haven't matched to a cause):" >&2
  jq -r '.new[] | "  \(.pkg)::\(.symbol)  (\(.file):\(.line):\(.column))"' <<<"$diff_result_test" >&2
fi

if [[ "$missing_test_count" -gt 0 ]]; then
  echo "" >&2
  echo "Baselined test-config FATAL(s) (H5) that DISAPPEARED (usually means an exclusion" >&2
  echo "silently widened — the failure mode this gate exists to catch; do not delete these" >&2
  echo "from the baseline without confirming why they stopped appearing):" >&2
  jq -r '.missing[] | "  \(.pkg // "?")::\(.symbol)  (\(.file):\(.line):\(.column))  -- \(.reason)"' <<<"$diff_result_test" >&2
fi

if [[ "$mismatched_test_count" -gt 0 ]]; then
  echo "" >&2
  echo "STALE test-config key(s) (H5) — baseline and observed share a (file,line,column) but" >&2
  echo "name a DIFFERENT declaration (a source edit likely landed a new FATAL at the exact spot" >&2
  echo "a stale baseline entry vacated; do not treat this as a match):" >&2
  jq -r '.mismatched[] | "  \(.key): baseline says \(.expected.pkg // "?")::\(.expected.symbol), observed is \(.observed.pkg // "?")::\(.observed.symbol)"' <<<"$diff_result_test" >&2
fi

if [[ "$summary_test_mismatch_count" -gt 0 ]]; then
  echo "" >&2
  echo "Test-config SUMMARY count(s) (H5) diverged from the baseline (an exclusion or" >&2
  echo "classification changed without changing the FATAL set — the widening this gate" >&2
  echo "exists to catch):" >&2
  jq -r '.[] | "  \(.field): expected \(.expected), observed \(.observed)"' <<<"$summary_test_diff" >&2
fi

echo "" >&2
echo "oracle-diff.sh's own report:" >&2
cat "$OUT" >&2
exit 1
