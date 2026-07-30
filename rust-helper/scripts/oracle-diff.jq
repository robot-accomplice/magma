# Classifies every helper-enumerated function against the oracle's dead_code
# verdict and prints a human-readable report to stdout, plus JSON summary
# lines prefixed "SUMMARY " / "SUMMARY_TEST " for scripted consumption.
#
# TWO independent comparisons are computed from the same helper.json graph
# (Task H5):
#   - plain-config:  helper's PRODUCTION-roots reachability vs the plain
#                     (`cargo check --all-targets`, cfg(test) off) oracle.
#                     Unchanged from before H5.
#   - test-config:   helper's ALL-roots reachability (production roots PLUS
#                     every #[test]/#[bench] entry point) vs the test-profile
#                     (`cargo check --all-targets --profile test`, cfg(test)
#                     on) oracle. New in H5.
#
# These are kept deliberately separate outputs, never merged into one
# verdict: a function dead under plain-config but live under test-config is
# the `test_only` signal magma exists to report, not a divergence. Only a
# function BOTH sides call dead in a config whose own oracle calls it live is
# a FATAL for that config.
#
# Inputs (all provided via --slurpfile from oracle-diff.sh):
#   $helper       - [helper.json]                  functions + calls
#                                                    (functions[].trait_impl
#                                                    carries each trait-impl
#                                                    method's own (file, line,
#                                                    column) cascade key — see
#                                                    enumerate.rs)
#   $oracle       - [{file, line, column}, ...]     plain-config oracle
#                                                    dead_code primary spans
#   $oracle_test  - [{file, line, column}, ...]     test-config oracle
#                                                    dead_code primary spans
#   $excluded     - [{id, reason}, ...]             attribute-based
#                                                    exclusions (source-level,
#                                                    so they apply identically
#                                                    to both configs)
#   $test_entries - [{id}, ...]                     ids carrying a literal
#                                                    #[test] attribute
#                                                    (source-scanned — no
#                                                    narrow field exposes this
#                                                    distinct from the broader
#                                                    `test` context flag; see
#                                                    oracle-diff.sh)

def fmt(x): "  \(x.pkg)::\(x.symbol)  (\(x.file):\(x.line), id=\(x.id))";
# (file, line, column) — column-precise, not just line, is load-bearing: two
# distinct declarations can share one source line (a trait's methods declared
# inline: `trait Tr { fn a(&self); fn b(&self); }`), and rustc still gives
# each its own diagnostic with a distinct column pointing at that exact name
# token (verified directly — see oracle-diff.sh's header comment). Keying on
# line alone would let a diagnostic that merely LANDS on a shared line be
# misread as being about every declaration on it; column makes the key
# exact-token, so it is safe to reuse for both the per-function check and the
# trait-impl cascade check without any separate kind-filtered map — and, per
# H5, for both configs' maps too.
def key(loc): loc.file + ":" + (loc.line|tostring) + ":" + (loc.column|tostring);

