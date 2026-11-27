#!/usr/bin/env bash
# End-to-end check of interviews and compare (ATS plan, phase 4) against the local stack (curl + jq).
#
#   reply-to -> schedule with an invite -> list -> reschedule -> my schedule -> cancel -> complete -> compare
#
# Needs a company with a published job and two undecided applications: run `STATE_FILE=state.env scripts/e2e-apply.sh`
# twice and pass the file. Run cmd/hiring-outbox afterwards to see the invites go out as emails with a calendar part.
#
# Env: STATE_FILE (required), BASE (default http://localhost:8081/api/v1).
set -euo pipefail

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-common.sh"
e2e_url BASE WEB_BFF_PORT 8081 /api/v1
STATE_FILE="${STATE_FILE:?set STATE_FILE to the file written by e2e-apply.sh}"
# shellcheck disable=SC1090
. "$STATE_FILE"
JAR="$(mktemp)"; CSRF=""
trap 'rm -f "$JAR"' EXIT
LAST_STATUS=""; LAST_BODY=""

step() { printf '\n=== %s ===\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
api() {
  local method="$1" path="$2" body="${3:-}" out
  local args=(-sS -X "$method" -c "$JAR" -b "$JAR" -w '\n%{http_code}' "$BASE$path")
  if [ -n "$CSRF" ] && [ "$method" != GET ]; then args+=(-H "X-CSRF-Token: $CSRF"); fi
  if [ -n "$body" ]; then args+=(-H 'Content-Type: application/json' -d "$body"); fi
  out="$(curl "${args[@]}")"
  LAST_STATUS="${out##*$'\n'}"; LAST_BODY="${out%$'\n'*}"
  printf '%s %s -> %s\n' "$method" "$path" "$LAST_STATUS"
  printf '%s' "$LAST_BODY" | jq -c . 2>/dev/null | cut -c1-240 || true
}
want() { [ "$LAST_STATUS" = "$1" ] || fail "$2 (want HTTP $1, got $LAST_STATUS): $LAST_BODY"; }
future() { date -u -v+"$1"d +%Y-%m-%dT10:00:00Z 2>/dev/null || date -u -d "+$1 days" +%Y-%m-%dT10:00:00Z; }

step "0. login and pick applications"
api POST /auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"; want 200 login
CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"
api GET "/jobs/$JOB/applications?status=received,in_review&limit=5"; want 200 list
mapfile -t APPS < <(jq -r '.data.items[].id' <<<"$LAST_BODY")
[ "${#APPS[@]}" -ge 2 ] || fail "need two undecided applications"
A1="${APPS[0]}"; A2="${APPS[1]}"
ME="$(jq -r '.data.user.id // .data.id // empty' <<<"$(curl -sS -b "$JAR" "$BASE/auth/me" || echo '{}')")"

step "1. an invite needs a reply-to address"
api PUT /hiring/email-settings '{"reply_to":""}'; want 200 "clear reply-to"
api POST "/jobs/$JOB/applications/$A1/interviews" "$(jq -nc --arg at "$(future 3)" '{starts_at:$at, timezone:"Europe/London", kind:"video", location:"https://meet.example.com/abc", invite:true}')"; want 400 "invite without reply-to"
api PUT /hiring/email-settings '{"reply_to":"jobs@example.com"}'; want 200 "set reply-to"

step "2. schedule"
api POST "/jobs/$JOB/applications/$A1/interviews" "$(jq -nc --arg at "$(future 3)" '{starts_at:$at, timezone:"Mars/Base", kind:"video", location:"https://meet.example.com/abc"}')"; want 400 "bad zone"
api POST "/jobs/$JOB/applications/$A1/interviews" "$(jq -nc --arg at "$(future 3)" '{starts_at:$at, timezone:"Europe/London", kind:"video", location:"not a link"}')"; want 400 "bad link"
api POST "/jobs/$JOB/applications/$A1/interviews" "$(jq -nc --arg at "2020-01-01T10:00:00Z" '{starts_at:$at, timezone:"Europe/London", kind:"phone"}')"; want 400 "in the past"
api POST "/jobs/$JOB/applications/$A1/interviews" "$(jq -nc --arg at "$(future 3)" --arg me "${ME:-}" '{starts_at:$at, duration_minutes:45, timezone:"Europe/London", kind:"video", location:"https://meet.example.com/abc", notes:"Panel of two", invite:true, invite_note:"Looking forward to it."} + (if $me != "" then {interviewer_ids:[$me]} else {} end)')"; want 201 schedule
IV="$(jq -r '.data.id' <<<"$LAST_BODY")"
[ "$(jq -r '.data.duration_minutes' <<<"$LAST_BODY")" = 45 ] || fail "duration"
api GET "/jobs/$JOB/applications/$A1/interviews"; want 200 list
[ "$(jq '.data | length' <<<"$LAST_BODY")" = 1 ] || fail "one interview"
api GET "/jobs/$JOB/applications/$A1/emails"; want 200 "the invite is in the email history"
[ "$(jq '.data | length' <<<"$LAST_BODY")" -ge 1 ] || fail "invite queued"

step "3. reschedule, then my schedule"
api PATCH "/jobs/$JOB/applications/$A1/interviews/$IV" "$(jq -nc --arg at "$(future 4)" '{starts_at:$at, invite:true}')"; want 200 reschedule
api PATCH "/jobs/$JOB/applications/$A1/interviews/$IV" '{"kind":"teleport"}'; want 400 "bad kind"
if [ -n "${ME:-}" ]; then
  api GET "/hiring/interviews/mine?to=$(future 30 | cut -c1-10)"; want 200 "my schedule"
  jq -e --arg i "$IV" '[.data[].id] | index($i) != null' <<<"$LAST_BODY" >/dev/null || fail "my schedule has the interview"
fi
api GET "/hiring/interviews/mine?from=yesterday"; want 400 "bad date"

step "4. cancel and complete"
api POST "/jobs/$JOB/applications/$A1/interviews/$IV/cancel" '{"notify":true,"note":"Sorry, the role is on hold."}'; want 200 cancel
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = cancelled ] || fail "cancelled"
api POST "/jobs/$JOB/applications/$A1/interviews/$IV/cancel" '{}'; want 409 "cancel twice"
api PATCH "/jobs/$JOB/applications/$A1/interviews/$IV" '{"notes":"x"}'; want 409 "edit a cancelled interview"
api POST "/jobs/$JOB/applications/$A2/interviews" "$(jq -nc --arg at "$(future 5)" '{starts_at:$at, timezone:"UTC", kind:"phone"}')"; want 201 "second interview"
IV2="$(jq -r '.data.id' <<<"$LAST_BODY")"
api POST "/jobs/$JOB/applications/$A2/interviews/$IV2/complete"; want 200 complete
api GET "/jobs/$JOB/applications/$A1/activity?limit=100"; want 200 activity
for t in interview_scheduled interview_updated interview_cancelled; do
  jq -e --arg t "$t" '[.data.items[].type] | index($t) != null' <<<"$LAST_BODY" >/dev/null || fail "the timeline has $t"
done

step "5. compare"
api GET "/jobs/$JOB/applications/compare?ids=$A1,$A2"; want 200 compare
[ "$(jq '.data.applications | length' <<<"$LAST_BODY")" = 2 ] || fail "two candidates"
jq -e '.data.rows | type == "array"' <<<"$LAST_BODY" >/dev/null || fail "rows"
grep -q '"flags"' <<<"$LAST_BODY" && fail "review flags must not be in a comparison"
api GET "/jobs/$JOB/applications/compare?ids=$A1"; want 400 "one is not a comparison"
api GET "/jobs/$JOB/applications/compare?ids=$A1,not-a-uuid"; want 404 "a bad id"
printf '\nSUCCESS: interviews e2e completed (job %s)\n' "$JOB"
