#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${DB_URL:-}" ]]; then
	echo "DB_URL is required" >&2
	exit 1
fi

direction="${1:-up}"
migrations_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

psql_cmd() {
	psql "$DB_URL" -v ON_ERROR_STOP=1 "$@"
}

ensure_migrations_table() {
	psql_cmd <<'SQL'
CREATE TABLE IF NOT EXISTS schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
SQL
}

migration_version() {
	local file="$1"
	basename "$file" .up.sql
}

is_applied() {
	local version="$1"
	local count
	count="$(psql_cmd -tAc "SELECT COUNT(*) FROM schema_migrations WHERE version = '$version'")"
	[[ "$count" -gt 0 ]]
}

apply_up() {
	local file="$1"
	local version
	version="$(migration_version "$file")"

	if is_applied "$version"; then
		echo "→ Skipping $version (already applied)"
		return
	fi

	echo "→ Applying $version"
	psql_cmd -f "$file"
	psql_cmd -c "INSERT INTO schema_migrations (version) VALUES ('$version')"
}

apply_down() {
	local version
	version="$(psql_cmd -tAc "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")"
	if [[ -z "$version" ]]; then
		echo "→ No applied migrations to roll back"
		return
	fi

	local file="$migrations_dir/${version}.down.sql"
	if [[ ! -f "$file" ]]; then
		echo "Missing down migration for $version" >&2
		exit 1
	fi

	echo "→ Rolling back $version"
	psql_cmd -f "$file"
	psql_cmd -c "DELETE FROM schema_migrations WHERE version = '$version'"
}

ensure_migrations_table

case "$direction" in
up)
	for file in "$migrations_dir"/*.up.sql; do
		[[ -e "$file" ]] || continue
		apply_up "$file"
	done
	;;
down)
	apply_down
	;;
*)
	echo "usage: $0 [up|down]" >&2
	exit 1
	;;
esac
