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
	DB_URL="$url" "$migrations_dir/migrate.sh" "$direction"
done

echo "All regional migrations complete."
