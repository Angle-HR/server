# Sourced by the staging scripts (not run directly). Picks the staging profile, signs in when the session is missing or
# expired, and refuses to continue if the credentials are not for the staging account.
# Sign-in messages go to stderr so a script's stdout (e.g. db-tunnel.sh --password) stays clean.

STAGING_ACCOUNT_ID="291216788438"
export AWS_PROFILE="${AWS_PROFILE:-openhr-staging}"

ensure_aws_login() {
  local account
  if ! account="$(aws sts get-caller-identity --query Account --output text 2>/dev/null)"; then
    echo "AWS session for profile '$AWS_PROFILE' is missing or expired; signing in (a browser window opens)..." >&2
    if [ -n "$(aws configure get sso_session 2>/dev/null)$(aws configure get sso_start_url 2>/dev/null)" ]; then
      aws sso login --profile "$AWS_PROFILE" >&2
    else
      aws login --profile "$AWS_PROFILE" >&2
    fi
    account="$(aws sts get-caller-identity --query Account --output text)"
  fi
  if [ "$account" != "$STAGING_ACCOUNT_ID" ]; then
    echo "Profile '$AWS_PROFILE' is account $account, not staging ($STAGING_ACCOUNT_ID). Refusing to continue." >&2
    exit 1
  fi
}
