#!/usr/bin/env bash
# End-to-end test: an individual account hires (docs/microservices/individual-accounts.md). curl + jq.
#
#   individual signup -> profile -> compliance (industry only) -> complete (personal organization, review pending)
#   -> GET /hiring/me works, publish blocked until verified, company actions refused for individuals
#   -> admin sees an "individual" case, rejects it -> the person requests a new review -> admin approves
#   -> DPA + privacy contact -> create + publish a job -> a candidate applies -> the individual sees the application
#   -> small fixes seen through the API: no waitlist permissions, bare IPs in the audit log, email link origin
#   -> superadmin deletes the personal organization (the last step: /hiring/me answers 404 afterwards)
#
# Same stack and env rules as scripts/e2e-apply.sh (see scripts/lib/e2e-common.sh for ports and the admin TOTP login).
# Env: BASE, MAILHOG, ADMIN_EMAIL, ADMIN_PASSWORD, APP_URL (the origin the stack's emails must link to; the link
# check is skipped when unset), ADMIN_AUDIT_IP_CHECK=off to skip the audit IP check.
set -euo pipefail

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-common.sh"
e2e_url BASE WEB_BFF_PORT 8081 /api/v1
e2e_url MAILHOG MAILHOG_UI_PORT 8025
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@anglehr.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-changeme123}"
EMAIL="e2e-individual-$(date +%s)-$RANDOM@example.com"
CANDIDATE="candidate-ind-$(date +%s)-$RANDOM@example.com"
PASS="test-password-123"
INDUSTRY="62000000-0000-4000-8000-000000000001"
UK_COUNTRY="a1b2c3d4-e5f6-4789-a012-3456789abcde"
FIRST="Ada"; LAST="Individual$RANDOM"

command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }
JAR="$(mktemp)"; ADMIN_JAR="$(mktemp)"; ANON_JAR="$(mktemp)"
trap 'rm -f "$JAR" "$ADMIN_JAR" "$ANON_JAR"' EXIT
CSRF=""; ADMIN_CSRF=""; LAST_STATUS=""; LAST_BODY=""; LAST_HEADERS=""