($helper[0]) as $g
| ($oracle[0] // []) as $oracle_spans
| ($oracle_test[0] // []) as $oracle_test_spans
| ($excluded[0] // []) as $attr_excl
| ($test_entries[0] // []) as $test_entry_list

# Oracle dead sets as O(1) lookups keyed by "file:line:column", one per config.
| (reduce $oracle_spans[] as $s ({}; . + {(key($s)): true})) as $oracle_map
| (reduce $oracle_test_spans[] as $s ({}; . + {(key($s)): true})) as $oracle_test_map

# Attribute-exclusion reasons keyed by function id. Source-level, so reused
# unchanged for both configs' exclusion logic below.
| (reduce $attr_excl[] as $e ({}; . + {($e.id|tostring): $e.reason})) as $attr_map

# #[test]-attributed function ids, keyed for O(1) membership. These are the
# all-roots seed's test half — see the $all_roots definition below.
| (reduce $test_entry_list[] as $e ({}; . + {($e.id|tostring): true})) as $test_entry_map

# Adjacency list: from-id (string) -> unique [to-id, ...]. One graph, shared
# by both BFS walks below — only the root set and the oracle map differ.
| (reduce $g.calls[] as $c ({}; .[($c.from|tostring)] += [$c.to]))
  | with_entries(.value |= unique) as $adj

| def bfs(roots):
  (reduce roots[] as $r ({}; . + {($r|tostring): true})) as $seen0
  | ( {seen: $seen0, frontier: roots}
      | until(.frontier == [];
          . as $state
          | ($state.frontier | map($adj[(.|tostring)] // []) | add // [] | unique) as $nbrs
          | ([$nbrs[] | select(($state.seen[(.|tostring)] // false) | not)]) as $new
          | { seen: (reduce $new[] as $n ($state.seen; . + {($n|tostring): true})),
              frontier: $new }
        )
      | .seen
    );

# Plain-config: production roots only (unchanged).
([$g.functions[] | select(.root) | .id]) as $roots
| bfs($roots) as $reach_map

# Test-config (H5): production roots, PLUS every #[test]-attributed function
# (from $test_entry_map) and every #[bench]-attributed function (`.bench` is
# already the narrow attribute signal — see enumerate.rs's `f.is_bench(db)` —
# unlike `.test`, which is deliberately broader than "carries #[test]" and so
# cannot be used directly as a root seed: seeding on the broad flag would
# treat every function merely living under #[cfg(test)] ancestry as its own
# root, trivially "reaching" itself and hiding genuinely-dead test helpers —
# exactly the false-agreement failure mode this harness exists to catch).
# These are entry points the compiler-generated test/bench harness calls
# directly — nothing in the graph itself calls them, matching how `main` is a
# root with no caller either.
| ([$g.functions[] | select(.root or .bench or ($test_entry_map[(.id|tostring)] // false)) | .id]) as $all_roots
| bfs($all_roots) as $reach_map_all

# Classify every function, both directions in one pass.
| ($g.functions | map(
    . as $f
    | (key($f)) as $key
    | ($f.trait_impl) as $ti
    | (if $ti == null then null else $ti.self_type end) as $st
    # Trait-impl cascade (Task 13), evaluated once per config: both the
    # trait's and the self type's declaration are keyed on (file, line,
    # column) against that config's OWN column-precise oracle map — sound for
    # the same reason the ordinary per-function check is (see key() above).
    # `self_type.kind == "unresolved"` deliberately satisfies neither branch
    # below in either config — a resolution failure must never license the
    # cascade the way genuine ineligibility does (see model::SelfType).
    | (if $ti == null then false else ($oracle_map[key($ti.trait_decl)] // false) end) as $trait_dead
    | (if $ti == null then false else ($oracle_test_map[key($ti.trait_decl)] // false) end) as $trait_dead_test
    | ($ti != null and $st.kind == "not_eligible") as $self_type_not_locally_eligible
    | ($ti != null and $st.kind == "local" and ($oracle_map[key($st)] // false)) as $self_type_dead
    | ($ti != null and $st.kind == "local" and ($oracle_test_map[key($st)] // false)) as $self_type_dead_test
    | ($ti != null and $trait_dead and ($self_type_not_locally_eligible or $self_type_dead)) as $cascade
    | ($ti != null and $trait_dead_test and ($self_type_not_locally_eligible or $self_type_dead_test)) as $cascade_test
    | . + {
        # Plain-config exclusion (unchanged): a test-context function is
        # excluded here because it structurally does not exist in the
        # cfg(test)-off compilation at all — there is no diagnostic the
        # plain oracle could ever render for it, not a normalisation choice.
        excluded_reason: (
          if $f.generated then "generated"
          elif $f.test then "test (never compiled under plain `cargo check`)"
          elif ($attr_map[($f.id|tostring)] != null)
            then "attribute: " + $attr_map[($f.id|tostring)]
          elif $cascade
            then "trait-impl-cascade (trait declared at \(key($ti.trait_decl)) independently reported dead by the oracle"
                 + (if $self_type_not_locally_eligible
                    then "; self type is not a workspace-local struct/enum/union eligible for its own dead_code diagnostic (builtin, reference, tuple, or externally-defined — including a generic instantiation of an external type)"
                    else "; self type declared at \(key($st)) independently reported dead by the oracle too" end)
                 + ")"
          else null
          end
        ),
        helper_dead: (($reach_map[($f.id|tostring)] // false) | not),
        oracle_dead: ($oracle_map[$key] // false),

        # Test-config exclusion (H5): deliberately does NOT exclude on
        # `$f.test` — the whole point of this direction is that the
        # test-profile oracle DOES compile test-context code and CAN render a
        # verdict on it. `generated` and attribute-suppression still apply:
        # both are facts about what the compiler can diagnose at all,
        # independent of which cfg(test) setting compiled it.
        test_excluded_reason: (
          if $f.generated then "generated"
          elif ($attr_map[($f.id|tostring)] != null)
            then "attribute: " + $attr_map[($f.id|tostring)]
          elif $cascade_test
            then "trait-impl-cascade (trait declared at \(key($ti.trait_decl)) independently reported dead by the test-config oracle"
                 + (if $self_type_not_locally_eligible
                    then "; self type is not a workspace-local struct/enum/union eligible for its own dead_code diagnostic (builtin, reference, tuple, or externally-defined — including a generic instantiation of an external type)"
                    else "; self type declared at \(key($st)) independently reported dead by the test-config oracle too" end)
                 + ")"
          else null
          end
        ),
        helper_dead_all: (($reach_map_all[($f.id|tostring)] // false) | not),
        oracle_dead_test: ($oracle_test_map[$key] // false)
      }
  )) as $classified

| ($classified | map(select(.excluded_reason == null))) as $considered
| ($classified | map(select(.excluded_reason != null))) as $excluded_fns
| ($considered | map(select(.helper_dead and (.oracle_dead | not)))) as $fatal
| ($considered | map(select((.helper_dead | not) and .oracle_dead))) as $report_only
| ($considered | map(select(.helper_dead and .oracle_dead))) as $agree_dead
| ($considered | map(select((.helper_dead | not) and (.oracle_dead | not)))) as $agree_live
| ($excluded_fns | group_by(.excluded_reason)
    | map({reason: .[0].excluded_reason, count: length})) as $excl_breakdown

| ($classified | map(select(.test_excluded_reason == null))) as $considered_test
| ($classified | map(select(.test_excluded_reason != null))) as $excluded_test_fns
| ($considered_test | map(select(.helper_dead_all and (.oracle_dead_test | not)))) as $fatal_test
| ($considered_test | map(select((.helper_dead_all | not) and .oracle_dead_test))) as $report_only_test
| ($considered_test | map(select(.helper_dead_all and .oracle_dead_test))) as $agree_dead_test
| ($considered_test | map(select((.helper_dead_all | not) and (.oracle_dead_test | not)))) as $agree_live_test
| ($excluded_test_fns | group_by(.test_excluded_reason)
    | map({reason: .[0].test_excluded_reason, count: length})) as $excl_breakdown_test

| "=== PLAIN-CONFIG: FATAL: helper says dead, oracle says live (false dead code) — \($fatal|length) ===",
  ( if ($fatal|length) == 0 then "  (none)" else ($fatal[] | fmt(.)) end ),
  "",
  "=== PLAIN-CONFIG: report-only: helper says live, oracle says dead (conservative) — \($report_only|length) ===",
  ( if ($report_only|length) == 0 then "  (none)" else ($report_only[] | fmt(.)) end ),
  "",
  "=== PLAIN-CONFIG: agreement ===",
  "  both dead: \($agree_dead|length)",
  "  both live: \($agree_live|length)",
  "",
  "=== PLAIN-CONFIG: normalisation exclusions (\($excluded_fns|length) of \($classified|length) functions) ===",
  ( if ($excl_breakdown|length) == 0 then "  (none)" else ($excl_breakdown[] | "  \(.reason): \(.count)") end ),
  "",
  "SUMMARY " + ({
      total_functions: ($classified|length),
      considered: ($considered|length),
      excluded: ($excluded_fns|length),
      excluded_breakdown: ($excl_breakdown | map({(.reason): .count}) | add // {}),
      fatal: ($fatal|length),
      report_only: ($report_only|length),
      agree_dead: ($agree_dead|length),
      agree_live: ($agree_live|length)
    } | tostring),
  # Machine-readable form of the FATAL set (Task 16), consumed by
  # scripts/oracle-gate.sh to diff against a committed per-fixture baseline.
  "FATAL_JSON " + ($fatal | map({symbol, pkg, file, line, column}) | tostring),
  "",
  "=== TEST-CONFIG (H5): FATAL: helper says dead under all-roots, test-profile oracle says live — \($fatal_test|length) ===",
  ( if ($fatal_test|length) == 0 then "  (none)" else ($fatal_test[] | fmt(.)) end ),
  "",
  "=== TEST-CONFIG: report-only: helper says live under all-roots, test-profile oracle says dead — \($report_only_test|length) ===",
  ( if ($report_only_test|length) == 0 then "  (none)" else ($report_only_test[] | fmt(.)) end ),
  "",
  "=== TEST-CONFIG: agreement ===",
  "  both dead: \($agree_dead_test|length)",
  "  both live: \($agree_live_test|length)",
  "",
  "=== TEST-CONFIG: normalisation exclusions (\($excluded_test_fns|length) of \($classified|length) functions) ===",
  ( if ($excl_breakdown_test|length) == 0 then "  (none)" else ($excl_breakdown_test[] | "  \(.reason): \(.count)") end ),
  "",
  "SUMMARY_TEST " + ({
      total_functions: ($classified|length),
      considered: ($considered_test|length),
      excluded: ($excluded_test_fns|length),
      excluded_breakdown: ($excl_breakdown_test | map({(.reason): .count}) | add // {}),
      fatal: ($fatal_test|length),
      report_only: ($report_only_test|length),
      agree_dead: ($agree_dead_test|length),
      agree_live: ($agree_live_test|length)
    } | tostring),
  "FATAL_JSON_TEST " + ($fatal_test | map({symbol, pkg, file, line, column}) | tostring)
