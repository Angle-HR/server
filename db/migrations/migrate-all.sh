#!/usr/bin/env bash
set -euo pipefail

direction="${1:-up}"
migrations_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
regions=(UK US AFRICA EU)

for region in "${regions[@]}"; do
	var="DB_URL_${region}"
	url="${!var:-}"
	if [[ -z "$url" ]]; then
		echo "DB_URL_${region} is required" >&2
		exit 1
	fi

	echo "=== Migrating ${region} ==="
	DB_URL="$url" bash "$migrations_dir/migrate.sh" "$direction"
done

echo "All regional migrations complete."

if [[ -z "${DB_URL_GLOBAL:-}" ]]; then
	echo "DB_URL_GLOBAL is required" >&2
	exit 1
fi

echo "=== Migrating GLOBAL ==="
DB_URL="$DB_URL_GLOBAL" bash "$migrations_dir/global_registry/migrate.sh" "$direction"

echo "All migrations complete."
