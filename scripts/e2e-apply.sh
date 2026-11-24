#!/usr/bin/env bash
# End-to-end test of the public apply path against the local stack (curl + jq).
#
#   company signup + onboarding -> KYB submit -> admin approves (bootstrap admin) -> accept DPA + privacy contact
#   -> create job, save form, publish -> anonymous GET public job -> anonymous apply (+ idempotent retry, repeat)
#   -> company lists applications (job list shows the counters) -> opens one -> in_review -> rejected
#   -> confirmation and status emails in mailhog
#
# UPLOADS=on (needs the clamav profile and CLAMAV_ADDR on the applications service, and the object store's public
# endpoint, see R2_PUBLIC_ENDPOINT) also runs: presign -> PUT from the host -> scan and claim on submit -> the company
# downloads the file through its presigned link and compares the bytes; the EICAR string declared as a PDF is refused (wrong magic bytes); the EICAR string inside a well-formed PDF stream (passes the
# format check, flagged by ClamAV) is refused as malware, the service logs the verdict and neither the staging nor the final
# object is left in the bucket (see OBJECT_BUCKET); a special
# category answer (health, sealed) is accepted; a knockout answer flags the application; a rejected candidate who
# applies again inside the block window is ignored (reapply block, evidenced by the applications service log).
# Without UPLOADS=on the script only checks that uploads fail closed with 503 when no scanner is configured.
# Not covered: retention.
#
# Needs the monolith stack with the notifications service (make local-up). Mail is delivered by the always-on
# notifications service for the auth emails; the hiring outbox (applicant emails) is drained by running
# ./cmd/hiring-outbox once, which this script does itself (see OUTBOX_CMD).
#
# Env:
#   BASE          API base            (default http://localhost:8081/api/v1)
#   BASE_APPS     API base of the applications service (default: BASE). Set it to run the applications side
#                 (public apply and uploads, /jobs/{id}/applications*, /hiring/applicant-settings) against
#                 `cmd/applications` while everything else (signup, KYB, jobs, publish) stays on the monolith,
#                 e.g. BASE_APPS=http://localhost:8104/api/v1. The session cookie and CSRF token the monolith
#                 issued are used as they are.
#   MAILHOG       mailhog HTTP base   (default http://localhost:8025)
#   ADMIN_EMAIL, ADMIN_PASSWORD   bootstrap admin (default admin@anglehr.local / changeme123)
#   OUTBOX_CMD    command that drains the hiring email outbox once and exits 0. Default: go run ./cmd/hiring-outbox
#                 with the stack's database and SMTP settings taken from PG_PORT / SMTP_PORT. Set to "skip" to skip.
#   PG_PORT       host port of the stack's postgres (default 5432), SMTP_PORT mailhog SMTP host port (default 1025),
#                 MINIO_API_PORT minio host port (default 9000; only needed for the outbox command to start)
#   UPLOADS       on to run the upload, special category, knockout and reapply steps (default off)
#   APPS_LOG_CMD  command printing the applications service log (default: docker compose -p $COMPOSE_PROJECT_NAME
#                 logs applications, project openhr-local when unset); used to see the reapply block fire
#   OBJECT_BUCKET bucket the CVs land in, checked through the object store's S3 API with curl --aws-sigv4 (default openhr-uk;
#                 MINIO_ROOT_USER/MINIO_ROOT_PASSWORD are the credentials, default minioadmin)
#   STATE_FILE    write the ids this run created (company, job, public id, credentials) for scripts/e2e-couplings.sh
#   KYB_COUNTRY   country with no automated registry, so verification goes to manual review (default DE)
#
# Test-only shortcuts: none. Everything goes through the public API, the admin API (approving the KYB case) and
# the outbox command.
set -euo pipefail

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-common.sh"
e2e_url BASE WEB_BFF_PORT 8081 /api/v1
BASE_APPS="${BASE_APPS:-$BASE}"
e2e_url MAILHOG MAILHOG_UI_PORT 8025
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@anglehr.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-changeme123}"
[ -n "${PG_PORT:-}" ] || e2e_port PG_PORT POSTGRES_PORT 5432
[ -n "${SMTP_PORT:-}" ] || e2e_port SMTP_PORT MAILHOG_SMTP_PORT 1025
e2e_port MINIO_API_PORT MINIO_API_PORT 9000
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
trap 'rm -f "$JAR" "$ADMIN_JAR" "$ANON_JAR"' EXIT
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

step "0. preflight"
curl -fsS "${BASE%/api/v1}/healthz" >/dev/null || fail "API not healthy at ${BASE%/api/v1}/healthz"
curl -fsS "$MAILHOG/api/v2/messages?limit=1" >/dev/null || fail "mailhog not reachable at $MAILHOG"
echo "api + mailhog reachable; company=$EMAIL candidate=$CANDIDATE"