step() { printf '\n=== %s ===\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
ok() { printf 'ok: %s\n' "$1"; }
call() {
  local jar="$1" csrf="$2" method="$3" path="$4" body="${5:-}"
  shift $(( $# < 5 ? $# : 5 ))
  local hdr; hdr="$(mktemp)"
  local args=(-sS -X "$method" -c "$jar" -b "$jar" -D "$hdr" -w '\n%{http_code}' "$BASE$path")
  if [ -n "$csrf" ] && [ "$method" != GET ] && [ "$method" != HEAD ]; then args+=(-H "X-CSRF-Token: $csrf"); fi
  if [ -n "$body" ]; then args+=(-H 'Content-Type: application/json' -d "$body"); fi
  args+=("$@")
  local out; out="$(curl "${args[@]}")"
  LAST_STATUS="${out##*$'\n'}"; LAST_BODY="${out%$'\n'*}"; LAST_HEADERS="$(cat "$hdr")"; rm -f "$hdr"
  printf '%s %s -> %s\n' "$method" "$path" "$LAST_STATUS"
  printf '%s' "$LAST_BODY" | jq -c . 2>/dev/null | cut -c1-240 || true
}
api() {
  call "$JAR" "$CSRF" "$@"
  local fresh; fresh="$(printf '%s' "$LAST_BODY" | jq -r '.data.session.csrf_token? // .data.csrf_token? // empty' 2>/dev/null)" || fresh=""
  [ -n "$fresh" ] && CSRF="$fresh"
  return 0
}
adm() { call "$ADMIN_JAR" "$ADMIN_CSRF" "$@"; }
anon() { call "$ANON_JAR" "" "$@"; }
expect() { case "$LAST_STATUS" in 2??) ;; *) fail "$1 (HTTP $LAST_STATUS): $LAST_BODY" ;; esac; }
expect_status() { [ "$LAST_STATUS" = "$1" ] || fail "$2 (want HTTP $1, got $LAST_STATUS): $LAST_BODY"; }
etag() { printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}'; }
mail_body() { # newest message body to $1 (retries)
  local i body
  for i in $(seq 1 30); do
    body="$(curl -sS -G "$MAILHOG/api/v2/search" --data-urlencode "kind=to" --data-urlencode "query=$1" | jq -r '.items[0].Content.Body // empty')"
    [ -n "$body" ] && { printf '%s' "$body"; return 0; }
    sleep 1
  done
  return 1
}
mail_code() { mail_body "$1" | sed 's/<[^>]*>/ /g' | grep -oE '(^|[^0-9])[0-9]{6}([^0-9]|$)' | head -1 | grep -oE '[0-9]{6}'; }

step "0. preflight"
curl -fsS "${BASE%/api/v1}/healthz" >/dev/null || fail "API not healthy at ${BASE%/api/v1}/healthz"
curl -fsS "$MAILHOG/api/v2/messages?limit=1" >/dev/null || fail "mailhog not reachable at $MAILHOG"
echo "individual=$EMAIL candidate=$CANDIDATE"

step "1. signup, verify, login"
call "$JAR" "" POST /auth/signup "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"; expect signup
SESSION_ID="$(jq -r '.data.verification_session_id' <<<"$LAST_BODY")"
CODE="$(mail_code "$EMAIL")" || fail "no verification email"
call "$JAR" "" POST /auth/verify-email "$(jq -nc --arg s "$SESSION_ID" --arg c "$CODE" '{verification_session_id:$s,code:$c}')"; expect verify-email
call "$JAR" "" POST /auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"; expect login
CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"

step "2. before onboarding there is nothing to hire with"
api GET /hiring/me; expect_status 400 "hiring/me before the profile step (onboarding_step_incomplete)"

step "3. individual onboarding: profile, compliance with the industry only, complete"
api PUT /onboarding/profile "$(jq -nc --arg f "$FIRST" --arg l "$LAST" --arg c "$UK_COUNTRY" '{account_type:"individual",first_name:$f,last_name:$l,country_id:$c}')"; expect profile
api PUT /onboarding/compliance "$(jq -nc --arg i "$INDUSTRY" '{industry_id:$i}')"; expect "compliance with the industry only"
[ "$(jq -r '.data.business_type_id // "none"' <<<"$LAST_BODY")" = none ] || fail "business_type_id must be absent for an individual"
api PUT /onboarding/compliance '{}' ; expect_status 400 "compliance without the industry"
api POST /onboarding/complete; expect complete
[ "$(jq -r '.data.organization.kind' <<<"$LAST_BODY")" = individual ] || fail "complete must return the personal organization"
[ "$(jq -r '.data.organization.verification_status' <<<"$LAST_BODY")" = pending ] || fail "the personal organization must start pending"
ORG_ID="$(jq -r '.data.organization.id' <<<"$LAST_BODY")"
api POST /onboarding/complete; expect_status 409 "completing twice"
ok "personal organization $ORG_ID created, review pending"

step "4. the personal organization works through the hiring paths"
api GET /hiring/me; expect "hiring/me"
[ "$(jq -r '.data.company_name' <<<"$LAST_BODY")" = "$FIRST $LAST" ] || fail "company_name is not the person's name"
jq -e '.data.roles | index("founder")' <<<"$LAST_BODY" >/dev/null || fail "owner must have the founder role"
api GET /organization/verification; expect "verification status"
[ "$(jq -r '.data.kind' <<<"$LAST_BODY")" = individual ] && [ "$(jq -r '.data.status' <<<"$LAST_BODY")" = pending ] || fail "status must be an individual pending case"
[ "$(jq -r '.data.can_publish' <<<"$LAST_BODY")" = false ] || fail "can_publish must be false before approval"
for p in retry confirm-name confirm-address change-country; do
  api POST "/organization/verification/$p" '{}'; expect_status 409 "individual $p"
done
api POST /organization/verification '{}'; expect_status 409 "re-requesting while in review"
api POST /jobs '{}'; expect "create a draft before verification (drafting needs nothing)"
JOB="$(jq -r '.data.id' <<<"$LAST_BODY")"
api GET "/jobs/$JOB/publish-check"; expect "publish-check"
jq -e '[.data.blocking[]?.code] | index("company_not_verified")' <<<"$LAST_BODY" >/dev/null || fail "publish must be blocked until verified: $LAST_BODY"
ok "drafting works, publishing is blocked by company_not_verified"

step "5. admin: the case is labelled individual; reject it; the person asks again; approve"
e2e_admin_login "$ADMIN_JAR" "$BASE" "$ADMIN_EMAIL" "$ADMIN_PASSWORD" || fail "admin login"
ADMIN_CSRF="$E2E_ADMIN_CSRF"
adm GET "/admin/verification?limit=100"; expect "queue"
[ "$(jq -r --arg o "$ORG_ID" '[.data[] | select(.organization_id == $o)][0].case_type' <<<"$LAST_BODY")" = individual ] || fail "queue item must carry case_type individual"
adm GET "/admin/verification/$ORG_ID"; expect "case detail"
[ "$(jq -r '.data.case_type' <<<"$LAST_BODY")" = individual ] || fail "detail must carry case_type individual"
adm POST "/admin/verification/$ORG_ID/review" '{"decision":"reject","reason":"name_mismatch"}'; expect "reject"
api GET /organization/verification; expect "status after rejection"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = failed ] && [ "$(jq -r '.data.can_retry' <<<"$LAST_BODY")" = false ] || fail "rejected individual: status failed, no company retry"
api POST /organization/verification '{}'; expect "request a new review"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = pending ] || fail "a new review must be pending"
adm GET "/admin/verification?limit=100"; expect "queue again"
[ "$(jq -r --arg o "$ORG_ID" '[.data[] | select(.organization_id == $o)] | length' <<<"$LAST_BODY")" = 1 ] || fail "the new review must be in the queue exactly once"
adm POST "/admin/verification/$ORG_ID/review" '{"decision":"approve"}'; expect "approve"
api GET /organization/verification; expect "status"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = verified ] && [ "$(jq -r '.data.can_publish' <<<"$LAST_BODY")" = true ] || fail "individual must be verified after approval"
api POST /organization/verification '{}'; expect_status 409 "re-requesting once verified"
ok "reject -> new review -> approve"

