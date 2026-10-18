#!/usr/bin/env bash
# bench-allocs.sh BASE_REF: run the benchmarks on BASE_REF and on the working
# tree, and fail if any benchmark allocates more per operation than before.
#
# Allocation counts are the same on every machine, unlike time, so a shared
# CI runner can compare them. A benchmark that is new, or gone, is skipped.
# Allowed growth: 10% and at least 2 allocations, for the noise of
# background goroutines.
set -euo pipefail

base_ref=${1:?usage: bench-allocs.sh BASE_REF}
root=$(git rev-parse --show-toplevel)
work=$(mktemp -d)
trap 'git -C "$root" worktree remove --force "$work/base" 2>/dev/null || true; rm -rf "$work"' EXIT

bench() { # dir -> "name allocs" lines
	(cd "$1" && go test -run '^$' -bench . -benchmem -benchtime=200x ./internal/... |
		awk '/^Benchmark/ { for (i = 1; i <= NF; i++) if ($i == "allocs/op") print $1, $(i-1) }' |
		sed -E 's/-[0-9]+ / /')
}

git -C "$root" worktree add --detach "$work/base" "$base_ref" >/dev/null
bench "$work/base" >"$work/base.txt"
bench "$root" >"$work/head.txt"

awk '
	NR == FNR { base[$1] = $2; next }
	($1 in base) {
		b = base[$1]; h = $2
		verdict = "ok"
		if (h > b * 1.1 && h - b >= 2) { verdict = "MORE"; bad = 1 }
		printf "%-50s %8d -> %-8d %s\n", $1, b, h, verdict
	}
	END { exit bad }
' "$work/base.txt" "$work/head.txt"