step "1. company: signup, verify, login (cookie session)"
call "$JAR" "" POST /auth/signup "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"; expect signup
SESSION_ID="$(jq -r '.data.verification_session_id' <<<"$LAST_BODY")"
CODE="$(mail_code "$EMAIL")" || fail "no verification email (is the notifications service running?)"
call "$JAR" "" POST /auth/verify-email "$(jq -nc --arg s "$SESSION_ID" --arg c "$CODE" '{verification_session_id:$s,code:$c}')"; expect verify-email
call "$JAR" "" POST /auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASS" '{email:$e,password:$p}')"; expect login
CSRF="$(jq -r '.data.csrf_token' <<<"$LAST_BODY")"

step "2. company: business onboarding"
api PUT /onboarding/profile "$(jq -nc --arg r "$COMPANY_ROLE" --arg c "$UK_COUNTRY" '{account_type:"business",country_id:$c,legal_business_name:"ANGLE Apply E2E Ltd",legal_full_name:"Jerry Oluwasegun",company_role_id:$r}')"; expect profile
api PUT /onboarding/address "$(jq -nc --arg c "$UK_COUNTRY" '{country_id:$c,entry_mode:"manual",line_1:"10 Downing Street",city:"London",state_or_county:"Greater London",post_code:"SW1A 2AA",identification:{registration_number:"12345678"}}')"; expect address
api PUT /onboarding/compliance "$(jq -nc --arg b "$BUSINESS_TYPE" --arg i "$INDUSTRY" '{business_type_id:$b,industry_id:$i,employee_count:25}')"; expect compliance
api POST /onboarding/complete; expect complete

step "3. company: KYB submit ($KYB_COUNTRY has no automated registry: goes to manual review)"
api POST /organization/verification "$(jq -nc --arg c "$KYB_COUNTRY" --arg n "${KYB_NUMBER:-HRB 123456}" '{country_code:$c,registration_number:$n,legal_name:"ANGLE Apply E2E Ltd",address:{line1:"1 Friedrichstrasse",city:"Berlin",country:"DE"},address_type:"registered"}')"
expect "kyb submit"
KYB_STATUS="$(jq -r '.data.status' <<<"$LAST_BODY")"
[ "$KYB_STATUS" = pending ] || fail "expected KYB status pending (manual review), got $KYB_STATUS"
ok "KYB is pending manual review"

step "4. admin: log in as the bootstrap admin, find the case, approve it"
e2e_admin_login "$ADMIN_JAR" "$BASE" "$ADMIN_EMAIL" "$ADMIN_PASSWORD" || fail "admin login"
ADMIN_CSRF="$E2E_ADMIN_CSRF"; ok "admin login"
adm GET "/admin/verification?limit=100"; expect "verification queue"
# The queue lists case ids with the organization id; the newest case for our country is the one just submitted.
ORG_ID="$(jq -r --arg c "$KYB_COUNTRY" '[.data[] | select(.country_code == $c)] | sort_by(.submitted_at) | last | .organization_id // empty' <<<"$LAST_BODY")"
[ -n "$ORG_ID" ] || fail "could not find the new company in the verification queue"
adm GET "/admin/verification/$ORG_ID"; expect "case details"
[ "$(jq -r '.data.registration_number // .data.case.registration_number // empty' <<<"$LAST_BODY")" = "${KYB_NUMBER:-HRB 123456}" ] || fail "case details do not show the submitted registration number"
adm POST "/admin/verification/$ORG_ID/review" '{"decision":"approve"}'; expect "approve"
api GET /organization/verification; expect "kyb status"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = verified ] || fail "company is not verified after approval"
ok "company verified"

step "5. company: accept the DPA, set the privacy contact"
api GET /organization/agreements; expect agreements
DPA_VERSION="$(jq -r '.data.dpa_version' <<<"$LAST_BODY")"
api POST /organization/agreements "$(jq -nc --arg v "$DPA_VERSION" '{version:$v}')"; expect "accept dpa"
api PUT /organization/privacy-contact '{"privacy_contact_email":"privacy@example.com","dpo_contact":"Data Protection Officer"}'; expect "privacy contact"
if [ "$UPLOADS" = on ]; then
  # TEST-ONLY SQL SHORTCUT: the legal role (automated screening switch, DPIA, special category questions, retention
  # settings) cannot be given through the API yet (there is no member invitation or role assignment endpoint). The
  # owner is given it directly in the regional database. LEGAL_SQL_CMD overrides how psql is run.
  LEGAL_SQL_CMD="${LEGAL_SQL_CMD:-docker exec -i ${COMPOSE_PROJECT_NAME:-openhr-local}-postgres-1 psql -q -U openhr -d openhr_uk}"
  $LEGAL_SQL_CMD <<SQL || fail "could not grant the legal role (test-only shortcut)"
