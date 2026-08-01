# Diffs the observed FATAL set (from oracle-diff.sh's "FATAL_JSON " line)
# against a committed per-fixture baseline (testdata/<crate>/oracle-expected.json)
# and reports three directions of divergence:
#
#   new        - a FATAL oracle-diff observed that the baseline doesn't list.
#                Usually a real regression: an exclusion narrowed, or the
#                helper started mis-deriving reachability somewhere new.
#   missing    - a FATAL the baseline lists that oracle-diff did NOT observe.
#                Usually a real regression in the OTHER direction: an
#                exclusion silently widened and now covers something it
#                shouldn't (the failure mode this whole task exists to catch)
#                — or the helper stopped emitting/reaching the node at all.
#   mismatched - a (file, line, column) key present on BOTH sides, but naming
#                a different symbol/pkg. The key alone matching is not enough:
#                if a source edit lands a *different* FATAL at the exact same
#                location a stale baseline entry vacated, a key-only diff
#                would call that a match and silently pass with the wrong
#                symbol printed — the same collision Task 13 already fixed
#                once for the harness itself (name/line-only keys). Column
#                precision makes this astronomically unlikely to occur
#                naturally, but the check is cheap and the failure mode it
#                closes is exactly the one this gate exists to catch.
#
# All three are failures. This is deliberately NOT "observed <= expected" or
# any other threshold/count comparison — see oracle-gate.sh's header comment
# for why a count can't distinguish "still adjudicated" from "silently
# widened".
#
# Keyed on (file, line, column), matching oracle-diff.jq's own key() — that
# granularity is what makes the key collision-proof (Task 13); reusing it
# here means a baseline entry can only ever match the one declaration it was
# written against.
def key(x): x.file + ":" + (x.line|tostring) + ":" + (x.column|tostring);

($expected | map({(key(.)): .}) | add // {}) as $exp_map
| ($observed | map({(key(.)): .}) | add // {}) as $obs_map
| (($exp_map | keys) - ($obs_map | keys)) as $missing_keys
| (($obs_map | keys) - ($exp_map | keys)) as $new_keys
| (($exp_map | keys) - $missing_keys) as $matched_keys
| ($matched_keys
    | map(select(
        $exp_map[.].symbol != $obs_map[.].symbol
        or ($exp_map[.].pkg // null) != ($obs_map[.].pkg // null)
      ))
  ) as $mismatched_keys
| {
    missing: ($missing_keys | map($exp_map[.])),
    new: ($new_keys | map($obs_map[.])),
    mismatched: ($mismatched_keys | map({
        key: .,
        expected: $exp_map[.],
        observed: $obs_map[.]
      }))
  }
