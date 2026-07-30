# Classifies every helper-enumerated function against the oracle's dead_code
# verdict and prints a human-readable report to stdout, plus a JSON summary
# line prefixed "SUMMARY " for scripted consumption.
#
# Inputs (all provided via --slurpfile from oracle-diff.sh):
#   $helper   - [helper.json]                    functions + calls (functions[].trait_impl
#                                                  carries each trait-impl method's own
#                                                  (file, line, column) cascade key —
#                                                  see enumerate.rs)
#   $oracle   - [{file, line, column}, ...]       oracle dead_code primary spans, ALL
#                                                  kinds — used for both the ordinary
#                                                  per-function check AND the cascade
#                                                  check below. Column-precise (not just
#                                                  line), which is what makes one map
#                                                  safe for both: see key() below.
#   $excluded - [{id, reason}, ...]               attribute-based exclusions

def fmt(x): "  \(x.pkg)::\(x.symbol)  (\(x.file):\(x.line), id=\(x.id))";
# (file, line, column) — column-precise, not just line, is load-bearing: two
# distinct declarations can share one source line (a trait's methods declared
# inline: `trait Tr { fn a(&self); fn b(&self); }`), and rustc still gives
# each its own diagnostic with a distinct column pointing at that exact name
# token (verified directly — see oracle-diff.sh's header comment). Keying on
# line alone would let a diagnostic that merely LANDS on a shared line be
# misread as being about every declaration on it; column makes the key
# exact-token, so it is safe to reuse for both the per-function check and the
# trait-impl cascade check without any separate kind-filtered map.
def key(loc): loc.file + ":" + (loc.line|tostring) + ":" + (loc.column|tostring);

($helper[0]) as $g
| ($oracle[0] // []) as $oracle_spans
| ($excluded[0] // []) as $attr_excl

# Oracle dead set as an O(1) lookup keyed by "file:line:column".
| (reduce $oracle_spans[] as $s ({}; . + {(key($s)): true})) as $oracle_map

# Attribute-exclusion reasons keyed by function id.
| (reduce $attr_excl[] as $e ({}; . + {($e.id|tostring): $e.reason})) as $attr_map

# Adjacency list: from-id (string) -> unique [to-id, ...].
| (reduce $g.calls[] as $c ({}; .[($c.from|tostring)] += [$c.to]))
  | with_entries(.value |= unique) as $adj

# BFS from every root, using an object as the seen-set for O(1) membership.
| ([$g.functions[] | select(.root) | .id]) as $roots
| (reduce $roots[] as $r ({}; . + {($r|tostring): true})) as $seen0
| ( {seen: $seen0, frontier: $roots}
    | until(.frontier == [];
        . as $state
        | ($state.frontier | map($adj[(.|tostring)] // []) | add // [] | unique) as $nbrs
        | ([$nbrs[] | select(($state.seen[(.|tostring)] // false) | not)]) as $new
        | { seen: (reduce $new[] as $n ($state.seen; . + {($n|tostring): true})),
            frontier: $new }
      )
    | .seen
  ) as $reach_map

# Classify every function.
| ($g.functions | map(
    . as $f
    | (key($f)) as $key
    | ($f.trait_impl) as $ti
    | (if $ti == null then null else $ti.self_type end) as $st
    # Trait-impl cascade (Task 13): both the trait's and the self type's
    # declaration are keyed on (file, line, column) against the SAME
    # column-precise oracle map used for the ordinary per-function check
    # below — sound for the same reason that check is: an exact-token key
    # cannot collide with an unrelated diagnostic on the same line the way a
    # line-only key can (see key() above). `self_type.kind == "unresolved"`
    # deliberately satisfies neither branch below — a resolution failure
    # must never license the cascade the way genuine ineligibility does (see
    # model::SelfType in enumerate.rs).
    | (if $ti == null then false else ($oracle_map[key($ti.trait_decl)] // false) end) as $trait_dead
    | ($ti != null and $st.kind == "not_eligible") as $self_type_not_locally_eligible
    | ($ti != null and $st.kind == "local" and ($oracle_map[key($st)] // false)) as $self_type_dead
    | ($ti != null and $trait_dead and ($self_type_not_locally_eligible or $self_type_dead)) as $cascade
    | . + {
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
        oracle_dead: ($oracle_map[$key] // false)
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

| "=== FATAL: helper says dead, oracle says live (false dead code) — \($fatal|length) ===",
  ( if ($fatal|length) == 0 then "  (none)" else ($fatal[] | fmt(.)) end ),
  "",
  "=== report-only: helper says live, oracle says dead (conservative) — \($report_only|length) ===",
  ( if ($report_only|length) == 0 then "  (none)" else ($report_only[] | fmt(.)) end ),
  "",
  "=== agreement ===",
  "  both dead: \($agree_dead|length)",
  "  both live: \($agree_live|length)",
  "",
  "=== normalisation exclusions (\($excluded_fns|length) of \($classified|length) functions) ===",
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
  # Kept as one prefixed line rather than a separate file so oracle-diff.sh's
  # existing single-jq-invocation, tee'd-stdout shape doesn't need a second
  # process or a second temp file just to expose this.
  "FATAL_JSON " + ($fatal | map({symbol, pkg, file, line, column}) | tostring)