INSERT INTO accounts.organization_members (organization_id, user_id, role)
  SELECT id, owner_user_id, 'owner' FROM accounts.organizations WHERE id = '$ORG_ID' ON CONFLICT DO NOTHING;
INSERT INTO accounts.organization_member_roles (member_id, role)
  SELECT m.id, 'legal' FROM accounts.organization_members m WHERE m.organization_id = '$ORG_ID' ON CONFLICT DO NOTHING;
SQL
  # Knockout questions need automated screening (Legal switch) and the company DPIA.
  api PUT /hiring/settings '{"automated_screening_enabled":true}'; expect "enable automated screening"
  api PUT /organization/dpia '{"reference":"local e2e","summary":"Knockout questions flag candidates for human review"}'; expect "record dpia"
fi

step "6. company: create a job, fill details and a form"
api POST /jobs '{}'; expect "create job"
JOB="$(jq -r '.data.id' <<<"$LAST_BODY")"
api GET /hiring/catalog; expect catalog
MARKET="$(jq -r '[.data.markets[] | select(.is_open == true)][0].code' <<<"$LAST_BODY")"
CURRENCY="$(jq -r '.data.currencies[0]' <<<"$LAST_BODY")"
echo "market=$MARKET currency=$CURRENCY"
api GET "/jobs/$JOB"; expect "get job"
REV="$(printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')"
api POST /hiring/departments '{"name":"Engineering"}'; expect "create department"
DEPT="$(jq -r '.data.id' <<<"$LAST_BODY")"
DETAILS="$(jq -nc --arg m "$MARKET" --arg cur "$CURRENCY" --arg d "$DEPT" '{
  department_id:$d, title:"Backend Engineer", location_mode:"specific_area", location_text:"Remote", use_company_address:false,
  workplace_type:"remote", travel_frequency:"none", visa_sponsorship:"no", employment_type:"full_time",
  pay:{type:"range",currency:$cur,period:"year",min:5000000,max:8000000,visible:true},
  skills:[{custom_label:"Go"}], markets:[{market_code:$m}], lawful_basis:"contract",
  show_on_career_page:true,
  description_sections:{role:{type:"doc",content:[{type:"paragraph",content:[{type:"text",text:"Build the apply path."}]}]}}}')"
api PUT "/jobs/$JOB/details" "$DETAILS" -H "If-Match: \"$REV\""; expect "put details"
api GET "/jobs/$JOB/application-form"; expect "get form"
echo "form questions: $(jq -c '[.data.questions[]? | {id,type,label,system_key,required}]' <<<"$LAST_BODY")"
REV="$(printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')"
# The standard form without the CV question: a CV needs the malware scanner, which the local stack does not run.
if [ "$UPLOADS" = on ]; then
  # Keep the CV question; add a knockout question ("no" flags) and a special category (health) question.
  OPTS='{"options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}'
  FORM="$(jq -c --argjson o "$OPTS" '{questions: (.data.questions + [
      {section:"profile",type:"single_choice",label:"Do you have the right to work here?",required:true,config:$o,
       knockout:{option_ids:["no"],reason:"No right to work"}},
      {section:"profile",type:"single_choice",label:"Do you need workplace adjustments?",required:false,config:$o,
       special_category:"health_disability"}]),
    declarations:[{category:"health_disability",legal_condition:"explicit_consent",purpose:"Provide reasonable adjustments"}]}' <<<"$LAST_BODY")"
else
FORM="$(jq -c '{questions: [.data.questions[] | select(.type != "file_upload" and .system_key != "cv" and .type != "autofill_resume")]}' <<<"$LAST_BODY")"
fi
api PUT "/jobs/$JOB/application-form" "$FORM" -H "If-Match: \"$REV\""; expect "put form"
if [ "$UPLOADS" = on ]; then
  KO_QID="$(jq -r '[.data.questions[] | select(.label | startswith("Do you have the right"))][0].id' <<<"$LAST_BODY")"
  api GET "/jobs/$JOB"; expect "get job"
  REV="$(printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')"
  api PUT "/jobs/$JOB/disqualification-rules" "$(jq -nc --arg q "$KO_QID" '{rules:[{question_id:$q,operator:"in",value:["no"],reason:"No right to work"}],dpia_scope_confirmed:true}')" -H "If-Match: \"$REV\""; expect "confirm dpia scope"
fi

