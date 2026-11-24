#!/usr/bin/env bash
# Region-migration saga and the events around it, against a split stack (curl + jq + docker). docs/microservices/org.md.
#
#   Stack: make local-up PROFILES=split  with
#     REGION_MOVE=saga REGION_MOVE_RESUME_INTERVAL=2s ORG_EVENTS=on IDENTITY_EVENTS=on APPLICATIONS_ORG_EVENTS=on
#     HIRING_ORG_EVENTS=on ORG_REMOTE=on IDENTITY_REMOTE=on ORG_APPLY=grpc IDENTITY_QUEUE=grpc SERVER_HIRING_EVENTS=off
#     (plus the UPSTREAM_*_URL and *_REMOTE variables of the split stack, docs/microservices/local-dev.md)
#
#   1  business account: signup, verify, profile (the step that gives it a country) runs the saga:
#      saga row completed, users_registry points at uk, pending rows gone, user + organization + progress in uk,
#      one org.region.moved.v1 in the uk outbox and on the ORG stream; the session keeps working
#   2  a repeated profile step moves nothing again (no new saga, still one event); the address step needs no country
#   3  compensation and resume: identity stopped, the profile step fails and leaves a stalled saga; identity started,
#      the same request succeeds; the failed attempt is compensated, the account is in uk exactly once
#   4  crash recovery: a saga row left in "started" is finished by the resumer without any request
#   5  org.company.deleted/reactivated.v1: admin deletes then reactivates the company (idempotent); applications lifts
#      its tombstone; one reactivated event
#   6  identity.staff.deactivated.v1: deactivating a staff member emits once and the identity-staff consumer ran
#
# Env: BASE (web-bff API base), MAILHOG, NATS_MON, ADMIN_EMAIL/ADMIN_PASSWORD, PG_CONTAINER (default <project>-postgres-1),
# IDENTITY_CONTAINER (default <project>-identity-1), ORG_CONTAINER. Ports default from the compose *_PORT variables;
# with another COMPOSE_PROJECT_NAME they must be set (scripts/lib/e2e-common.sh).
set -euo pipefail

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-common.sh"
e2e_url BASE WEB_BFF_PORT 8081 /api/v1
e2e_url MAILHOG MAILHOG_UI_PORT 8025
e2e_url NATS_MON NATS_MONITOR_PORT 8222
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@anglehr.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-changeme123}"
PROJECT="${COMPOSE_PROJECT_NAME:-openhr-local}"
PG_CONTAINER="${PG_CONTAINER:-$PROJECT-postgres-1}"
IDENTITY_CONTAINER="${IDENTITY_CONTAINER:-$PROJECT-identity-1}"
ORG_CONTAINER="${ORG_CONTAINER:-$PROJECT-org-1}"
PASS="test-password-123"
UK_COUNTRY="a1b2c3d4-e5f6-4789-a012-3456789abcde"
COMPANY_ROLE="60000000-0000-4000-8000-000000000001"
BUSINESS_TYPE="61000000-0000-4000-8000-000000000001"
INDUSTRY="62000000-0000-4000-8000-000000000001"

