#!/usr/bin/env bash
# End-to-end checks for the interim data couplings that were removed (needs the all-remote stack of
# docs/microservices/local-dev.md with ORG_TENANTS/ORG_NOTICE/ORG_COMPANY/IDENTITY_USERS=grpc,
# APPLICATIONS_ORG_EVENTS=on, HIRING_ORG_EVENTS=on, the clamav profile and a company made by
# `STATE_FILE=state.env UPLOADS=on scripts/e2e-apply.sh`).
#
#   1  a second member is invited and accepts, is added to the job through PUT /jobs/{id}/members, and the
#      applications service honours it (company-side list) from the members event alone; the fallback counters of
#      the read model (GET $APPS_HTTP/internal/jobmodel-stats) show no use of the hiring tables for JobAccess,
#      TenantIDs, LookupJob or the notice recipient
#   2  an Anywhere job accepts candidates from any valid country (GB, KE, BR), refuses an invalid code; a
#      specific-area job still refuses a country outside its markets
#   3  settings page, DPA and privacy contact through org (ORG_COMPANY=grpc); manager names through identity
#   4  superadmin DELETE /admin/companies/{id}: applications rows and MinIO objects disappear (applications consumer),
#      hiring archives the jobs (hiring-org), repeating the delete is harmless
#
# Env: STATE_FILE (required), BASE, BASE_APPS, MAILHOG, ADMIN_EMAIL, ADMIN_PASSWORD, PG_DOCKER (postgres container,
# read-only checks), APPS_HTTP (applications service HTTP, default http://localhost:38104), MINIO (default
# http://localhost:39000), REGION (uk).
set -euo pipefail

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-common.sh"
e2e_url BASE WEB_BFF_PORT 8081 /api/v1
BASE_APPS="${BASE_APPS:-$BASE}"
e2e_url MAILHOG MAILHOG_UI_PORT 8025
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@anglehr.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-changeme123}"
[ -n "${PG_PORT:-}" ] || e2e_port PG_PORT POSTGRES_PORT 5432
[ -n "${SMTP_PORT:-}" ] || e2e_port SMTP_PORT MAILHOG_SMTP_PORT 1025
KYB_COUNTRY="${KYB_COUNTRY:-DE}"
UPLOADS="${UPLOADS:-off}"
APPS_LOG_CMD="${APPS_LOG_CMD:-docker compose -p ${COMPOSE_PROJECT_NAME:-openhr-local} logs --no-color applications}"
CANDIDATE2="candidate2-$(date +%s)-$RANDOM@example.com"
EMAIL="${EMAIL:-e2e-apply-$(date +%s)-$RANDOM@example.com}"
CANDIDATE="candidate-$(date +%s)-$RANDOM@example.com"
PASS="${PASS:-test-password-123}"
COMPANY_ROLE="60000000-0000-4000-8000-000000000001"
BUSINESS_TYPE="61000000-0000-4000-8000-000000000001"
INDUSTRY="62000000-0000-4000-8000-000000000001"
UK_COUNTRY="a1b2c3d4-e5f6-4789-a012-3456789abcde"

command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
JAR="$(mktemp)"; ADMIN_JAR="$(mktemp)"; ANON_JAR="$(mktemp)"
MEMBER_JAR="$(mktemp)"; MEMBER_CSRF=""
trap 'rm -f "$JAR" "$ADMIN_JAR" "$ANON_JAR" "$MEMBER_JAR"' EXIT
CSRF=""; ADMIN_CSRF=""
LAST_STATUS=""; LAST_BODY=""; LAST_HEADERS=""