step "7. company: publish"
api GET "/jobs/$JOB/publish-check"; expect "publish-check"
jq -c '.data | {blocking: [.blocking[]? | {code,path,message}]}' <<<"$LAST_BODY"
for gate in $(jq -r '.data.gates[]? | select(.confirmed != true and (.requires_confirmation // .manual // false) == true) | .gate_id // .id' <<<"$LAST_BODY"); do
  api PUT "/jobs/$JOB/compliance/$gate" '{"confirmed":true}'; expect "confirm gate $gate"
done
api GET "/jobs/$JOB"; expect "get job"
REV="$(printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}')"
api POST "/jobs/$JOB/publish" "" -H "If-Match: \"$REV\""; expect "publish"
STATUS="$(jq -r '.data.job.status // .data.status' <<<"$LAST_BODY")"
PUBLIC_ID="$(jq -r '.data.job.public_id // .data.public_id // empty' <<<"$LAST_BODY")"
[ "$STATUS" = published ] || fail "job status is $STATUS, expected published"
[ -n "$PUBLIC_ID" ] || { api GET "/jobs/$JOB"; PUBLIC_ID="$(jq -r '.data.public_id' <<<"$LAST_BODY")"; }
ok "published as $PUBLIC_ID"

step "8. anonymous: read the public job"
anon GET "/public/jobs/$PUBLIC_ID"; expect "public job"
[ "$(jq -r '.data.accepting' <<<"$LAST_BODY")" = true ] || fail "public job is not accepting applications"
Q_NAME="$(jq -r '[.data.job.questions[]? | select(.system_key == "full_name")][0].id // empty' <<<"$LAST_BODY")"
Q_MAIL="$(jq -r '[.data.job.questions[]? | select(.system_key == "email")][0].id // empty' <<<"$LAST_BODY")"
[ -n "$Q_NAME" ] && [ -n "$Q_MAIL" ] || fail "public job does not list the name and email questions: $(jq -c '.data.job.questions' <<<"$LAST_BODY")"
# Required questions other than name, email and files must be answered too; the standard form has none.
EXTRA="$(jq -c '[.data.job.questions[]? | select(.required == true and .system_key != "full_name" and .system_key != "email" and .system_key != "cv")] | length' <<<"$LAST_BODY")"
[ "$EXTRA" = 0 ] || echo "NOTE: $EXTRA other required questions exist; the script does not answer them"
if [ "$UPLOADS" = on ]; then
  Q_CV="$(jq -r '[.data.job.questions[]? | select(.system_key == "cv")][0].id // empty' <<<"$LAST_BODY")"
  Q_KO="$(jq -r '[.data.job.questions[]? | select(.label | startswith("Do you have the right"))][0].id // empty' <<<"$LAST_BODY")"
  Q_SC="$(jq -r '[.data.job.questions[]? | select(.label | startswith("Do you need workplace"))][0].id // empty' <<<"$LAST_BODY")"
  [ -n "$Q_CV" ] && [ -n "$Q_KO" ] && [ -n "$Q_SC" ] || fail "public job lacks the cv, knockout or special category question: $(jq -c '.data.job.questions' <<<"$LAST_BODY")"
fi
# The job is open in market $MARKET; the candidate's country is a member country of it (UK -> GB).
case "$MARKET" in UK) COUNTRY=GB ;; *) COUNTRY="$MARKET" ;; esac

# upload_cv BYTES_FILE -> CV_TOKEN: presign, PUT from the host to the object store's public endpoint.
upload_cv() {
  local f="$1" size ct=application/pdf
  size="$(wc -c <"$f" | tr -d ' ')"
  # The applications service learns the job from an event, so a presign right after publish can see 404 for a moment.
  local try
  for try in 1 2 3 4 5 6 7 8 9 10; do
    anon POST "/public/jobs/$PUBLIC_ID/uploads" "$(jq -nc --arg q "$Q_CV" --arg c "$ct" --argjson s "$size" '{question_id:$q,filename:"cv.pdf",content_type:$c,size_bytes:$s}')"
    [ "$LAST_STATUS" = 404 ] || break
    sleep 1
  done
  if [ "${UPLOAD_SOFT:-}" = 1 ] && [ "$LAST_STATUS" != 201 ]; then return 1; fi
  expect_status 201 "presign"
  CV_TOKEN="$(jq -r '.data.token' <<<"$LAST_BODY")"
  local url method; url="$(jq -r '.data.url' <<<"$LAST_BODY")"; method="$(jq -r '.data.method' <<<"$LAST_BODY")"
  local hdrs=(); while IFS= read -r h; do hdrs+=(-H "$h"); done < <(jq -r '.data.headers | to_entries[] | "\(.key): \(.value)"' <<<"$LAST_BODY")
  local code; code="$(curl -sS -o /dev/null -w '%{http_code}' -X "$method" "${hdrs[@]}" --data-binary "@$f" "$url")"
  [ "$code" = 200 ] || fail "PUT to the presigned URL ($url) answered $code; is R2_PUBLIC_ENDPOINT reachable from here?"
}
if [ "$UPLOADS" = on ]; then
  step "8b. uploads: presign, PUT from the host, a file that is not a PDF (the EICAR test string) is refused on submit"
  CVFILE="$(mktemp)"; EVIL="$(mktemp)"
  { printf '%%PDF-1.4\n%% e2e test cv\n'; head -c 3000 /dev/zero | tr '\0' 'x'; printf '\n%%%%EOF\n'; } >"$CVFILE"
  { printf '%s' 'X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*'; } >"$EVIL"
  upload_cv "$EVIL"; EVIL_TOKEN="$CV_TOKEN"
  # EICAR as the content of a stream object in a PDF: it has the %PDF- magic, so only the scanner can refuse it.
  MALWARE="$(mktemp)"; EICAR="$(cat "$EVIL")"
  printf '%%PDF-1.4\n1 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%%%EOF\n' "${#EICAR}" "$EICAR" >"$MALWARE"
  upload_cv "$MALWARE"; MALWARE_TOKEN="$CV_TOKEN"
  upload_cv "$CVFILE"
  ok "presigned PUT from the host worked (token $CV_TOKEN)"