step "6. DPA, privacy contact, job, publish"
api GET /organization/agreements; expect agreements
DPA_VERSION="$(jq -r '.data.dpa_version' <<<"$LAST_BODY")"
api POST /organization/agreements "$(jq -nc --arg v "$DPA_VERSION" '{version:$v}')"; expect "accept dpa"
api PUT /organization/privacy-contact "$(jq -nc --arg e "$EMAIL" '{privacy_contact_email:$e}')"; expect "privacy contact"
api GET /hiring/catalog; expect catalog
MARKET="$(jq -r '[.data.markets[] | select(.is_open == true)][0].code' <<<"$LAST_BODY")"
CURRENCY="$(jq -r '.data.currencies[0]' <<<"$LAST_BODY")"
api GET "/jobs/$JOB"; expect "get job"; REV="$(etag)"
api POST /hiring/departments '{"name":"Household"}'; expect "department"
DEPT="$(jq -r '.data.id' <<<"$LAST_BODY")"
DETAILS="$(jq -nc --arg m "$MARKET" --arg cur "$CURRENCY" --arg d "$DEPT" '{
  department_id:$d, title:"Nanny", location_mode:"specific_area", location_text:"Remote", use_company_address:false,
  workplace_type:"remote", travel_frequency:"none", visa_sponsorship:"no", employment_type:"full_time",
  pay:{type:"range",currency:$cur,period:"year",min:5000000,max:8000000,visible:true},
  skills:[{custom_label:"Childcare"}], markets:[{market_code:$m}], lawful_basis:"contract",
  show_on_career_page:true,
  description_sections:{role:{type:"doc",content:[{type:"paragraph",content:[{type:"text",text:"Look after two children."}]}]}}}')"
api PUT "/jobs/$JOB/details" "$DETAILS" -H "If-Match: \"$REV\""; expect "put details"
api GET "/jobs/$JOB/application-form"; expect "get form"; REV="$(etag)"
FORM="$(jq -c '{questions: [.data.questions[] | select(.type != "file_upload" and .system_key != "cv" and .type != "autofill_resume")]}' <<<"$LAST_BODY")"
api PUT "/jobs/$JOB/application-form" "$FORM" -H "If-Match: \"$REV\""; expect "put form"
api GET "/jobs/$JOB/publish-check"; expect "publish-check"
for gate in $(jq -r '.data.gates[]? | select(.confirmed != true and (.requires_confirmation // .manual // false) == true) | .gate_id // .id' <<<"$LAST_BODY"); do
  api PUT "/jobs/$JOB/compliance/$gate" '{"confirmed":true}'; expect "confirm gate $gate"
done
api GET "/jobs/$JOB"; expect "get job"; REV="$(etag)"
api POST "/jobs/$JOB/publish" "" -H "If-Match: \"$REV\""; expect "publish"
PUBLIC_ID="$(jq -r '.data.job.public_id // .data.public_id // empty' <<<"$LAST_BODY")"
[ -n "$PUBLIC_ID" ] || { api GET "/jobs/$JOB"; PUBLIC_ID="$(jq -r '.data.public_id' <<<"$LAST_BODY")"; }
ok "published as $PUBLIC_ID"

