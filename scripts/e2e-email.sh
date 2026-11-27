#!/usr/bin/env bash
# End-to-end check of candidate email (ATS plan, phase 3) against the local stack (curl + jq).
#
#   reply-to setting -> template -> preview -> send now -> scheduled send and cancel -> history
#   -> disqualify with a written message -> limits
#
# Needs a company with a published job and an undecided application: run `STATE_FILE=state.env scripts/e2e-apply.sh`
# first and pass the file. Run scripts/hiring-outbox (cmd/hiring-outbox) afterwards to see messages go out; this
# script checks what the API stores and returns.
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
ok() { printf 'ok: %s\n' "$1"; }
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

step "0. login"
api POST /auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"; want 200 login
CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"

step "1. pick two undecided applications"
api GET "/jobs/$JOB/applications?status=received,in_review&limit=5"; want 200 list
mapfile -t APPS < <(jq -r '.data.items[].id' <<<"$LAST_BODY")
[ "${#APPS[@]}" -ge 2 ] || fail "need two undecided applications on the job"
A1="${APPS[0]}"; A2="${APPS[1]}"

step "2. reply-to"
api PUT /hiring/email-settings '{"reply_to":"not an email"}'; want 400 "bad address"
api PUT /hiring/email-settings '{"reply_to":"Eve <eve@example.com>"}'; want 400 "no display names"
api PUT /hiring/email-settings '{"reply_to":"jobs@example.com"}'; want 200 "set reply-to"
api GET /hiring/email-settings; want 200 settings
[ "$(jq -r '.data.reply_to' <<<"$LAST_BODY")" = jobs@example.com ] || fail "reply-to is saved"

step "3. templates"
NAME="Interview invite $(date +%s)"
api POST /hiring/email-templates "$(jq -nc --arg n "$NAME" '{name:$n, subject:"Next steps for {{job_title}}", body:"Hi {{candidate_name}},\n\nWe would like to talk about {{job_title}} at {{company}}.\n\n{{sender_name}}"}')"; want 201 "create template"
TPL="$(jq -r '.data.id' <<<"$LAST_BODY")"
api POST /hiring/email-templates "$(jq -nc --arg n "$NAME" '{name:$n, subject:"s", body:"b"}')"; want 409 "duplicate name"
api POST /hiring/email-templates '{"name":"Bad","subject":"s","body":"Dear {{first_name}}"}'; want 400 "unknown placeholder"
api GET /hiring/email-templates; want 200 list

step "4. preview and send"
api POST "/jobs/$JOB/applications/$A1/emails/preview" "$(jq -nc --arg t "$TPL" '{template_id:$t}')"; want 200 preview
grep -q '{{' <<<"$LAST_BODY" && fail "placeholders are filled in the preview"
api POST "/jobs/$JOB/applications/$A1/emails" '{"subject":"Hello","body":"Hi {{candidate_name}}","recipient":"someone@else.test"}'; want 400 "there is no recipient field"
api POST "/jobs/$JOB/applications/$A1/emails" "$(jq -nc --arg t "$TPL" '{template_id:$t}')"; want 201 "send now"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = queued ] || fail "queued for the sender"
SOON="$(date -u -v+2H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+2 hours' +%Y-%m-%dT%H:%M:%SZ)"
api POST "/jobs/$JOB/applications/$A1/emails" "$(jq -nc --arg t "$TPL" --arg at "$SOON" '{template_id:$t, schedule_at:$at}')"; want 201 "schedule"
SCHED="$(jq -r '.data.id' <<<"$LAST_BODY")"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = scheduled ] || fail "scheduled"
api POST "/jobs/$JOB/applications/$A1/emails" "$(jq -nc --arg t "$TPL" '{template_id:$t, schedule_at:"2020-01-01T00:00:00Z"}')"; want 400 "the past"
api DELETE "/jobs/$JOB/applications/$A1/emails/$SCHED"; want 204 "cancel"
api DELETE "/jobs/$JOB/applications/$A1/emails/$SCHED"; want 409 "cancel twice"
api GET "/jobs/$JOB/applications/$A1/emails"; want 200 history
[ "$(jq '.data | length' <<<"$LAST_BODY")" = 2 ] || fail "two messages in the history"
api GET "/jobs/$JOB/applications/$A1/activity?limit=100"; want 200 activity
for t in message_queued message_cancelled; do
  jq -e --arg t "$t" '[.data.items[].type] | index($t) != null' <<<"$LAST_BODY" >/dev/null || fail "the timeline has $t"
done

step "5. disqualify with a message"
# A received application goes through review first; ignore the answer when it is in review already.
api POST "/jobs/$JOB/applications/$A2/status" '{"status":"in_review"}' || true
api POST "/jobs/$JOB/applications/$A2/status" "$(jq -nc '{status:"rejected", reason:"not_a_fit", message:{subject:"Update on {{job_title}}", body:"Dear {{candidate_name}}, thank you for applying."}}')"; want 200 "reject with a message"
api GET "/jobs/$JOB/applications/$A2/emails"; want 200 history
[ "$(jq '.data | length' <<<"$LAST_BODY")" = 1 ] || fail "the decision message is in the history"

step "6. cleanup"
api DELETE "/hiring/email-templates/$TPL"; want 204 "delete template"
printf '\nSUCCESS: email e2e completed (job %s)\n' "$JOB"