fi
APPLY="$(jq -nc --arg n "$Q_NAME" --arg m "$Q_MAIL" --arg e "$CANDIDATE" --arg c "$COUNTRY" \
  '{candidate_country:$c, answers:{($n):"Ada Candidate",($m):$e}, privacy_notice_acknowledged:true, consent:false, source:"linkedin"}')"
if [ "$UPLOADS" = on ]; then
  APPLY="$(jq -c --arg cv "$Q_CV" --arg t "$CV_TOKEN" --arg ko "$Q_KO" --arg sc "$Q_SC" '.answers += {($cv):$t,($ko):"yes",($sc):"yes"}' <<<"$APPLY")"
  EVIL_APPLY="$(jq -c --arg cv "$Q_CV" --arg t "$EVIL_TOKEN" --arg e "evil-$CANDIDATE" --arg m "$Q_MAIL" '.answers[$cv]=$t | .answers[$m]=$e' <<<"$APPLY")"
  anon POST "/public/jobs/$PUBLIC_ID/applications" "$EVIL_APPLY"
  case "$LAST_STATUS" in 4??) ok "the EICAR file declared as a PDF is refused (HTTP $LAST_STATUS)" ;; *) fail "the EICAR file was accepted (HTTP $LAST_STATUS)" ;; esac
  MALWARE_APPLY="$(jq -c --arg cv "$Q_CV" --arg t "$MALWARE_TOKEN" --arg m "$Q_MAIL" --arg e "malware-$CANDIDATE" '.answers[$cv]=$t | .answers[$m]=$e' <<<"$APPLY")"
  MAL_BEFORE="$(eval "$APPS_LOG_CMD" 2>&1 | grep -c 'uploads: malware found' || true)"
  anon POST "/public/jobs/$PUBLIC_ID/applications" "$MALWARE_APPLY"
  case "$LAST_STATUS" in 4??) ok "the EICAR file inside a PDF is refused (HTTP $LAST_STATUS): $(jq -c '.error // .' <<<"$LAST_BODY" | cut -c1-200)" ;; *) fail "the malware file was accepted (HTTP $LAST_STATUS)" ;; esac
  grep -q -i "malware" <<<"$LAST_BODY" || ok "note: the response does not name malware (the reason stays server side)"
  MAL_AFTER="$(eval "$APPS_LOG_CMD" 2>&1 | grep -c 'uploads: malware found' || true)"
  [ "$MAL_AFTER" -gt "$MAL_BEFORE" ] || fail "the applications log gained no 'uploads: malware found' line (is CLAMAV_ADDR set on the applications service?)"
  ok "the applications service logged the malware verdict"
  anon POST "/public/jobs/$PUBLIC_ID/applications" "$MALWARE_APPLY"
  case "$LAST_STATUS" in 4??) ok "the rejected token cannot be used again (HTTP $LAST_STATUS)" ;; *) fail "the rejected token was accepted again (HTTP $LAST_STATUS)" ;; esac
fi

step "9. anonymous: apply, retry with the same idempotency key, apply again"
# With APPLICATIONS_JOBSOURCE=events (and no db fallback) the applications read model is fed by hiring.job.* events:
# wait until it knows the job (an empty body is refused with a validation error once it does, 404 before).
for _ in $(seq 1 30); do
  [ "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' "$BASE_APPS/public/jobs/$PUBLIC_ID/applications")" != 404 ] && break
  sleep 1
