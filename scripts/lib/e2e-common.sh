# Shared helpers for the scripts/e2e-*.sh scripts (source it; needs curl and jq).
#
# Ports. Defaults are the openhr-local ports. When COMPOSE_PROJECT_NAME names another stack, a port (or the whole URL)
# must be given explicitly: the script fails fast instead of silently talking to a different stack on the default ports.
# The port variables are the docker compose ones: WEB_BFF_PORT, ADMIN_BFF_PORT, APPLICATIONS_PORT,
# MAILHOG_UI_PORT, MAILHOG_SMTP_PORT, NATS_MONITOR_PORT, POSTGRES_PORT, MINIO_API_PORT.
#
# Bootstrap admin with ADMIN_TOTP_REQUIRED: e2e_admin_login enrols the admin (authenticator secret kept in
# ADMIN_TOTP_SECRET_FILE, default $TMPDIR/openhr-e2e-<project>-admin-totp-secret) on the first login of a fresh stack and
# completes the TOTP step afterwards, playing the authenticator app with cmd/tools/totpcode. Delete the file when the
# stack is recreated (`make local-e2e-all` does).

E2E_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
E2E_PROJECT="${COMPOSE_PROJECT_NAME:-openhr-local}"
E2E_TOTP_FILE="${ADMIN_TOTP_SECRET_FILE:-${TMPDIR:-/tmp}/openhr-e2e-$E2E_PROJECT-admin-totp-secret}"

# e2e_port OUT PORTVAR DEFAULT: OUT=$PORTVAR, or DEFAULT only for the openhr-local project.
e2e_port() {
  local v="${!2:-}"
  if [ -z "$v" ]; then
    if [ "$E2E_PROJECT" != openhr-local ]; then
      echo "FAIL: COMPOSE_PROJECT_NAME=$E2E_PROJECT but $2 is not set; refusing to fall back to the default openhr-local port $3 (set $2 or the URL variable explicitly)" >&2
      exit 2
    fi
    v="$3"
  fi
  printf -v "$1" '%s' "$v"
}
# e2e_url URLVAR PORTVAR DEFAULT [SUFFIX]: an explicit URLVAR wins, else http://localhost:<port><suffix>.
e2e_url() {
  [ -n "${!1:-}" ] && return 0
  local p; e2e_port p "$2" "$3"
  printf -v "$1" 'http://localhost:%s%s' "$p" "${4:-}"
}

e2e_totp_code() { (cd "$E2E_ROOT" && go run ./cmd/tools/totpcode "$1"); }

# e2e_admin_login JAR BASE EMAIL PASSWORD: signs the admin in (completing or enrolling TOTP when the stack needs it);
# sets E2E_ADMIN_CSRF. Prints nothing but errors; returns 1 with a message on failure.
e2e_admin_login() {
  local jar="$1" base="$2" email="$3" pass="$4" body csrf secret code
  body="$(curl -sS -c "$jar" -b "$jar" -X POST "$base/admin/auth/login" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg e "$email" --arg p "$pass" '{email:$e,password:$p}')")"
  if [ "$(jq -r '.data.totp_required // false' <<<"$body")" = true ]; then
    secret="${ADMIN_TOTP_SECRET:-}"
    [ -z "$secret" ] && [ -f "$E2E_TOTP_FILE" ] && secret="$(cat "$E2E_TOTP_FILE")"
    [ -n "$secret" ] || { echo "FAIL: $email has TOTP enabled but no secret is known (set ADMIN_TOTP_SECRET or $E2E_TOTP_FILE, or use a fresh stack)" >&2; return 1; }
    # A code generated right at the end of its 30 s step can be stale on arrival: a wrong code keeps the pre-auth alive,
    # so retry with a fresh one (well under the five-attempt lockout).
    for _ in 1 2 3; do
      code="$(e2e_totp_code "$secret")"
      body="$(curl -sS -c "$jar" -b "$jar" -X POST "$base/admin/auth/login/totp" -H 'Content-Type: application/json' -d "$(jq -nc --arg c "$code" '{code:$c}')")"
      jq -e '.data.csrf_token' >/dev/null <<<"$body" && break
      sleep 2
    done
  elif [ "$(jq -r '.data.totp_enrollment_required // false' <<<"$body")" = true ]; then
    csrf="$(jq -r '.data.csrf_token' <<<"$body")"
    body="$(curl -sS -c "$jar" -b "$jar" -X POST "$base/admin/auth/totp/enroll" -H "X-CSRF-Token: $csrf")"
    secret="$(jq -r '.data.secret // empty' <<<"$body")"
    [ -n "$secret" ] || { echo "FAIL: TOTP enrol: $body" >&2; return 1; }
    code="$(e2e_totp_code "$secret")"
    body="$(curl -sS -c "$jar" -b "$jar" -X POST "$base/admin/auth/totp/confirm" -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' -d "$(jq -nc --arg c "$code" '{code:$c}')")"
    if jq -e '.data.csrf_token' >/dev/null <<<"$body"; then
      ( umask 077; printf '%s\n' "$secret" >"$E2E_TOTP_FILE" )
      echo "enrolled TOTP for $email (secret kept in $E2E_TOTP_FILE)" >&2
    fi
  fi
  E2E_ADMIN_CSRF="$(jq -r '.data.csrf_token // empty' <<<"$body")"
  [ -n "$E2E_ADMIN_CSRF" ] || { echo "FAIL: admin login for $email: $body" >&2; return 1; }
}
