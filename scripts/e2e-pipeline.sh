#!/usr/bin/env bash
# End-to-end check of the application pipeline (ATS plan, phase 2) against the local stack (curl + jq).
#
#   a job's stages (seeded) -> custom stage, rename, reorder -> move an application between stages
#   -> notes, tags, follow -> activity timeline -> disqualify with a reason (final) -> bulk move
#
# Needs a company with a published job that has at least two undecided applications (received or in_review):
# run `STATE_FILE=state.env scripts/e2e-apply.sh` first, apply to the job a couple of times, and pass the file.
# The stack is the one of docs/microservices/local-dev.md (web-bff in front of applications).
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
api() { # api METHOD PATH [BODY]
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
stage_id() { jq -r --arg k "$1" '.data[] | select(.kind == $k) | .id' <<<"$LAST_BODY" | head -1; }

step "0. login"
api POST /auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"; want 200 login
CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"

step "1. the default pipeline"
api GET "/jobs/$JOB/stages"; want 200 stages
[ "$(jq '.data | length' <<<"$LAST_BODY")" -ge 6 ] || fail "six default stages"
APPLIED="$(stage_id applied)"; INTERVIEW="$(stage_id interview)"; DISQ="$(stage_id disqualified)"; HIRED="$(stage_id hired)"
[ "$(jq -r '.data[0].kind' <<<"$LAST_BODY")" = applied ] || fail "Applied first"
[ "$(jq -r '.data[-1].kind' <<<"$LAST_BODY")" = disqualified ] || fail "Disqualified last"
ok "Applied first, Disqualified last"

step "2. custom stage, rename, reorder, delete"
api POST "/jobs/$JOB/stages" '{"name":"Phone screen"}'; want 201 "create stage"
CUSTOM="$(jq -r '.data.id' <<<"$LAST_BODY")"
api POST "/jobs/$JOB/stages" '{"name":"phone SCREEN"}'; want 409 "duplicate name"
api PATCH "/jobs/$JOB/stages/$CUSTOM" '{"name":"Screening call"}'; want 200 rename
api PATCH "/jobs/$JOB/stages/$APPLIED" '{"name":"Inbox"}'; want 409 "Applied is locked"
api DELETE "/jobs/$JOB/stages/$DISQ"; want 409 "Disqualified is locked"
api DELETE "/jobs/$JOB/stages/$CUSTOM"; want 204 "an empty custom stage can go"
api POST "/jobs/$JOB/stages" '{"name":"Phone screen"}'; want 201 "create again"
CUSTOM="$(jq -r '.data.id' <<<"$LAST_BODY")"
api GET "/jobs/$JOB/stages"; want 200 stages
OPEN="$(jq -c '[.data[] | select(.final | not) | .id]' <<<"$LAST_BODY")"
REORDERED="$(jq -c --arg c "$CUSTOM" '[.[0], $c] + (.[1:] | map(select(. != $c)))' <<<"$OPEN")"
api PUT "/jobs/$JOB/stages/order" "$(jq -nc --argjson ids "$REORDERED" '{stage_ids:$ids}')"; want 200 reorder
[ "$(jq -r '.data[1].id' <<<"$LAST_BODY")" = "$CUSTOM" ] || fail "custom stage is second"
api PUT "/jobs/$JOB/stages/order" "$(jq -nc --arg a "$CUSTOM" '{stage_ids:[$a]}')"; want 400 "an incomplete order"
ok "stage management behaves"

step "3. pick undecided applications"
api GET "/jobs/$JOB/applications?status=received,in_review&limit=5"; want 200 list
mapfile -t APPS < <(jq -r '.data.items[].id' <<<"$LAST_BODY")
[ "${#APPS[@]}" -ge 2 ] || fail "need two undecided applications on the job (found ${#APPS[@]})"
A1="${APPS[0]}"; A2="${APPS[1]}"
jq -e '.data.stage_counts | type == "object"' <<<"$LAST_BODY" >/dev/null || fail "stage_counts in the list"

step "4. move, notes, tags, follow"
api POST "/jobs/$JOB/applications/$A1/stage" "$(jq -nc --arg s "$CUSTOM" '{stage_id:$s}')"; want 200 "move to the custom stage"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = in_review ] || fail "received becomes in_review"
api POST "/jobs/$JOB/applications/$A1/stage" "$(jq -nc --arg s "$INTERVIEW" '{stage_id:$s}')"; want 200 "move to Interview"
api POST "/jobs/$JOB/applications/$A1/notes" '{"body":"Strong on systems design."}'; want 201 "add note"
NOTE="$(jq -r '.data.id' <<<"$LAST_BODY")"
api PATCH "/jobs/$JOB/applications/$A1/notes/$NOTE" '{"body":"Strong on systems design, weaker on testing."}'; want 200 "edit own note"
api GET "/jobs/$JOB/applications/$A1/notes"; want 200 notes
[ "$(jq '.data | length' <<<"$LAST_BODY")" = 1 ] || fail "one note"
api POST /hiring/tags "$(jq -nc --arg n "Referral-$(date +%s)" '{name:$n, color:"green"}')"; want 201 "create tag"
TAG="$(jq -r '.data.id' <<<"$LAST_BODY")"
api PUT "/jobs/$JOB/applications/$A1/tags" "$(jq -nc --arg t "$TAG" '{tag_ids:[$t]}')"; want 200 "set tags"
api PUT "/jobs/$JOB/applications/$A1/follow"; want 204 follow
api GET "/jobs/$JOB/applications?tag=$TAG"; want 200 "filter by tag"
[ "$(jq '.data.items | length' <<<"$LAST_BODY")" = 1 ] || fail "tag filter finds the application"
api GET "/jobs/$JOB/applications?followed=true"; want 200 "filter by follow"
[ "$(jq -r --arg a "$A1" '[.data.items[].id] | index($a) != null' <<<"$LAST_BODY")" = true ] || fail "followed list has it"
api GET "/jobs/$JOB/applications?stage=$INTERVIEW"; want 200 "filter by stage"
ok "moves, notes, tags and follows work"

