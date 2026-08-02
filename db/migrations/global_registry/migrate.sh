#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${DB_URL:-}" ]]; then
	echo "DB_URL is required" >&2
	exit 1
fi

if ! command -v goose >/dev/null 2>&1; then
	echo "goose is required (https://github.com/pressly/goose)" >&2
	exit 1
fi

direction="${1:-up}"
migrations_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

case "$direction" in
up | down)
	goose -dir "$migrations_dir" postgres "$DB_URL" "$direction"
	;;
*)
	echo "usage: $0 [up|down]" >&2
	exit 1
	;;
esac
