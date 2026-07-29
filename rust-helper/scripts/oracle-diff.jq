# Classifies every helper-enumerated function against the oracle's dead_code
# verdict and prints a human-readable report to stdout, plus a JSON summary
# line prefixed "SUMMARY " for scripted consumption.
#
# Inputs (all provided via --slurpfile from oracle-diff.sh):
#   $helper      - [helper.json]                    functions + calls
#   $oracle      - [{file, line}, ...]              oracle dead_code primary spans
#   $excluded    - [{id, reason}, ...]              attribute-based exclusions
#   $impl_info   - [{id, trait, type}, ...]          enclosing impl of each method (source-scanned)
#   $dead_traits - ["TraitName", ...]                traits the oracle independently reports dead
#   $dead_types  - ["TypeName", ...]                 types the oracle independently reports dead

def fmt(x): "  \(x.pkg)::\(x.symbol)  (\(x.file):\(x.line), id=\(x.id))";

($helper[0]) as $g
| ($oracle[0] // []) as $oracle_spans
| ($excluded[0] // []) as $attr_excl
| ($impl_info[0] // []) as $impl_list
| ($dead_traits[0] // []) as $dead_trait_list
| ($dead_types[0] // []) as $dead_type_list

# Oracle dead set as an O(1) lookup keyed by "file:line".
| (reduce $oracle_spans[] as $s ({}; . + {($s.file + ":" + ($s.line|tostring)): true})) as $oracle_map

# Attribute-exclusion reasons keyed by function id.
| (reduce $attr_excl[] as $e ({}; . + {($e.id|tostring): $e.reason})) as $attr_map

# Enclosing impl {trait, type} keyed by function id, and the oracle's
# independently-dead trait/type name sets — together drive the trait-impl
# cascade-suppression check (see the comment block in oracle-diff.sh).
| (reduce $impl_list[] as $e ({}; . + {($e.id|tostring): $e})) as $impl_map
| (reduce $dead_trait_list[] as $t ({}; . + {($t): true})) as $dead_trait_map
| (reduce $dead_type_list[] as $t ({}; . + {($t): true})) as $dead_type_map

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
    | ($f.file + ":" + ($f.line|tostring)) as $key
    | ($impl_map[($f.id|tostring)]) as $im
    | ($im != null and ($dead_trait_map[$im.trait] // false) and ($dead_type_map[$im.type] // false)) as $cascade
    | . + {
        excluded_reason: (
          if $f.generated then "generated"
          elif $f.test then "test (never compiled under plain `cargo check`)"
          elif ($f.pkg | split("::") | any(. == "test" or . == "tests"))
            then "test-module-nested (heuristic: pkg path contains a test/tests segment)"
          elif ($attr_map[($f.id|tostring)] != null)
            then "attribute: " + $attr_map[($f.id|tostring)]
          elif $cascade
            then "trait-impl-cascade (trait `\($im.trait)` and type `\($im.type)` both reported dead by the oracle)"
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
    } | tostring)
