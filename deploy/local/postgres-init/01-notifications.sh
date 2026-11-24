#!/bin/sh
# Runs once, on an empty postgres volume (docker-entrypoint-initdb.d). The migrate container also creates the
# database when it is missing (db/migrations/ensure-database.sh), which covers volumes created before this existed.
set -e
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<-SQL
	SELECT 'CREATE DATABASE openhr_notifications OWNER "$POSTGRES_USER"'
	WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'openhr_notifications')\gexec
SQL
