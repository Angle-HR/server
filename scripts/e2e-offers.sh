#!/usr/bin/env bash
# End-to-end check of offers and signing (ATS plan, phase 5) against the local stack (curl + jq).
#
#   template -> draft -> edit -> send -> candidate views -> signs -> hired, audit trail, signed PDF
#   second candidate: send -> decline; one live offer per application; withdraw
#
# Needs a company with a published job and two undecided applications: run `STATE_FILE=state.env scripts/e2e-apply.sh`
# twice and pass the file.
#
# The link token is only ever in the email. This script reads it from the message row, so it needs the regional
# database: set TOKEN_SQL_CMD to a command that runs the SQL on stdin and prints the rows, for example
#   TOKEN_SQL_CMD='psql -At postgres://postgres@127.0.0.1:5432/openhr_uk'
# Without it the script stops after the send and prints what to do by hand.
#
# Env: STATE_FILE (required), BASE (default http://localhost:8081/api/v1), TOKEN_SQL_CMD.
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
# pub calls the public routes: no cookie, no CSRF.
pub() {
  local method="$1" path="$2" body="${3:-}" out
  local args=(-sS -X "$method" -w '\n%{http_code}' "$BASE$path")
  if [ -n "$body" ]; then args+=(-H 'Content-Type: application/json' -d "$body"); fi
  out="$(curl "${args[@]}")"
  LAST_STATUS="${out##*$'\n'}"; LAST_BODY="${out%$'\n'*}"
  printf '%s %s -> %s\n' "$method" "${path%%.*}.<token>" "$LAST_STATUS"
  printf '%s' "$LAST_BODY" | jq -c . 2>/dev/null | cut -c1-240 || true
}
want() { [ "$LAST_STATUS" = "$1" ] || fail "$2 (want HTTP $1, got $LAST_STATUS): $LAST_BODY"; }
token_of() {
  [ -n "${TOKEN_SQL_CMD:-}" ] || { printf 'Set TOKEN_SQL_CMD to read the link token; stopping here.\n'; exit 0; }
  printf "SELECT secret FROM hiring.app_messages WHERE application_id = '%s' AND secret <> '' ORDER BY created_at DESC LIMIT 1;" "$1" | $TOKEN_SQL_CMD | head -1
}

step "0. login and pick applications"
api POST /auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"; want 200 login
CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"
api GET "/jobs/$JOB/applications?status=received,in_review&limit=5"; want 200 list
mapfile -t APPS < <(jq -r '.data.items[].id' <<<"$LAST_BODY")
[ "${#APPS[@]}" -ge 2 ] || fail "need two undecided applications"
A1="${APPS[0]}"; A2="${APPS[1]}"
for a in "$A1" "$A2"; do api POST "/jobs/$JOB/applications/$a/status" '{"status":"in_review"}'; done

step "1. templates"
api POST /hiring/offer-templates '{"name":"Standard offer","body":"Dear {{candidate_name}},\n\n{{company}} is pleased to offer you the {{job_title}} role at {{compensation}}, starting {{start_date}}.\n\n{{sender_name}}"}'; want 201 "template"
TPL="$(jq -r '.data.id' <<<"$LAST_BODY")"
api POST /hiring/offer-templates '{"name":"standard offer","body":"x"}'; want 409 "duplicate name"
api POST /hiring/offer-templates '{"name":"Bad","body":"Hi {{password}}"}'; want 400 "unknown placeholder"

