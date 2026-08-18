#!/usr/bin/env bash
set -euo pipefail

# Creates the target database on first run when connected as a superuser.
# Called by migrate.sh before goose applies schema migrations.
ensure_database() {
	local db_url="$1"

	if [[ -z "$db_url" ]]; then
		echo "DB_URL is required" >&2
		return 1
	fi

	local db_name db_user
	db_name="$(echo "$db_url" | sed -n 's|.*/\([^/?]*\).*|\1|p')"
	db_user="$(echo "$db_url" | sed -n 's|postgres://\([^:@/]*\).*|\1|p')"
	if [[ -z "$db_name" ]]; then
		echo "Could not parse database name from DB_URL" >&2
		return 1
	fi
	if [[ -z "$db_user" ]]; then
		db_user="openhr"
	fi

	local admin_url="${db_url//\/${db_name}/\/postgres}"
	if [[ "$admin_url" == "$db_url" ]]; then
		admin_url="${db_url}?sslmode=disable"
		admin_url="${admin_url//\/${db_name}?/\/postgres?}"
	fi

	if ! command -v psql >/dev/null 2>&1; then
		echo "psql not found; skipping database bootstrap for ${db_name}" >&2
		return 0
	fi

	if psql "$admin_url" -tAc "SELECT 1 FROM pg_database WHERE datname = '${db_name}'" | grep -q 1; then
		return 0
	fi

	echo "Creating database ${db_name}..."
	psql "$admin_url" -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"${db_name}\" OWNER \"${db_user}\""
}