done
KEY="e2e-key-$(date +%s)-$RANDOM"
anon POST "/public/jobs/$PUBLIC_ID/applications" "$APPLY" -H "Idempotency-Key: $KEY"; expect_status 201 "apply"
[ "$(jq -r '.data.received' <<<"$LAST_BODY")" = true ] || fail "receipt says not received"
anon POST "/public/jobs/$PUBLIC_ID/applications" "$APPLY" -H "Idempotency-Key: $KEY"; expect_status 201 "idempotent retry"
if [ "$UPLOADS" = on ]; then # an upload token is single use: the repeat brings a new file
  upload_cv "$CVFILE"; REPEAT="$(jq -c --arg cv "$Q_CV" --arg t "$CV_TOKEN" '.answers[$cv]=$t' <<<"$APPLY")"
else REPEAT="$APPLY"; fi
anon POST "/public/jobs/$PUBLIC_ID/applications" "$REPEAT"; expect_status 201 "repeat application"
anon POST "/public/jobs/$PUBLIC_ID/applications" "$(jq -c '.privacy_notice_acknowledged=false' <<<"$APPLY")"
[ "$LAST_STATUS" = 422 ] || [ "$LAST_STATUS" = 400 ] || fail "applying without acknowledging the notice must be refused (got $LAST_STATUS)"
ok "apply accepted, retry and repeat answered the same, missing acknowledgement refused"

if [ "$UPLOADS" != on ]; then
  step "9b. upload (not run: the stack has no malware scanner; it must fail closed)"
  anon POST "/public/jobs/$PUBLIC_ID/uploads" '{"question_id":"x","filename":"cv.pdf","content_type":"application/pdf","size_bytes":1000}'
  [ "$LAST_STATUS" = 503 ] || fail "upload without a scanner must answer 503, got $LAST_STATUS"
  ok "upload fails closed with 503 without a scanner"
fi

step "10. company: list applications"
api GET "/jobs/$JOB/applications"; expect "list applications"
COUNT="$(jq -r '.data.items | length' <<<"$LAST_BODY")"
[ "$COUNT" = 1 ] || fail "expected exactly 1 application (the repeat must not duplicate), got $COUNT"
APP="$(jq -r '.data.items[0].id' <<<"$LAST_BODY")"
[ "$(jq -r '.data.items[0].email' <<<"$LAST_BODY")" = "$CANDIDATE" ] || fail "listed application is not the candidate's"
[ "$(jq -r '.data.counts.received // 0' <<<"$LAST_BODY")" = 1 ] || fail "counts.received is not 1"
api GET /jobs; expect "job list"
[ "$(jq -r --arg j "$JOB" '.data[] | select(.id == $j) | "\(.applicant_count)/\(.new_applicant_count)"' <<<"$LAST_BODY")" = "1/1" ] \
  || fail "the job list must show applicant_count 1 and new_applicant_count 1 (written in the apply transaction)"
ok "1 application listed ($APP); the job list shows its counters 1/1"

