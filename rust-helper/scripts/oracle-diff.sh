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
#   - a method declared directly inside a `trait Name { .. }` body (Task 9:
#     `enumerate.rs` now enumerates these). Empirically verified: rustc's
#     dead_code lint, whether the method is default-bodied or bodyless,
#     whether the trait is genuinely dead or genuinely live via some impl
#     elsewhere, NEVER emits a diagnostic on the method's own declaration
#     line — only ever one "trait `X` is never used" on the TRAIT's own
#     line, and only when the whole trait is unreachable. So the method's
#     own (file,line) key can carry no verdict either way — unlike the
#     impl-cascade case above, this is not a narrowing of an
#     independently-confirmed-dead pair, it is a structural gap in what the
#     compiler can tell us per method. A live default body's OWN outgoing
#     calls (e.g. `Tr::m`'s call to a free function) are NOT affected: the
#     callee is a normal function with its own line, diagnosed normally.
#     Detected the same way as the impl-cascade scan, by a bounded upward
#     (or same-line, for a single-line `trait X { fn m(&self); }`) scan for
#     the enclosing `trait Name {` header.
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
: > "$WORK/decl-trait-info.jsonl"
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
      # Bash parameter-expansion removal (${l//[^{]/}) mis-parses a pattern
      # containing a literal `}` — verified directly: it returns garbage for
      # closes (the whole line, near enough), not the `}` count. tr -dc
      # (delete all bytes NOT in the given set) has no such ambiguity.
      opens_n=$(tr -dc '{' <<<"$l" | wc -c)
      closes_n=$(tr -dc '}' <<<"$l" | wc -c)
      depth=$(( depth + closes_n - opens_n ))
      if (( depth < 0 )); then depth=0; fi
      j=$((j - 1))
    done
    if [[ -n "$impl_trait" && -n "$impl_type" ]]; then
      jq -cn --argjson id "$id" --arg trait "$impl_trait" --arg type "$impl_type" \
        '{id: $id, trait: $trait, type: $type}' >> "$WORK/impl-info.jsonl"
    fi

    # Trait-DECLARATION detection (Task 9): is this method declared directly
    # inside a `trait Name { .. }` body, as opposed to an impl? Needed
    # because when a trait is entirely unused, rustc's dead_code lint emits
    # exactly ONE diagnostic, on the trait's OWN declaration line — never a
    # separate one per declared method (verified directly: a default-bodied
    # method inside a genuinely-dead trait produces only "trait `X` is never
    # used", nothing at the method's own line — and when the trait IS used,
    # via some impl elsewhere, there is no diagnostic at all, at any line).
    # Either way the method's own (file,line) key can never independently
    # confirm or deny its liveness, in either direction — every function
    # found here is therefore excluded from the comparison below (see
    # oracle-diff.jq), not compared with a substituted verdict.
    #
    # A line-oriented brace-depth scan (like the impl-cascade one above)
    # cannot be reused as-is: it stops at the FIRST line matching the header
    # regex, without checking whether that line's own brace is genuinely
    # still open by the time scanning reaches the target — which silently
    # misattributes a closed SIBLING block above (proven directly: without
    # this check, testdata/libonly's `ReS::re_m` — an INHERENT-impl-style
    # trait-impl method, not a trait-declaration one — was wrongly tagged
    # with decl_trait="ReTr" by scanning past the already-closed
    # `pub trait ReTr { .. }` above its enclosing `impl ReTr for ReS {`).
    # Fixed with a proper reverse character-level brace match: scan
    # characters back-to-front from just above the function's line (or, for
    # a same-line "trait Name { fn m(&self); }" declaration, from its own
    # line), counting `}` as descending one level and `{` as ascending one;
    # the first `{` reached at level 0 is the true nearest enclosing scope,
    # whatever it is — stop there unconditionally (a non-"trait" enclosing
    # scope, e.g. "impl .. for .. {", correctly means "not a trait
    # declaration method", not "keep looking further up").
    decl_trait="$(TARGET_LINE="$line" perl -e '
      my $target = $ENV{TARGET_LINE} + 0;
      my @lines = <STDIN>;
      my $cur = $lines[$target - 1] // "";
      if ($cur =~ /^\s*(pub\s+)?trait\s+([A-Za-z_][A-Za-z0-9_]*)/) {
        print $2;
        exit;
      }
      my $depth = 0;
      for (my $ln = $target - 1; $ln >= 1 && $ln >= $target - 1000; $ln--) {
        my $text = $lines[$ln - 1];
        next unless defined $text;
        for (my $i = length($text) - 1; $i >= 0; $i--) {
          my $c = substr($text, $i, 1);
          if ($c eq "}") {
            $depth++;
          } elsif ($c eq "{") {
            if ($depth == 0) {
              if ($text =~ /^\s*(pub\s+)?trait\s+([A-Za-z_][A-Za-z0-9_]*)/) {
                print $2;
              }
              exit;
            }
            $depth--;
          }
        }
      }
    ' < "$REPO_ABS/$file" 2>/dev/null)"
    if [[ -n "$decl_trait" ]]; then
      jq -cn --argjson id "$id" --arg trait "$decl_trait" \
        '{id: $id, trait: $trait}' >> "$WORK/decl-trait-info.jsonl"
    fi
  fi
done < "$WORK/scan-targets.tsv"
jq -s '.' "$WORK/excluded-attrs.jsonl" > "$WORK/excluded-attrs.json"
jq -s '.' "$WORK/impl-info.jsonl" > "$WORK/impl-info.json"
jq -s '.' "$WORK/decl-trait-info.jsonl" > "$WORK/decl-trait-info.json"

echo "== computing reachability and diffing against oracle ==" >&2
jq -nr \
  --slurpfile helper "$WORK/helper.json" \
  --slurpfile oracle "$WORK/oracle-dead.json" \
  --slurpfile excluded "$WORK/excluded-attrs.json" \
  --slurpfile impl_info "$WORK/impl-info.json" \
  --slurpfile decl_trait_info "$WORK/decl-trait-info.json" \
  --slurpfile dead_traits "$WORK/dead-traits.json" \
  --slurpfile dead_types "$WORK/dead-types.json" \
  -f "$SCRIPT_DIR/oracle-diff.jq" | tee "$WORK/report.txt"

echo "" >&2
echo "NOTE: this diff only covers functions the helper enumerated. A function" >&2
echo "the helper never emits as a node is invisible here — neither FATAL nor" >&2
echo "report-only, just absent — so FATAL:0 does not by itself mean full parity." >&2
echo "Known enumeration gap: Task 11 (include!-generated code, e.g. OUT_DIR" >&2
echo "build-script output)." >&2
echo "A propagated false-dead-code effect (a live enumerated function whose only" >&2
echo "caller is one of these invisible functions) IS still caught as a normal" >&2
echo "FATAL entry for that caller; what's uncovered is the invisible function's" >&2
echo "own status and untested chained-invisibility cases." >&2

fatal_count="$(grep -o '"fatal":[0-9]*' "$WORK/report.txt" | grep -o '[0-9]*$')"
if [[ "${fatal_count:-0}" -gt 0 ]]; then
  echo "oracle-diff: FATAL — helper reports dead code the oracle considers live" >&2
  exit 1
fi
