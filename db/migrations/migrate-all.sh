#!/usr/bin/env bash
set -euo pipefail

direction="${1:-up}"
migrations_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
migrations_path="db/migrations"
regions=(UK US AFRICA EU)

# Returns 0 if any .sql file under db/migrations changed (or if migrations should run).
sql_files_changed() {
	if [[ "${FORCE_MIGRATE:-}" == "1" ]]; then
		return 0
	fi

	if ! git rev-parse --git-dir >/dev/null 2>&1; then
		# No git (e.g. migrate container): cannot detect changes; run migrations.
		return 0
	fi

	local repo_root
	repo_root="$(git rev-parse --show-toplevel)"

	sql_changed_in_diff() {
		git -C "$repo_root" diff --name-only "$@" -- "$migrations_path" | grep -qE '\.sql$'
	}

	if [[ -n "${MIGRATE_COMPARE_REF:-}" && "${MIGRATE_COMPARE_REF}" != "0000000000000000000000000000000000000000" ]]; then
		sql_changed_in_diff "${MIGRATE_COMPARE_REF}" HEAD
		return $?
	fi

	if sql_changed_in_diff HEAD~1 HEAD 2>/dev/null; then
		return 0
	fi
	if sql_changed_in_diff HEAD; then
		return 0
	fi
	if sql_changed_in_diff --cached; then
		return 0
	fi
	if git -C "$repo_root" ls-files --others --exclude-standard "$migrations_path" | grep -qE '\.sql$'; then
		return 0
	fi

	return 1
}

if [[ "$direction" == "up" ]] && ! sql_files_changed; then
	echo "No SQL migration changes detected; skipping."
	exit 0
fi

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