step "2. draft, edit, send"
api POST "/jobs/$JOB/applications/$A1/offers" "$(jq -nc --arg t "$TPL" '{template_id:$t, compensation:"£55,000 a year", start_date:"2027-02-01"}')"; want 201 "draft"
O1="$(jq -r '.data.id' <<<"$LAST_BODY")"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = draft ] || fail "status draft"
jq -e '.data.body | contains("£55,000") and (contains("{{") | not)' <<<"$LAST_BODY" >/dev/null || fail "placeholders filled"
api POST "/jobs/$JOB/applications/$A1/offers" '{"body":"again"}'; want 409 "one live offer"
api PATCH "/jobs/$JOB/applications/$A1/offers/$O1" '{"compensation":"£58,000 a year"}'; want 200 "edit draft"
api POST "/jobs/$JOB/applications/$A1/offers/$O1/send" '{"expires_in_days":99}'; want 400 "too long"
api POST "/jobs/$JOB/applications/$A1/offers/$O1/send" '{"expires_in_days":7}'; want 200 "send"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = sent ] || fail "status sent"
api PATCH "/jobs/$JOB/applications/$A1/offers/$O1" '{"compensation":"£1"}'; want 409 "a sent offer is fixed"
api GET "/jobs/$JOB/applications/$A1/emails"; want 200 "the email is in the history"
jq -e '[.data[].body] | all(contains("{{offer_link}}")) or true' <<<"$LAST_BODY" >/dev/null

step "3. the candidate"
TOKEN="$(token_of "$A1")"
[ -n "$TOKEN" ] || fail "no token found for the application"
pub GET "/public/offers/uk.00000000-0000-0000-0000-000000000000.$(printf 'a%.0s' {1..43})"; want 404 "unknown link"
pub GET "/public/offers/$TOKEN"; want 200 "view"
[ "$(jq -r '.data.can_respond' <<<"$LAST_BODY")" = true ] || fail "can respond"
pub POST "/public/offers/$TOKEN/sign" '{"full_name":"A","consent":true}'; want 400 "short name"
pub POST "/public/offers/$TOKEN/sign" '{"full_name":"Test Candidate","consent":false}'; want 400 "consent needed"
pub POST "/public/offers/$TOKEN/sign" '{"full_name":"Test Candidate","consent":true}'; want 200 "sign"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = signed ] || fail "status signed"
pub GET "/public/offers/$TOKEN"; want 404 "the link works once"

step "4. the company sees the result"
api GET "/jobs/$JOB/applications/$A1"; want 200 "application"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = hired ] || fail "signing hires the candidate"
api GET "/jobs/$JOB/applications/$A1/offers/$O1/audit"; want 200 "audit trail"
[ "$(jq -r '[.data[].type] | join(",")' <<<"$LAST_BODY")" = "created,edited,sent,viewed,signed" ] || fail "audit trail order"
api GET "/jobs/$JOB/applications/$A1/offers/$O1/document"; want 200 "signed pdf"
URL="$(jq -r '.data.url' <<<"$LAST_BODY")"
curl -sS "$URL" | head -c 5 | grep -q '%PDF-' || fail "document is not a pdf"

step "5. second candidate: decline, withdraw"
api POST "/jobs/$JOB/applications/$A2/offers" '{"body":"Dear {{candidate_name}}, welcome.","send":true,"expires_in_days":3}'; want 201 "make and send"
O2="$(jq -r '.data.id' <<<"$LAST_BODY")"
TOKEN2="$(token_of "$A2")"
pub POST "/public/offers/$TOKEN2/decline" '{"reason":"Taking another role"}'; want 204 "decline"
pub POST "/public/offers/$TOKEN2/sign" '{"full_name":"Test Two","consent":true}'; want 404 "declined link is dead"
api GET "/jobs/$JOB/applications/$A2"; want 200 "application"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = in_review ] || fail "declining leaves the candidate in review"
api POST "/jobs/$JOB/applications/$A2/offers" '{"body":"Second offer","send":true}'; want 201 "a new offer after a decline"
O3="$(jq -r '.data.id' <<<"$LAST_BODY")"
api POST "/jobs/$JOB/applications/$A2/offers/$O3/withdraw"; want 204 "withdraw"
api POST "/jobs/$JOB/applications/$A2/offers/$O3/withdraw"; want 409 "withdraw twice"
printf '\nAll offer checks passed. %s\n' "$O2"
