#!/usr/bin/env bash
# Reports how many jobs are left in the Fluvio email queue (the legacy path drained by cmd/email-worker), so an
# operator knows when email-worker can be retired. Read-only. See docs/microservices/notifications.md.
#
#   DB_URL_GLOBAL=postgres://... scripts/email-queue-depth.sh
#   make email-queue-depth                     # uses DB_URL_GLOBAL from the environment
#   LOCAL=1 make email-queue-depth             # against the local docker stack (compose service `postgres`)
#
# Exit code: 0 queue is empty (nothing pending, scheduled, retrying or running), 1 jobs remain, 2 error.
# Dead jobs are reported but do not block retirement: they need a human either way.
set -euo pipefail

QUEUE="${EMAIL_QUEUE:-email}"
if [[ ! "$QUEUE" =~ ^[a-z0-9_-]+$ ]]; then
	echo "invalid EMAIL_QUEUE" >&2
	exit 2
fi

SQL="SELECT state::text, count(*) FROM fluvio.fluvio_jobs
     WHERE queue = '${QUEUE}' AND state NOT IN ('completed', 'cancelled', 'dead')
     GROUP BY 1 ORDER BY 1"
DEAD_SQL="SELECT count(*) FROM fluvio.fluvio_dead_jobs WHERE queue = '${QUEUE}' AND replayed_at IS NULL"

run_psql() {
	if [[ "${LOCAL:-}" == "1" ]]; then
		docker compose -f docker-compose.yml -f deploy/local/compose.split.yml exec -T postgres \
			psql -U openhr -d openhr_global -v ON_ERROR_STOP=1 -tA -F ' ' -c "$1"
	else
		if [[ -z "${DB_URL_GLOBAL:-}" ]]; then
			echo "DB_URL_GLOBAL is required (or LOCAL=1 for the local docker stack)" >&2
			exit 2
		fi
		psql "$DB_URL_GLOBAL" -v ON_ERROR_STOP=1 -tA -F ' ' -c "$1"
	fi
}

rows="$(run_psql "$SQL")" || { echo "query failed" >&2; exit 2; }
dead="$(run_psql "$DEAD_SQL")" || { echo "query failed" >&2; exit 2; }

total=0
if [[ -n "$rows" ]]; then
	while read -r state n; do
		printf '%-10s %s\n' "$state" "$n"
		total=$((total + n))
	done <<<"$rows"
fi
echo "queue=${QUEUE} outstanding=${total} dead_unreplayed=${dead:-0}"
if [[ "$total" -eq 0 ]]; then
	echo "EMPTY: email-worker has nothing left to drain"
	exit 0
fi
echo "NOT EMPTY: keep email-worker running"
exit 1