step() { printf '\n=== %s ===\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
ok() { printf 'ok: %s\n' "$1"; }

# call JAR_VAR CSRF_VAR METHOD PATH [BODY] [extra curl args...] -> LAST_STATUS, LAST_BODY, LAST_HEADERS
call() {
  local jar="$1" csrf="$2" method="$3" path="$4" body="${5:-}"
  shift $(( $# < 5 ? $# : 5 ))
  local hdr; hdr="$(mktemp)"
  local base="$BASE"
  case "$path" in
    /public/jobs/*/applications|/public/jobs/*/uploads|/jobs/*/applications|/jobs/*/applications/*|/hiring/applicant-settings) base="$BASE_APPS" ;;
  esac
  local args=(-sS -X "$method" -c "$jar" -b "$jar" -D "$hdr" -w '\n%{http_code}' "$base$path")
  if [ -n "$csrf" ] && [ "$method" != GET ] && [ "$method" != HEAD ]; then args+=(-H "X-CSRF-Token: $csrf"); fi
  if [ -n "$body" ]; then args+=(-H 'Content-Type: application/json' -d "$body"); fi
  args+=("$@")
  local out; out="$(curl "${args[@]}")"
  LAST_STATUS="${out##*$'\n'}"; LAST_BODY="${out%$'\n'*}"; LAST_HEADERS="$(cat "$hdr")"; rm -f "$hdr"
  printf '%s %s -> %s%s\n' "$method" "$path" "$LAST_STATUS" "$([ "$base" != "$BASE" ] && printf ' (applications service)')"
  printf '%s' "$LAST_BODY" | jq -c . 2>/dev/null | cut -c1-260 || true
}
api() { # as the company user
  call "$JAR" "$CSRF" "$@"
  local fresh; fresh="$(printf '%s' "$LAST_BODY" | jq -r '.data.session.csrf_token? // .data.csrf_token? // empty' 2>/dev/null)" || fresh=""
  [ -n "$fresh" ] && CSRF="$fresh"
  return 0
}
adm() { call "$ADMIN_JAR" "$ADMIN_CSRF" "$@"; }
anon() { call "$ANON_JAR" "" "$@"; }
expect() { case "$LAST_STATUS" in 2??) ;; *) fail "$1 (HTTP $LAST_STATUS): $LAST_BODY" ;; esac; }
expect_status() { [ "$LAST_STATUS" = "$1" ] || fail "$2 (want HTTP $1, got $LAST_STATUS): $LAST_BODY"; }

mail_code() {
  local to="$1" i body code
  for i in $(seq 1 30); do
    body="$(curl -sS -G "$MAILHOG/api/v2/search" --data-urlencode "kind=to" --data-urlencode "query=$to" | jq -r '.items[0].Content.Body // empty')"
    if [ -n "$body" ]; then
      code="$(printf '%s' "$body" | sed 's/<[^>]*>/ /g' | grep -oE '(^|[^0-9])[0-9]{6}([^0-9]|$)' | head -1 | grep -oE '[0-9]{6}')" || true
      [ -n "$code" ] && { printf '%s' "$code"; return 0; }
    fi
    sleep 1
  done
  return 1
}
mail_count() { # messages to $1 whose subject matches the regex $2
  curl -sS -G "$MAILHOG/api/v2/search" --data-urlencode "kind=to" --data-urlencode "query=$1" \
    | jq --arg re "$2" '[.items[] | select((.Content.Headers.Subject // [""] | join(" ")) | test($re; "i"))] | length'
}


STATE_FILE="${STATE_FILE:?set STATE_FILE to the file written by e2e-apply.sh}"
# shellcheck disable=SC1090
. "$STATE_FILE"
e2e_url APPS_HTTP APPLICATIONS_PORT 8104
e2e_url MINIO MINIO_API_PORT 9000
REGION="${REGION:-uk}"
PG_DOCKER="${PG_DOCKER:-${COMPOSE_PROJECT_NAME:-openhr-local}-postgres-1}"
psql_uk() { docker exec -i "$PG_DOCKER" psql -qAt -U openhr -d "openhr_$REGION" -c "$1"; }
minio_count() { # objects under the prefix in the regional bucket
  curl -sS --aws-sigv4 "aws:amz:us-east-1:s3" --user "minioadmin:minioadmin" -G "$MINIO/openhr-$REGION" \
    --data-urlencode "list-type=2" --data-urlencode "prefix=$1" | { grep -o '<Key>' || true; } | wc -l | tr -d ' '
}
stats() { curl -fsS "$APPS_HTTP/internal/jobmodel-stats"; }
wait_for() { # wait_for SECONDS DESCRIPTION cmd...
  local n="$1" d="$2" i; shift 2
  for i in $(seq 1 "$n"); do "$@" && return 0; sleep 1; done
  fail "timed out: $d"
}

step "0. login as the company from the apply run"
call "$JAR" "" POST /auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"; expect login
CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"
BASE_STATS="$(stats)"; echo "fallback counters at start: $BASE_STATS"

step "1. second member: invite, accept, add to the job, applications honours it"
MEMBER="member-$(date +%s)-$RANDOM@example.com"
api POST /organizations/invites "$(jq -nc --arg e "$MEMBER" '{email:$e}')"; expect "invite"
TOKEN=""
for i in $(seq 1 30); do
  BODY="$(curl -sS -G "$MAILHOG/api/v2/search" --data-urlencode kind=to --data-urlencode "query=$MEMBER" | jq -r '.items[0].Content.Body // empty')"
  TOKEN="$(printf '%s' "$BODY" | grep -oE 'invite\?token=[A-Za-z0-9_-]+' | head -1 | cut -d= -f2)" || true
  [ -n "$TOKEN" ] && break; sleep 1
done
[ -n "$TOKEN" ] || fail "no invite token in the invite mail for $MEMBER"
call "$MEMBER_JAR" "" POST /auth/accept-invite "$(jq -nc --arg t "$TOKEN" --arg p "$PASS" '{token:$t,password:$p,first_name:"Mia",last_name:"Member"}')"; expect "accept invite"
call "$MEMBER_JAR" "" POST /auth/login "$(jq -nc --arg e "$MEMBER" --arg p "$PASS" '{email:$e,password:$p}')"; expect "member login"
MEMBER_CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"
call "$MEMBER_JAR" "$MEMBER_CSRF" GET /auth/me; expect "member me"
MEMBER_ID="$(jq -r '.data.user.id // .data.id' <<<"$LAST_BODY")"
[ -n "$MEMBER_ID" ] && [ "$MEMBER_ID" != null ] || fail "no member id in /auth/me"
# TEST-ONLY SQL SHORTCUT (as in e2e-apply.sh for the legal role): company roles cannot be assigned through the HTTP API
# yet (OrgService.SetMemberRoles is gRPC only). The invitee gets line_manager (job.view.assigned): it sees a job only
# when it is the creator or on the job's team, which is exactly what the members event has to carry to applications.
psql_uk "INSERT INTO accounts.organization_member_roles (member_id, role)
  SELECT m.id, 'line_manager' FROM accounts.organization_members m WHERE m.user_id = '$MEMBER_ID' ON CONFLICT DO NOTHING" >/dev/null \
  || fail "could not grant the line_manager role (test-only shortcut)"
call "$MEMBER_JAR" "$MEMBER_CSRF" GET "/jobs/$JOB/applications"
BEFORE="$LAST_STATUS"
[ "$BEFORE" != 200 ] || fail "the new member can list the job's applications before being on its team"
ok "before: member is refused (HTTP $BEFORE)"
api GET "/jobs/$JOB/members"; expect "members"
REV="$(printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')"
api GET "/jobs/$JOB"; REV="$(printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')"
api PUT "/jobs/$JOB/members" "$(jq -nc --arg u "$MEMBER_ID" '{members:[{user_id:$u,role:"recruiter"}]}')" -H "If-Match: \"$REV\""; expect "add member"
member_lists() { call "$MEMBER_JAR" "$MEMBER_CSRF" GET "/jobs/$JOB/applications" >/dev/null; [ "$LAST_STATUS" = 200 ]; }
wait_for 30 "member cannot list the applications after being added" member_lists
ok "after: the applications service lets the member in (access from hiring.job.members_changed.v1)"
api PUT "/jobs/$JOB/members" '{"members":[]}' -H "If-Match: \"$(api GET "/jobs/$JOB" >/dev/null; printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')\""; expect "remove member"
member_refused() { call "$MEMBER_JAR" "$MEMBER_CSRF" GET "/jobs/$JOB/applications" >/dev/null; [ "$LAST_STATUS" != 200 ]; }
wait_for 30 "member still lists after removal" member_refused
ok "removal honoured too (HTTP $LAST_STATUS)"
api PUT "/jobs/$JOB/members" "$(jq -nc --arg u "$MEMBER_ID" '{members:[{user_id:$u,role:"hiring_manager"}]}')" -H "If-Match: \"$(api GET "/jobs/$JOB" >/dev/null; printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')\""; expect "add member as hiring manager"

step "2. no fallback to the hiring tables"
S="$(stats)"; echo "fallback counters now: $S"
[ "$(jq '[to_entries[] | select(.key | test("^(job_access|tenant_ids|lookup_job|notice_recipient):")) | .value] | add // 0' <<<"$S")" = 0 ] \
  || fail "the read model used the hiring tables for access, tenants, lookup or notice recipient: $S"
ok "job_access, tenant_ids, lookup_job and notice_recipient never touched the hiring tables"

step "3. Anywhere and specific-area jobs"
api POST /jobs '{}'; expect "create job"; JOB2="$(jq -r '.data.id' <<<"$LAST_BODY")"
api GET /hiring/catalog; expect catalog
CURRENCY="$(jq -r '.data.currencies[0]' <<<"$LAST_BODY")"
api GET "/jobs/$JOB2"; REV="$(printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')"
api GET /hiring/departments >/dev/null; DEPT="$(jq -r '.data[0].id' <<<"$LAST_BODY")"
DETAILS="$(jq -nc --arg cur "$CURRENCY" --arg d "$DEPT" '{
  department_id:$d, title:"Anywhere Engineer", location_mode:"anywhere", use_company_address:false,
  workplace_type:"remote", travel_frequency:"none", visa_sponsorship:"no", employment_type:"full_time",
  pay:{type:"range",currency:$cur,period:"year",min:5000000,max:8000000,visible:true},
  skills:[{custom_label:"Go"}], markets:[], lawful_basis:"contract", show_on_career_page:true,
  description_sections:{role:{type:"doc",content:[{type:"paragraph",content:[{type:"text",text:"Work from anywhere."}]}]}}}')"
api PUT "/jobs/$JOB2/details" "$DETAILS" -H "If-Match: \"$REV\""; expect "put details (anywhere)"
api GET "/jobs/$JOB2/application-form"; expect "get form"
REV="$(printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')"
FORM="$(jq -c '{questions: [.data.questions[] | select(.type != "file_upload" and .system_key != "cv" and .type != "autofill_resume")]}' <<<"$LAST_BODY")"
api PUT "/jobs/$JOB2/application-form" "$FORM" -H "If-Match: \"$REV\""; expect "put form"
api GET "/jobs/$JOB2/publish-check"; expect "publish-check"
for gate in $(jq -r '[.data.blocking[]? | select(.code == "gate_unconfirmed") | .gate_id] | .[]' <<<"$LAST_BODY"); do
  api GET "/jobs/$JOB2" >/dev/null; REV="$(printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')"
  api PUT "/jobs/$JOB2/compliance/$gate" '{"confirmed":true}' -H "If-Match: \"$REV\""; expect "confirm gate $gate"
done
api GET "/jobs/$JOB2"; REV="$(printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')"
api POST "/jobs/$JOB2/publish" "" -H "If-Match: \"$REV\""; expect "publish anywhere job"
PUB2="$(jq -r '.data.job.public_id // .data.public_id // empty' <<<"$LAST_BODY")"
[ -n "$PUB2" ] || fail "no public id for the anywhere job: $LAST_BODY"
anon GET "/public/jobs/$PUB2"; expect "public anywhere job"
QN2="$(jq -r '[.data.job.questions[]? | select(.system_key == "full_name")][0].id' <<<"$LAST_BODY")"
QM2="$(jq -r '[.data.job.questions[]? | select(.system_key == "email")][0].id' <<<"$LAST_BODY")"
apply_from() { # apply_from PUBLIC_ID QN QM COUNTRY -> LAST_STATUS
  anon POST "/public/jobs/$1/applications" "$(jq -nc --arg n "$2" --arg m "$3" --arg e "c-$4-$RANDOM-$(date +%s)@example.com" --arg c "$4" \
    '{candidate_country:$c, answers:{($n):"Ada Candidate",($m):$e}, privacy_notice_acknowledged:true, consent:false, source:"linkedin"}')"
}
# The applications read model is fed by events (APPLICATIONS_JOBFALLBACK=off): an empty body is a validation error once
# it knows the job, 404 before.
wait_job() { anon GET "/public/jobs/$PUB2" >/dev/null; [ "$LAST_STATUS" = 200 ] || return 1
  [ "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' "$BASE_APPS/public/jobs/$PUB2/applications")" != 404 ]; }
wait_for 20 "anywhere job not visible" wait_job
for c in GB KE BR; do
  apply_from "$PUB2" "$QN2" "$QM2" "$c"; expect_status 201 "anywhere job, candidate from $c"
  ok "anywhere job accepts a candidate from $c"
done
apply_from "$PUB2" "$QN2" "$QM2" ZZ
case "$LAST_STATUS" in 4??) ok "an invalid country code is refused (HTTP $LAST_STATUS)" ;; *) fail "ZZ accepted on an anywhere job" ;; esac
# Specific-area job from the apply run: open in $MARKET only.
case "$MARKET" in NG) OUT=US ;; US) OUT=NG ;; *) OUT=US ;; esac
case "$MARKET" in US) OUT=NG ;; esac
apply_from "$PUBLIC_ID" "$Q_NAME" "$Q_MAIL" "$OUT"
case "$LAST_STATUS" in 4??) ok "the specific-area job (market $MARKET) refuses a candidate from $OUT (HTTP $LAST_STATUS)" ;; *) fail "specific-area job accepted $OUT (HTTP $LAST_STATUS)" ;; esac
api GET "/jobs/$JOB2/applications"; expect "list anywhere applications"
[ "$(jq -r '.data.items | length' <<<"$LAST_BODY")" = 3 ] || fail "expected 3 applications on the anywhere job: $LAST_BODY"
ok "the company lists the 3 applications from GB, KE and BR (market codes: $(jq -c '[.data.items[].market]' <<<"$LAST_BODY"))"

step "4. company data through org and identity (ORG_COMPANY, IDENTITY_USERS)"
api GET /organization/agreements; expect agreements
api PUT /organization/privacy-contact '{"privacy_contact_email":"privacy2@example.com","dpo_contact":"DPO Two"}'; expect "privacy contact"
api GET /hiring/settings; expect "settings"
anon GET "/public/jobs/$PUB2"; expect "public job"
[ "$(jq -r '.data.company.privacy_email // .data.job.company.privacy_email // empty' <<<"$LAST_BODY")" != "" ] || echo "NOTE: public JSON has no privacy email field to compare"
api GET /jobs; expect "job list"
[ "$(jq -r --arg j "$JOB" '.data[] | select(.id == $j) | .managers[0].name' <<<"$LAST_BODY")" = "Mia Member" ] \
  || fail "manager name from identity missing on the job list: $(jq -c '[.data[] | {id,managers}]' <<<"$LAST_BODY")"
ok "manager name resolved through IdentityService.BatchGetUsers"

step "5. delete the company (superadmin), watch the chain"
FILES_BEFORE="$(( $(minio_count "f/$ORG_ID/") + $(minio_count "u/$ORG_ID/") ))"
APPS_BEFORE="$(psql_uk "SELECT count(*) FROM hiring.applications WHERE tenant_id = '$ORG_ID'")"
LIVE_BEFORE="$(psql_uk "SELECT count(*) FROM hiring.job_postings WHERE tenant_id = '$ORG_ID' AND status IN ('published','paused')")"
echo "before: applications=$APPS_BEFORE minio objects=$FILES_BEFORE live jobs=$LIVE_BEFORE"
[ "$APPS_BEFORE" -ge 1 ] || fail "no applications to delete"
[ "$FILES_BEFORE" -ge 1 ] || echo "NOTE: no uploaded files (run e2e-apply with UPLOADS=on to assert object deletion)"
e2e_admin_login "$ADMIN_JAR" "$BASE" "$ADMIN_EMAIL" "$ADMIN_PASSWORD" || fail "admin login"
ADMIN_CSRF="$E2E_ADMIN_CSRF"
adm DELETE "/admin/companies/not-a-uuid?region=$REGION"; expect_status 400 "bad id"
adm DELETE "/admin/companies/$ORG_ID"; expect_status 400 "missing region"
adm DELETE "/admin/companies/$ORG_ID?region=$REGION" '{"reason":"e2e couplings"}'; expect "delete company"
[ "$(jq -r '.data.already_deleted' <<<"$LAST_BODY")" = false ] || fail "first delete reported already_deleted"
adm DELETE "/admin/companies/$ORG_ID?region=$REGION"; expect "delete again"
[ "$(jq -r '.data.already_deleted' <<<"$LAST_BODY")" = true ] || fail "second delete must report already_deleted"
ok "deleted; the repeat is harmless"
gone() { [ "$(psql_uk "SELECT count(*) FROM hiring.applications WHERE tenant_id = '$ORG_ID'")" = 0 ]; }
wait_for 60 "applications still stored" gone
ok "applications consumer removed the applications"
objs() { [ "$(( $(minio_count "f/$ORG_ID/") + $(minio_count "u/$ORG_ID/") ))" = 0 ]; }
wait_for 30 "objects still in MinIO" objs
ok "MinIO: $FILES_BEFORE object(s) before, 0 after"
[ "$(psql_uk "SELECT count(*) FROM hiring.app_jobs WHERE company_id = '$ORG_ID'")" = 0 ] || fail "job read model rows remain"
archived() { [ "$(psql_uk "SELECT count(*) FROM hiring.job_postings WHERE tenant_id = '$ORG_ID' AND status IN ('published','paused')")" = 0 ]; }
wait_for 60 "hiring still has live jobs of the company" archived
ok "hiring-org archived the company's live jobs: $(psql_uk "SELECT string_agg(status || ':' || count, ' ') FROM (SELECT status, count(*) FROM hiring.job_postings WHERE tenant_id = '$ORG_ID' GROUP BY status) t")"
sleep 3
[ "$(psql_uk "SELECT count(*) FROM hiring.app_jobs WHERE company_id = '$ORG_ID'")" = 0 ] || fail "the closing events restored job read model rows"
anon GET "/public/jobs/$PUB2"
[ "$LAST_STATUS" != 200 ] || [ "$(jq -r '.data.accepting' <<<"$LAST_BODY")" = false ] || fail "the deleted company's job still accepts applications"
ok "the public job no longer accepts applications (HTTP $LAST_STATUS)"
call "$JAR" "$CSRF" GET /jobs >/dev/null
[ "$LAST_STATUS" != 200 ] || fail "the deleted company's owner can still list jobs"
ok "the owner no longer resolves to a company (HTTP $LAST_STATUS)"
S="$(stats)"; echo "fallback counters at the end: $S"

printf '\nSUCCESS: couplings e2e completed (company %s, anywhere job %s)\n' "$ORG_ID" "$PUB2"
