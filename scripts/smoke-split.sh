#!/usr/bin/env bash
# Health smoke test for the local stack. Checks every service that is RUNNING; services whose profile is not enabled
# are reported as SKIP. Core services must be up and healthy or the script exits 1.
#
# For each running service: docker health status (if it has a healthcheck) + HTTP /healthz on its published port,
# and /readyz where the service implements it (a 404 on /readyz is only noted: not every service has one yet).
set -uo pipefail

cd "$(dirname "$0")/.."
COMPOSE=(docker compose -f docker-compose.yml -f deploy/local/compose.split.yml)

# name|host port|health path|core(1=must run)
SERVICES=(
  "postgres||-|1"
  "redis||-|1"
  "minio|${MINIO_API_PORT:-9000}|/minio/health/live|1"
  "mailhog|${MAILHOG_UI_PORT:-8025}|/api/v2/messages?limit=1|1"
  "nats|${NATS_MONITOR_PORT:-8222}|/healthz|1"
  "identity|${IDENTITY_PORT:-8101}|/healthz|0"
  "org|${ORG_PORT:-8102}|/healthz|0"
  "hiring|${HIRING_PORT:-8103}|/healthz|0"
  "applications|${APPLICATIONS_PORT:-8104}|/healthz|0"
  "notifications|${NOTIFICATIONS_PORT:-8105}|/healthz|0"
  "web-bff|${WEB_BFF_PORT:-8081}|/healthz|0"
  "admin-bff|${ADMIN_BFF_PORT:-8082}|/healthz|0"
  "client|${CLIENT_PORT:-3000}|/|0"
  "admin|${ADMIN_PORT:-3001}|/|0"
  "clamav||-|0"
)

fails=0
row() { printf '%-14s %-9s %-10s %s\n' "$1" "$2" "$3" "$4"; }
row SERVICE STATE HEALTH DETAIL
for entry in "${SERVICES[@]}"; do
  IFS='|' read -r name port path core <<<"$entry"
  cid="$("${COMPOSE[@]}" ps -q "$name" 2>/dev/null | head -1)"
  state=""
  [ -n "$cid" ] && state="$(docker inspect -f '{{.State.Status}}' "$cid" 2>/dev/null)"
  if [ "$state" != running ]; then
    if [ "$core" = 1 ]; then
      row "$name" "${state:-absent}" - "FAIL: core service not running"
      fails=$((fails + 1))
    else
      row "$name" SKIP - "not running (profile off)"
    fi
    continue
  fi
  health="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid")"
  detail=""
  ok=1
  if [ "$health" = unhealthy ] || [ "$health" = starting ]; then
    ok=0
    detail="docker health=$health"
  fi
  if [ -n "$port" ]; then
    code="$(curl -s -o /dev/null -m 5 -w '%{http_code}' "http://127.0.0.1:${port}${path}")"
    case "$code" in
      2??) detail="${detail:+$detail; }GET $path $code" ;;
      *) ok=0; detail="${detail:+$detail; }GET $path -> $code" ;;
    esac
    if [ "$path" = /healthz ] && [ "$name" != nats ]; then
      rc="$(curl -s -o /dev/null -m 5 -w '%{http_code}' "http://127.0.0.1:${port}/readyz")"
      case "$rc" in
        2??) detail="$detail; /readyz $rc" ;;
        404) detail="$detail; /readyz 404 (not implemented)" ;;
        *) ok=0; detail="$detail; /readyz -> $rc" ;;
      esac
    fi
  fi
  if [ "$ok" = 1 ]; then
    row "$name" "$state" "$health" "OK ${detail}"
  else
    row "$name" "$state" "$health" "FAIL ${detail}"
    fails=$((fails + 1))
  fi
done

# One-shot jobs must have completed cleanly.
for job in migrate minio-setup; do
  cid="$("${COMPOSE[@]}" ps -a -q "$job" | head -1)"
  code="$(docker inspect -f '{{.State.ExitCode}}' "$cid" 2>/dev/null)"
  if [ "$code" = 0 ]; then
    row "$job" exited - "OK exit 0"
  else
    row "$job" exited - "FAIL exit ${code:-?}"
    fails=$((fails + 1))
  fi
done

if [ "$fails" -gt 0 ]; then
  echo "SMOKE FAILED: $fails problem(s)"
  exit 1
fi
echo "SMOKE OK"