command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }
AJAR="$(mktemp)"; JARS=()
IDENTITY_STOPPED=0
cleanup() { rm -f "$AJAR" "${JARS[@]:-}"; [ "$IDENTITY_STOPPED" = 1 ] && docker start "$IDENTITY_CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
ok() { printf 'ok: %s\n' "$*"; }
step() { printf '\n=== %s ===\n' "$1"; }
pg() { docker exec -i "$PG_CONTAINER" psql -qAt -U openhr -d "openhr_$1" -c "$2"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
# until_val SECONDS EXPECTED CMD...: run CMD every second until its output equals EXPECTED. until_ge: output >= MIN.
until_val() { local n="$1" want="$2" i; shift 2; for i in $(seq 1 "$n"); do [ "$("$@" 2>/dev/null)" = "$want" ] && return 0; sleep 1; done; return 1; }
until_ge() { local n="$1" min="$2" v i; shift 2; for i in $(seq 1 "$n"); do v="$("$@" 2>/dev/null)"; [ "${v:-0}" -ge "$min" ] 2>/dev/null && return 0; sleep 1; done; return 1; }
stream_msgs() { curl -s "$NATS_MON/jsz?streams=true" | jq -r --arg s "$1" '[.account_details[]?.stream_detail[]? | select(.name==$s) | .state.messages] | add // 0'; }

# new_account EMAIL -> JAR/CSRF/USER_ID of a verified, logged-in business account that has only given its profile.
new_account() {
  local email="$1" body sid otp
  JAR="$(mktemp)"; JARS+=("$JAR")
  body="$(curl -sS -c "$JAR" -b "$JAR" -X POST "$BASE/auth/signup" -H 'Content-Type: application/json' -d "$(jq -nc --arg e "$email" --arg p "$PASS" '{email:$e,password:$p}')")"
  sid="$(jq -r '.data.verification_session_id // empty' <<<"$body")"; [ -n "$sid" ] || fail "signup $email: $body"
  otp="$(for _ in $(seq 1 30); do
    o="$(curl -sG "$MAILHOG/api/v2/search" --data-urlencode kind=to --data-urlencode "query=$email" | jq -r '.items[0].Content.Body // empty' | sed 's/<[^>]*>/ /g' | grep -oE '(^|[^0-9])[0-9]{6}([^0-9]|$)' | head -1 | grep -oE '[0-9]{6}' || true)"
    [ -n "$o" ] && { echo "$o"; break; }; sleep 1; done)"
  [ -n "$otp" ] || fail "no verification code for $email"
  curl -sS -c "$JAR" -b "$JAR" -X POST "$BASE/auth/verify-email" -H 'Content-Type: application/json' -d "$(jq -nc --arg s "$sid" --arg c "$otp" '{verification_session_id:$s,code:$c}')" >/dev/null
  body="$(curl -sS -c "$JAR" -b "$JAR" -X POST "$BASE/auth/login" -H 'Content-Type: application/json' -d "$(jq -nc --arg e "$email" --arg p "$PASS" '{email:$e,password:$p}')")"
  CSRF="$(jq -r '.data.csrf_token // empty' <<<"$body")"; [ -n "$CSRF" ] || fail "login $email: $body"
  USER_ID="$(pg global "SELECT id FROM accounts.pending_users WHERE email = '$email'")"
  [ -n "$USER_ID" ] || fail "no pending user for $email"
}
LAST=""; BODY=""
api() { # api METHOD PATH [BODY] uses JAR and CSRF; sets LAST (status) and BODY
  local out args=(-sS -X "$1" -c "$JAR" -b "$JAR" -w '\n%{http_code}' "$BASE$2")
  [ "$1" != GET ] && args+=(-H "X-CSRF-Token: $CSRF")
  [ -n "${3:-}" ] && args+=(-H 'Content-Type: application/json' -d "$3")
  out="$(curl "${args[@]}")"; LAST="${out##*$'\n'}"; BODY="${out%$'\n'*}"
  local fresh; fresh="$(jq -r '.data.session.csrf_token? // .data.csrf_token? // empty' <<<"$BODY" 2>/dev/null || true)"
  [ -n "$fresh" ] && CSRF="$fresh"
  printf '%s %s -> %s\n' "$1" "$2" "$LAST"
  return 0
}
profile_body() { jq -nc --arg r "$COMPANY_ROLE" --arg c "$UK_COUNTRY" '{account_type:"business",country_id:$c,legal_business_name:"Saga E2E Ltd",legal_full_name:"Sam Saga",company_role_id:$r}'; }
address_body() { jq -nc --arg c "$UK_COUNTRY" '{country_id:$c,entry_mode:"manual",line_1:"10 Downing Street",city:"London",state_or_county:"Greater London",post_code:"SW1A 2AA",identification:{registration_number:"12345678"}}'; }

step "0. preflight"
curl -fsS "${BASE%/api/v1}/healthz" >/dev/null || fail "web-bff not healthy at ${BASE%/api/v1}/healthz"
[ "$(docker exec "$ORG_CONTAINER" printenv REGION_MOVE)" = saga ] || fail "the org service runs with REGION_MOVE=$(docker exec "$ORG_CONTAINER" printenv REGION_MOVE), not saga"
[ "$(pg global "SELECT to_regclass('accounts.region_move_sagas') IS NOT NULL")" = t ] || fail "migration global_registry/000018 is not applied"
ok "stack runs the saga ($PROJECT)"

step "1. business account: the profile step moves it into uk through the saga"
EMAIL1="saga-1-$(date +%s)-$RANDOM@example.com"
ORG_MSGS_BEFORE="$(stream_msgs ORG)"
new_account "$EMAIL1"; U1="$USER_ID"; J1="$JAR"; C1="$CSRF"
api PUT /onboarding/profile "$(profile_body)"
[ "$LAST" = 200 ] || fail "profile step (HTTP $LAST): $BODY"
[ "$(pg global "SELECT state FROM accounts.region_move_sagas WHERE user_id = '$U1'")" = completed ] || fail "saga is not completed"
[ "$(pg global "SELECT region FROM auth.users_registry WHERE user_id = '$U1'")" = uk ] || fail "users_registry does not point at uk"
[ "$(pg global "SELECT (SELECT count(*) FROM accounts.pending_users WHERE id='$U1') + (SELECT count(*) FROM accounts.pending_organizations WHERE owner_user_id='$U1') + (SELECT count(*) FROM accounts.pending_onboarding_progress WHERE user_id='$U1')")" = 0 ] || fail "pending rows remain"
[ "$(pg uk "SELECT (SELECT count(*) FROM accounts.users WHERE id='$U1') || '/' || (SELECT count(*) FROM accounts.organizations WHERE owner_user_id='$U1') || '/' || (SELECT count(*) FROM accounts.onboarding_progress WHERE user_id='$U1')")" = 1/1/1 ] || fail "uk does not hold user/organization/progress exactly once"
ORG1="$(pg uk "SELECT id FROM accounts.organizations WHERE owner_user_id = '$U1'")"
ok "moved: user $U1, organization $ORG1"
[ "$(pg uk "SELECT count(*) FROM events_outbox WHERE type='org.region.moved.v1' AND partition_key='$U1'")" = 1 ] || fail "expected one org.region.moved.v1 in the uk outbox"
until_val 20 1 pg uk "SELECT count(*) FROM events_outbox WHERE type='org.region.moved.v1' AND partition_key='$U1' AND sent_at IS NOT NULL" || fail "the event was not relayed"
[ "$(pg uk "SELECT envelope->'data'->>'company_id' = '$ORG1' AND envelope->'data'->>'region' = 'uk' AND envelope->'data'->>'from_region' = 'global' FROM events_outbox WHERE type='org.region.moved.v1' AND partition_key='$U1'")" = t ] || fail "event payload"
[ "$(stream_msgs ORG)" -gt "$ORG_MSGS_BEFORE" ] || fail "no message reached the ORG stream"
ok "org.region.moved.v1 written in the same region and relayed to the ORG stream"
J1="$JAR"; C1="$CSRF" # the profile step rotated the session and its CSRF token
api GET /onboarding/status; [ "$LAST" = 200 ] || fail "session after the move (HTTP $LAST): $BODY"
ok "the rotated session keeps working"

step "2. repeating the profile step moves nothing again; the address step needs no country"
api PUT /onboarding/profile "$(profile_body)"; [ "$LAST" = 200 ] || fail "repeat (HTTP $LAST): $BODY"
api PUT /onboarding/address "$(address_body | jq -c 'del(.country_id)')"; [ "$LAST" = 200 ] || fail "address without country_id (HTTP $LAST): $BODY"
[ "$(pg global "SELECT count(*) FROM accounts.region_move_sagas WHERE user_id = '$U1'")" = 1 ] || fail "a second saga was created"
[ "$(pg uk "SELECT count(*) FROM events_outbox WHERE type='org.region.moved.v1' AND partition_key='$U1'")" = 1 ] || fail "a second event was written"
ok "one saga, one event"

step "3. identity down: the saga stalls and compensates; identity back: the same request succeeds"
EMAIL3="saga-3-$(date +%s)-$RANDOM@example.com"
new_account "$EMAIL3"; U3="$USER_ID"
docker stop "$IDENTITY_CONTAINER" >/dev/null; IDENTITY_STOPPED=1
api PUT /onboarding/profile "$(profile_body)"
case "$LAST" in 5??) ok "profile step failed while identity is down (HTTP $LAST)";; *) fail "expected 5xx with identity down, got $LAST: $BODY";; esac
S3="$(pg global "SELECT state FROM accounts.region_move_sagas WHERE user_id = '$U3' ORDER BY created_at LIMIT 1")"
case "$S3" in started|compensating|compensated) ok "saga is $S3 (not completed, not committed)";; *) fail "unexpected saga state $S3";; esac
[ "$(pg global "SELECT region FROM auth.users_registry WHERE user_id = '$U3'")" = global ] || fail "registry moved although identity was down"
[ "$(pg global "SELECT count(*) FROM accounts.pending_users WHERE id = '$U3'")" = 1 ] || fail "the pending user is gone"
docker start "$IDENTITY_CONTAINER" >/dev/null; IDENTITY_STOPPED=0
until_val 90 healthy docker inspect -f '{{.State.Health.Status}}' "$IDENTITY_CONTAINER" 2>/dev/null || fail "identity did not become healthy"
sleep 2
api PUT /onboarding/profile "$(profile_body)"
[ "$LAST" = 200 ] || { sleep 5; api PUT /onboarding/profile "$(profile_body)"; }
[ "$LAST" = 200 ] || fail "profile step after identity is back (HTTP $LAST): $BODY"
[ "$(pg global "SELECT region FROM auth.users_registry WHERE user_id = '$U3'")" = uk ] || fail "registry does not point at uk"
[ "$(pg global "SELECT string_agg(state, ',' ORDER BY created_at) FROM accounts.region_move_sagas WHERE user_id = '$U3'")" = compensated,completed ] || fail "expected a compensated then a completed saga: $(pg global "SELECT string_agg(state, ',' ORDER BY created_at) FROM accounts.region_move_sagas WHERE user_id = '$U3'")"
[ "$(pg uk "SELECT (SELECT count(*) FROM accounts.users WHERE id='$U3') || '/' || (SELECT count(*) FROM accounts.organizations WHERE owner_user_id='$U3')")" = 1/1 ] || fail "uk does not hold the account exactly once"
[ "$(pg uk "SELECT count(*) FROM events_outbox WHERE type='org.region.moved.v1' AND partition_key='$U3'")" = 1 ] || fail "expected exactly one org.region.moved.v1"
ok "failed attempt compensated, retry completed, one event"