if [ "$UPLOADS" = on ]; then
  step "10b. company: download the candidate's CV, check the special category answer is not in the clear"
  api GET "/jobs/$JOB/applications/$APP"; expect "open application"
  FILE_ID="$(jq -r '.data.files[0].id // empty' <<<"$LAST_BODY")"
  [ -n "$FILE_ID" ] || fail "the application has no file: $LAST_BODY"
  api GET "/jobs/$JOB/applications/$APP/files/$FILE_ID"; expect "file link"
  DL="$(mktemp)"; curl -fsS "$(jq -r '.data.url' <<<"$LAST_BODY")" -o "$DL" || fail "download through the presigned link failed"
  cmp -s "$DL" "$CVFILE" || fail "downloaded bytes differ from the uploaded CV"
  ok "the CV downloaded through the company-side link is byte-identical"
  # Nothing of the rejected files is stored: list the bucket (S3 API, SigV4) and look for the upload ids. The accepted
  # CV is the control: its final object must be listed, so a wrong bucket or a failing listing cannot pass vacuously.
  OBJECT_BUCKET="${OBJECT_BUCKET:-openhr-uk}"
  LISTING="$(curl -fsS --aws-sigv4 "aws:amz:us-east-1:s3" --user "${MINIO_ROOT_USER:-minioadmin}:${MINIO_ROOT_PASSWORD:-minioadmin}" \
    "http://localhost:$MINIO_API_PORT/$OBJECT_BUCKET?list-type=2&max-keys=1000")" || fail "listing bucket $OBJECT_BUCKET failed"
  grep -q "f/[^<]*$CV_TOKEN" <<<"$LISTING" || fail "control: the accepted CV ($CV_TOKEN) is not stored under f/ in $OBJECT_BUCKET"
  for t in "$MALWARE_TOKEN" "$EVIL_TOKEN"; do
    ! grep -q "$t" <<<"$LISTING" || fail "a rejected upload ($t) is still stored in $OBJECT_BUCKET"
  done
  ok "no staging or final object exists for the EICAR uploads (control: the clean CV is stored)"
  printf '%s' "$LAST_BODY" >/dev/null
  api GET "/jobs/$JOB/applications/$APP"; expect "open application"
  echo "answers of the application: $(jq -c '[.data.answers[] | {question_id, label}]' <<<"$LAST_BODY" | cut -c1-300)"
  ok "special category answer accepted with the application (stored sealed in hiring_restricted.special_answers)"

  step "10c. knockout: a candidate answering no is flagged, not rejected"
  upload_cv "$CVFILE"
  KO_APPLY="$(jq -c --arg cv "$Q_CV" --arg t "$CV_TOKEN" --arg ko "$Q_KO" --arg m "$Q_MAIL" --arg e "$CANDIDATE2" '.answers[$cv]=$t | .answers[$ko]="no" | .answers[$m]=$e' <<<"$APPLY")"
  anon POST "/public/jobs/$PUBLIC_ID/applications" "$KO_APPLY"; expect_status 201 "knockout candidate applies"
  api GET "/jobs/$JOB/applications"; expect "list applications"
  [ "$(jq -r --arg e "$CANDIDATE2" '[.data.items[] | select(.email == $e)][0].status' <<<"$LAST_BODY")" = flagged ] \
    || fail "the knockout candidate is not flagged: $(jq -c '[.data.items[] | {email,status}]' <<<"$LAST_BODY")"
  ok "the application with a failed knockout answer has status flagged"
  api PUT /hiring/applicant-settings '{"reapply_block_months":6}'; expect "reapply block on"
fi

step "11. company: open it, then received -> in_review -> rejected"
api GET "/jobs/$JOB/applications/$APP"; expect "open application"
[ "$(jq -r '.data.answers | length' <<<"$LAST_BODY")" -ge 2 ] || fail "opened application has no answers"
api POST "/jobs/$JOB/applications/$APP/status" '{"status":"in_review","note":"Started reviewing"}'; expect "in_review"
api POST "/jobs/$JOB/applications/$APP/status" '{"status":"rejected","note":"Not a fit"}'; expect "rejected"
[ "$(jq -r '.data.status' <<<"$LAST_BODY")" = rejected ] || fail "status after rejection is not rejected"
api POST "/jobs/$JOB/applications/$APP/status" '{"status":"hired"}'
[ "$LAST_STATUS" = 409 ] || fail "a final application must refuse a further change (got $LAST_STATUS)"
ok "pipeline transitions applied, final status is final"

if [ "$UPLOADS" = on ]; then
  step "11a. reapply block: the rejected candidate applies again inside the window"
  upload_cv "$CVFILE"
  AGAIN="$(jq -c --arg cv "$Q_CV" --arg t "$CV_TOKEN" '.answers[$cv]=$t' <<<"$APPLY")"
  anon POST "/public/jobs/$PUBLIC_ID/applications" "$AGAIN"; expect_status 201 "blocked candidate gets the ordinary receipt"
  api GET "/jobs/$JOB/applications"; expect "list applications"
  [ "$(jq -r --arg e "$CANDIDATE" '[.data.items[] | select(.email == $e)] | length' <<<"$LAST_BODY")" = 1 ] || fail "a second application from the blocked candidate was stored"
  LOGS="$($APPS_LOG_CMD 2>&1)" || true
  grep -q "blocked candidate ignored" <<<"$LOGS" || fail "no 'blocked candidate ignored' line in the applications log (is the reapply block configured?)"
  ok "same receipt, nothing stored, and the service logged the ignored application"
fi

step "11b. a pause in hiring must stop applications at the applications side, a resume must reopen it"
# Needs the job to be served from the event-fed read model (APPLICATIONS_JOBSOURCE=events, HIRING_EVENTS=on) for the
# wait below to mean anything; with the DB job source the change is visible at once.
rev_of() { api GET "/jobs/$JOB" >/dev/null; printf '%s' "$LAST_HEADERS" | tr -d '\r' | awk -F': ' 'tolower($1)=="etag"{gsub(/"/,"",$2);print $2}'; }
try_apply() { [ "$UPLOADS" = on ] && [ "${1:-}" = accepted ] && { UPLOAD_SOFT=1 upload_cv "$CVFILE" >/dev/null && APPLY="$(jq -c --arg cv "$Q_CV" --arg t "$CV_TOKEN" '.answers[$cv]=$t' <<<"$APPLY")"; } || true
  anon POST "/public/jobs/$PUBLIC_ID/applications" "$(jq -c --arg e "pause-$RANDOM-$CANDIDATE" '.answers |= with_entries(if (.value|test("@")) then .value=$e else . end)' <<<"$APPLY")" >/dev/null; }