step "7. a candidate applies; the individual sees the application"
for _ in $(seq 1 30); do
  [ "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' "$BASE/public/jobs/$PUBLIC_ID/applications")" != 404 ] && break
  sleep 1
done
anon GET "/public/jobs/$PUBLIC_ID"; expect "public job"
Q_NAME="$(jq -r '[.data.job.questions[]? | select(.system_key == "full_name")][0].id // empty' <<<"$LAST_BODY")"
Q_MAIL="$(jq -r '[.data.job.questions[]? | select(.system_key == "email")][0].id // empty' <<<"$LAST_BODY")"
case "$MARKET" in UK) COUNTRY=GB ;; *) COUNTRY="$MARKET" ;; esac
APPLY="$(jq -nc --arg n "$Q_NAME" --arg m "$Q_MAIL" --arg e "$CANDIDATE" --arg c "$COUNTRY" \
  '{candidate_country:$c, answers:{($n):"Bo Candidate",($m):$e}, privacy_notice_acknowledged:true, consent:false, source:"linkedin"}')"
anon POST "/public/jobs/$PUBLIC_ID/applications" "$APPLY" -H "Idempotency-Key: ind-$RANDOM-$(date +%s)"; expect_status 201 "apply"
api GET "/jobs/$JOB/applications"; expect "list applications"
[ "$(jq -r '.data.items | length' <<<"$LAST_BODY")" = 1 ] || fail "the individual must see exactly one application"
[ "$(jq -r '.data.items[0].email' <<<"$LAST_BODY")" = "$CANDIDATE" ] || fail "the listed application is not the candidate's"
APP="$(jq -r '.data.items[0].id' <<<"$LAST_BODY")"
api GET "/jobs/$JOB/applications/$APP"; expect "open application"
ok "the individual sees the application $APP"

step "8. small fixes as seen through the API"
adm GET /admin/permissions; expect "permissions"
jq -e '[.data[]?.slug // .data[]?.name // .data[]?] | map(tostring) | map(select(test("waitlist"))) | length == 0' <<<"$LAST_BODY" >/dev/null || fail "waitlist permissions must be gone: $LAST_BODY"
ok "no waitlist permissions in GET /admin/permissions"
if [ "${ADMIN_AUDIT_IP_CHECK:-on}" != off ]; then
  adm GET "/admin/audit-logs?limit=100"; expect "audit logs"
  BAD="$(jq -r '[.data[]? | .ip // empty | select(test("^[0-9.]+:[0-9]+$") or test("^\\[.*\\]:[0-9]+$"))] | length' <<<"$LAST_BODY")"
  [ "$BAD" = 0 ] || fail "$BAD audit rows still carry ip:port"
  [ "$(jq -r '[.data[]? | .ip // empty] | length' <<<"$LAST_BODY")" -gt 0 ] || fail "no audit row with an ip to check"
  ok "audit IPs are bare"
fi
if [ -n "${APP_URL:-}" ]; then
  call "$ANON_JAR" "" POST /auth/forgot-password "$(jq -nc --arg e "$EMAIL" '{email:$e}')"
  LINK=""
  for _ in $(seq 1 30); do
    LINK="$(curl -sS -G "$MAILHOG/api/v2/search" --data-urlencode "kind=to" --data-urlencode "query=$EMAIL" \
      | jq -r '.items[].Content.Body' | grep -oE 'https?://[^"<> ]*reset-password\?token=[^"<> ]*' | head -1)" || true
    [ -n "$LINK" ] && break
    sleep 1
  done
  case "$LINK" in "$APP_URL"/reset-password?token=*) ok "reset link points at APP_URL ($APP_URL)" ;; *) fail "reset link is '$LINK', want $APP_URL/reset-password?token=..." ;; esac
fi

step "9. superadmin deletes the personal organization"
adm DELETE "/admin/companies/$ORG_ID?region=uk" '{"reason":"e2e-individual"}'; expect "delete company"
for _ in $(seq 1 10); do api GET /hiring/me; [ "$LAST_STATUS" = 404 ] && break; sleep 1; done
expect_status 404 "hiring/me after deletion"
ok "deleted: /hiring/me answers 404"

printf '\nSUCCESS: individual account hired end to end (org %s, job %s)\n' "$ORG_ID" "$JOB"
