#!/usr/bin/env bash
# Checks every branch of the pr/NN-* stack in a throwaway worktree: go build, go vet, go test.
# Tests that need Postgres/NATS skip without their env vars, so no services are required.
#
# Usage: scripts/check-pr-stack.sh [branch ...]    (default: every local pr/NN-* branch, in order)
# Env:   KEEP=1 keeps the worktrees; GOFLAGS is passed through.
# Exit status is non-zero when any branch fails. Never pushes or modifies branches.
set -uo pipefail

repo=$(git rev-parse --show-toplevel)
cd "$repo"

if [ "$#" -gt 0 ]; then
	branches=("$@")
else
	branches=()
	while IFS= read -r b; do branches+=("$b"); done < <(git for-each-ref --sort=refname --format='%(refname:short)' 'refs/heads/pr/[0-9][0-9]-*' | grep -v '^pr/00-')
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/pr-stack-check.XXXXXX")
cleanup() {
	[ "${KEEP:-0}" = 1 ] && return
	for d in "$tmp"/*/; do
		[ -d "$d" ] && git worktree remove --force "$d" >/dev/null 2>&1
	done
	rm -rf "$tmp"
}
trap cleanup EXIT

fail=0
printf '%-28s %-9s %-8s %-8s %-8s\n' BRANCH SHA BUILD VET TEST
for b in "${branches[@]}"; do
	name=${b//\//_}
	wt="$tmp/$name"
	if ! git worktree add --detach -q "$wt" "$b" 2>/dev/null; then
		printf '%-28s %-9s %s\n' "$b" "-" "cannot create worktree"
		fail=1
		continue
	fi
	sha=$(git -C "$wt" rev-parse --short HEAD)
	res=()
	for step in "build ./..." "vet ./..." "test ./..."; do
		if (cd "$wt" && go $step >"$tmp/$name.${step%% *}.log" 2>&1); then
			res+=(ok)
		else
			res+=(FAIL)
			fail=1
			echo "--- $b: go $step failed (tail)" >&2
			tail -n 15 "$tmp/$name.${step%% *}.log" >&2
		fi
	done
	printf '%-28s %-9s %-8s %-8s %-8s\n' "$b" "$sha" "${res[0]}" "${res[1]}" "${res[2]}"
	[ "${KEEP:-0}" = 1 ] || git worktree remove --force "$wt" >/dev/null 2>&1
done

exit "$fail"
