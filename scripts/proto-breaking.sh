#!/usr/bin/env bash
# Runs `buf breaking` against the given baseline. If the baseline has no proto
# module yet (first introduction of contracts), there is nothing to break: pass.
set -uo pipefail
against="${1:?usage: proto-breaking.sh <buf input>}"
out=$(buf breaking --against "$against" 2>&1)
status=$?
if [ $status -eq 0 ]; then
  echo "proto-breaking: no breaking changes"
  exit 0
fi
if echo "$out" | grep -qiE "had no .proto files|does not exist|no such file|could not read|input contained no"; then
  echo "proto-breaking: baseline has no proto/ yet, skipping ($against)"
  exit 0
fi
echo "$out"
exit $status