step "5. activity"
api GET "/jobs/$JOB/applications/$A1/activity?limit=2"; want 200 activity
[ "$(jq '.data.items | length' <<<"$LAST_BODY")" = 2 ] || fail "two entries on the first page"
CURSOR="$(jq -r '.data.next_cursor // empty' <<<"$LAST_BODY")"
[ -n "$CURSOR" ] || fail "there is a second page"
api GET "/jobs/$JOB/applications/$A1/activity?cursor=$CURSOR"; want 200 "second page"
api GET "/jobs/$JOB/applications/$A1/activity?limit=100"; want 200 "all of it"
for t in applied stage_changed status_changed note_added note_edited tags_changed; do
  jq -e --arg t "$t" '[.data.items[].type] | index($t) != null' <<<"$LAST_BODY" >/dev/null || fail "the timeline has $t"
done
grep -q "systems design" <<<"$LAST_BODY" && fail "note text must not appear in the timeline"
ok "timeline complete and free of note text"

step "6. disqualify is final"
api POST "/jobs/$JOB/applications/$A1/stage" "$(jq -nc --arg s "$DISQ" '{stage_id:$s}')"; want 400 "a reason is required"
api POST "/jobs/$JOB/applications/$A1/stage" "$(jq -nc --arg s "$DISQ" '{stage_id:$s, reason:"made_up"}')"; want 400 "an unknown reason"
api GET /hiring/disqualification-reasons; want 200 reasons
api POST "/jobs/$JOB/applications/$A1/stage" "$(jq -nc --arg s "$DISQ" '{stage_id:$s, reason:"not_a_fit", note:"Thanks for your time."}')"; want 200 disqualify
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = rejected ] || fail "status is rejected"
api POST "/jobs/$JOB/applications/$A1/stage" "$(jq -nc --arg s "$INTERVIEW" '{stage_id:$s}')"; want 409 "no way back"
api GET "/jobs/$JOB/applications/$A1"; want 200 detail
[ "$(jq -r '.data.disqualify_reason' <<<"$LAST_BODY")" = not_a_fit ] || fail "the reason is on the application"
api DELETE "/jobs/$JOB/stages/$DISQ"; want 409 "still locked"
ok "a disqualified candidate stays disqualified"

step "7. bulk"
api POST "/jobs/$JOB/applications/bulk/stage" "$(jq -nc --arg a "$A1" --arg b "$A2" --arg s "$INTERVIEW" '{application_ids:[$a,$b], stage_id:$s}')"; want 200 bulk
[ "$(jq -r --arg a "$A1" '.data[] | select(.application_id == $a) | .reason' <<<"$LAST_BODY")" = final ] || fail "the decided one is reported as final"
[ "$(jq -r --arg b "$A2" '.data[] | select(.application_id == $b) | .ok' <<<"$LAST_BODY")" = true ] || fail "the open one moved"
api POST "/jobs/$JOB/applications/bulk/stage" "$(jq -nc --arg b "$A2" --arg s "$DISQ" '{application_ids:[$b], stage_id:$s}')"; want 400 "bulk disqualify needs a reason"
api PUT "/jobs/$JOB/applications/$A2/follow"; want 204 follow
api DELETE "/jobs/$JOB/applications/$A2/follow"; want 204 unfollow
api DELETE "/hiring/tags/$TAG"; want 204 "delete the tag"
api POST "/jobs/$JOB/applications/$A2/stage" "$(jq -nc --arg s "$HIRED" '{stage_id:$s}')"; want 200 "hire through the stage"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = hired ] || fail "hired"

printf '\nSUCCESS: pipeline e2e completed (job %s)\n' "$JOB"