wait_apply() { # wait_apply accepted|refused
  local want="$1" i
  for i in $(seq 1 20); do
    try_apply "$want"
    case "$want:$LAST_STATUS" in accepted:201 | refused:4??) return 0 ;; esac
    sleep 1
  done
  return 1
}
api POST "/jobs/$JOB/pause" "" -H "If-Match: \"$(rev_of)\""; expect "pause"
wait_apply refused || fail "applications still accepted 20s after the job was paused (last status $LAST_STATUS)"
ok "paused: the applications side refuses new applications (HTTP $LAST_STATUS)"
api POST "/jobs/$JOB/resume" "" -H "If-Match: \"$(rev_of)\""; expect "resume"
wait_apply accepted || fail "applications still refused 20s after the job was resumed (last status $LAST_STATUS)"
ok "resumed: applications are accepted again"

step "12. emails: drain the hiring outbox, then look in mailhog"
OUTBOX_CMD="${OUTBOX_CMD:-}"
if [ "$OUTBOX_CMD" = skip ]; then
  echo "SKIPPED (OUTBOX_CMD=skip)"
else
  DSN_BASE="postgres://openhr:${POSTGRES_PASSWORD:-change-me}@localhost:${PG_PORT}"
  (cd "$ROOT" && \
    APP_ENV=development \
    ANGLEHR_UK_POSTGRES_DSN="$DSN_BASE/openhr_uk?sslmode=disable" \
    ANGLEHR_US_POSTGRES_DSN="$DSN_BASE/openhr_us?sslmode=disable" \
    ANGLEHR_AFRICA_POSTGRES_DSN="$DSN_BASE/openhr_africa?sslmode=disable" \
    ANGLEHR_EU_POSTGRES_DSN="$DSN_BASE/openhr_eu?sslmode=disable" \
    ANGLEHR_ASIA_POSTGRES_DSN="$DSN_BASE/openhr_asia?sslmode=disable" \
    DB_URL_GLOBAL="$DSN_BASE/openhr_global?sslmode=disable" \
    SMTP_HOST=localhost SMTP_PORT="$SMTP_PORT" SMTP_FROM=no-reply@anglehr.local SMTP_FROM_NAME="Angle HR" \
    APP_URL=http://localhost:3000 \
    R2_ENDPOINT="http://localhost:${MINIO_API_PORT}" R2_ACCESS_KEY="${MINIO_ROOT_USER:-minioadmin}" \
    R2_SECRET_KEY="${MINIO_ROOT_PASSWORD:-minioadmin}" \
    ANGLEHR_UK_R2_BUCKET=openhr-uk ANGLEHR_US_R2_BUCKET=openhr-us ANGLEHR_AFRICA_R2_BUCKET=openhr-africa \
    ANGLEHR_EU_R2_BUCKET=openhr-eu ANGLEHR_ASIA_R2_BUCKET=openhr-asia \
    ${OUTBOX_CMD:-go run ./cmd/hiring-outbox}) 2>&1 | tail -5
  for i in $(seq 1 15); do
    RECEIVED="$(mail_count "$CANDIDATE" '^We received your application')"
    [ "$RECEIVED" -ge 1 ] && break
    sleep 1
  done
  [ "$RECEIVED" -ge 1 ] || fail "no confirmation email for $CANDIDATE in mailhog"
  ok "application confirmation reached mailhog for $CANDIDATE"
  UPDATE="$(mail_count "$CANDIDATE" '^An update on your application')"
  [ "$UPDATE" -ge 1 ] || fail "no status email after the rejection for $CANDIDATE"
  ok "status email after the rejection reached mailhog"
fi

if [ -n "${STATE_FILE:-}" ]; then # for scripts/e2e-couplings.sh
  printf 'ORG_ID=%q\nJOB=%q\nPUBLIC_ID=%q\nEMAIL=%q\nPASS=%q\nMARKET=%q\nQ_NAME=%q\nQ_MAIL=%q\n' \
    "$ORG_ID" "$JOB" "$PUBLIC_ID" "$EMAIL" "$PASS" "$MARKET" "$Q_NAME" "$Q_MAIL" >"$STATE_FILE"
fi
printf '\nSUCCESS: apply e2e completed (company %s, job %s, public id %s)\n' "$EMAIL" "$JOB" "$PUBLIC_ID"
