#!/usr/bin/env bash
# check-artifact builds the server the way a release does and holds the result
# to budgets that otherwise regress in silence: the stripped binary size, which
# grows with every dependency bump, and the time spent in package initialisers
# before main runs, which every CLI call, shell completion and health check
# pays. The AWS linking check runs on the same build, from a copy that keeps
# its symbol table because go tool nm needs one and the shipped binary has none.
#
# Budgets come from the environment, so a deliberate change raises one in the
# commit that justifies it:
#   ARTIFACT_SIZE_BUDGET_BYTES  stripped size of cmd/shinyhub for linux/amd64
#                               (default 56000000)
#   ARTIFACT_INIT_BUDGET_MS     package init time before main (default 50)
#   ARTIFACT_INIT_RUNS          runs whose minimum is held to the budget (default 5)
#   ARTIFACT_OUT_DIR            where the builds are written (default tmp/check-artifact)
set -euo pipefail

cd "$(dirname "$0")/.."

out_dir=${ARTIFACT_OUT_DIR:-tmp/check-artifact}
size_budget=${ARTIFACT_SIZE_BUDGET_BYTES:-56000000}
init_budget_ms=${ARTIFACT_INIT_BUDGET_MS:-50}
init_runs=${ARTIFACT_INIT_RUNS:-5}

mkdir -p "$out_dir"
stripped=$out_dir/shinyhub
symbols=$out_dir/shinyhub-symbols
native=$out_dir/shinyhub-native
trace=$out_dir/inittrace.txt
min_trace=$out_dir/inittrace-min.txt
report=$out_dir/report.md

# The flags and environment .goreleaser.yaml builds with. The version only
# has to be set so that --version runs the same code as a release binary.
#
# Size and linking are measured on one fixed release target, linux/amd64,
# the server binary the container image ships. A native build would make the
# answer depend on the host: darwin/arm64 is several megabytes smaller than
# linux/amd64, so a budget that passes on a workstation fails on a runner.
# Package init has to be timed by running the binary, so it uses a native
# build with the same flags.
release_flags=(-trimpath)
version_flag="-X main.version=check-artifact"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build "${release_flags[@]}" -ldflags "-s -w $version_flag" -o "$stripped" ./cmd/shinyhub
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build "${release_flags[@]}" -ldflags "-w $version_flag" -o "$symbols" ./cmd/shinyhub
CGO_ENABLED=0 go build "${release_flags[@]}" -ldflags "-s -w $version_flag" -o "$native" ./cmd/shinyhub

size=$(wc -c < "$stripped" | tr -d ' ')

# GODEBUG=inittrace=1 reports every package initialiser with its wall-clock
# cost on stderr; their sum is the time before main. The minimum over several
# runs is what the budget holds: a cost paid by the code itself shows in every
# run, while a busy host inflates only some of them.
init_min_ms=
for _ in $(seq "$init_runs"); do
  GODEBUG=inittrace=1 "$native" --version 2> "$trace" > /dev/null
  run_ms=$(awk '/^init /{s += $5} END{printf "%.1f", s}' "$trace")
  if [ -z "$init_min_ms" ] || awk -v a="$run_ms" -v b="$init_min_ms" 'BEGIN{exit !(a < b)}'; then
    init_min_ms=$run_ms
    cp "$trace" "$min_trace"
  fi
done

status=0
{
  if [ "$size" -le "$size_budget" ]; then
    echo "- ok: stripped linux/amd64 size ${size} bytes (budget ${size_budget})"
  else
    echo "- FAIL: stripped linux/amd64 size ${size} bytes exceeds the budget of ${size_budget}"
    status=1
  fi

  if awk -v a="$init_min_ms" -v b="$init_budget_ms" 'BEGIN{exit !(a <= b)}'; then
    echo "- ok: package init ${init_min_ms} ms before main, minimum of ${init_runs} runs (budget ${init_budget_ms} ms)"
  else
    echo "- FAIL: package init ${init_min_ms} ms before main, minimum of ${init_runs} runs, exceeds the budget of ${init_budget_ms} ms"
    echo "  slowest initialisers:"
    sort -k5,5 -rn "$min_trace" | head -n 5 | sed 's/^/      /'
    status=1
  fi

  if python3 scripts/check-sdk-linking.py "$symbols"; then
    echo "- ok: the AWS operations linked into the binary match the allowlist"
  else
    echo "- FAIL: see the linking check output above"
    status=1
  fi

  # Text bytes by package group, so a bump that adds megabytes names its
  # source. The group is the module-shaped prefix of the package path:
  # host/owner/repo on code hosts, host/project elsewhere, and the package
  # itself in the standard library.
  echo
  echo "| text bytes | package group |"
  echo "|---:|---|"
  go tool nm -size -sort size "$symbols" | awk '
    $3 == "T" || $3 == "t" {
      sym = $4
      if (sym ~ /^(type:|go:)/) {
        group = "generated"
      } else {
        n = split(sym, seg, "/")
        take = n
        if (n > 1 && index(seg[1], ".") > 0) {
          take = (seg[1] ~ /^(github\.com|gitlab\.com|bitbucket\.org|golang\.org)$/) ? 3 : 2
          if (take > n) take = n
        }
        if (take == n) sub(/\..*/, "", seg[n])
        group = seg[1]
        for (i = 2; i <= take; i++) group = group "/" seg[i]
      }
      bytes[group] += $2
    }
    END { for (g in bytes) printf "| %d | %s |\n", bytes[g], g }
  ' | sort -t '|' -k2 -rn | head -n 10
} > "$report"

cat "$report"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### Artifact budgets"
    echo
    cat "$report"
  } >> "$GITHUB_STEP_SUMMARY"
fi
exit "$status"