step "4. crash recovery: a stalled saga is finished by the resumer"
EMAIL4="saga-4-$(date +%s)-$RANDOM@example.com"
new_account "$EMAIL4"; U4="$USER_ID"
pg global "INSERT INTO accounts.region_move_sagas (id, user_id, target_region, state, updated_at) VALUES (gen_random_uuid(), '$U4', 'uk', 'started', now() - interval '10 minutes')" >/dev/null
until_val 60 completed pg global "SELECT state FROM accounts.region_move_sagas WHERE user_id = '$U4'" || fail "the resumer did not finish the saga (set REGION_MOVE_RESUME_INTERVAL=2s on org): state $(pg global "SELECT state FROM accounts.region_move_sagas WHERE user_id = '$U4'")"
[ "$(pg global "SELECT region FROM auth.users_registry WHERE user_id = '$U4'")" = uk ] || fail "registry does not point at uk"
[ "$(pg uk "SELECT count(*) FROM accounts.users WHERE id='$U4'")" = 1 ] || fail "user not copied"
ok "resumer completed the saga without any request"

step "5. org.company.deleted / reactivated"
e2e_admin_login "$AJAR" "$BASE" "$ADMIN_EMAIL" "$ADMIN_PASSWORD" || fail "admin login"
ACSRF="$E2E_ADMIN_CSRF"
adm() { curl -sS -b "$AJAR" -c "$AJAR" -X "$1" "$BASE$2" -H "X-CSRF-Token: $ACSRF" -H 'Content-Type: application/json' ${3:+-d "$3"}; }
adm DELETE "/admin/companies/$ORG1?region=uk" '{"reason":"e2e"}' | jq -e '.data.already_deleted == false' >/dev/null || fail "delete company"
[ "$(pg uk "SELECT deleted_at IS NOT NULL FROM accounts.organizations WHERE id='$ORG1'")" = t ] || fail "company not deleted"
until_val 30 1 pg uk "SELECT count(*) FROM hiring.app_deleted_companies WHERE company_id='$ORG1'" || fail "applications did not record the tombstone (APPLICATIONS_ORG_EVENTS=on?)"
ok "deleted, applications tombstone present"
R="$(adm POST "/admin/companies/$ORG1/reactivate?region=uk" '{"reason":"e2e"}')"
jq -e '.data.already_active == false' >/dev/null <<<"$R" || fail "reactivate: $R"
[ "$(pg uk "SELECT deleted_at IS NULL FROM accounts.organizations WHERE id='$ORG1'")" = t ] || fail "company still deleted"
adm POST "/admin/companies/$ORG1/reactivate?region=uk" | jq -e '.data.already_active == true' >/dev/null || fail "second reactivate is not a no-op"
[ "$(pg uk "SELECT count(*) FROM events_outbox WHERE type='org.company.reactivated.v1' AND partition_key='$ORG1'")" = 1 ] || fail "expected exactly one org.company.reactivated.v1"
until_val 30 0 pg uk "SELECT count(*) FROM hiring.app_deleted_companies WHERE company_id='$ORG1'" || fail "applications did not lift the tombstone"
hiring_saw() { docker logs "${HIRING_CONTAINER:-$PROJECT-hiring-1}" 2>&1 | grep -c "company reactivated: its archived jobs stay archived.*company=$ORG1"; }
until_ge 30 1 hiring_saw || fail "hiring-org did not consume the reactivation (HIRING_ORG_EVENTS=on?)"
ok "reactivated once (idempotent), tombstone lifted, hiring consumed it"

step "6. identity.staff.deactivated"
ROLE="$(curl -sS -b "$AJAR" "$BASE/admin/roles" | jq -r '([.data[] | select(.slug != "superadmin")][0] // .data[0]).slug')" # the bootstrap admin keeps the last-superadmin guard satisfied
STAFF="saga-staff-$(date +%s)-$RANDOM@example.com"
SID="$(adm POST /admin/staff "$(jq -nc --arg e "$STAFF" --arg r "$ROLE" '{email:$e,name:"Saga Staff",roles:[$r]}')" | jq -r '.data.id // empty')"
[ -n "$SID" ] || fail "staff invite"
adm PATCH "/admin/staff/$SID" '{"is_active":false}' >/dev/null # an invited member starts inactive: no transition, no event
[ "$(pg global "SELECT count(*) FROM events_outbox WHERE type='identity.staff.deactivated.v1' AND partition_key='$SID'")" = 0 ] || fail "an invited (inactive) member emitted a deactivation"
adm PATCH "/admin/staff/$SID" '{"is_active":true}' | jq -e '.data.is_active == true' >/dev/null || fail "activate staff"
adm PATCH "/admin/staff/$SID" '{"is_active":false}' | jq -e '.data.is_active == false' >/dev/null || fail "deactivate staff"
adm PATCH "/admin/staff/$SID" '{"is_active":false}' >/dev/null
[ "$(pg global "SELECT count(*) FROM events_outbox WHERE type='identity.staff.deactivated.v1' AND partition_key='$SID'")" = 1 ] || fail "expected exactly one identity.staff.deactivated.v1"
until_ge 30 1 pg global "SELECT count(*) FROM events_inbox WHERE consumer='identity-staff'" || fail "the identity-staff consumer did not run (IDENTITY_EVENTS=on?)"
ok "emitted once, consumed by identity-staff"

printf '\nregion saga e2e passed\n'
