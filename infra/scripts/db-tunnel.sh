#!/usr/bin/env bash
# Open a tunnel from this machine to the staging RDS instance through the SSM bastion (no public DB, no SSH).
# Usage: scripts/db-tunnel.sh [local-port]      (default 15432; Ctrl-C closes the tunnel)
#        scripts/db-tunnel.sh --password        (print the master password, for DBeaver/psql)
# Needs: AWS credentials for the STAGING account (AWS_PROFILE=openhr-staging, after `aws sso login`),
#        the Session Manager plugin (brew install --cask session-manager-plugin), bastion_enabled = true.
set -euo pipefail

REGION="${AWS_REGION:-eu-west-2}"   # staging region; override with AWS_REGION
NAME="openhr-staging"
DB="$NAME"

aws_() { aws --region "$REGION" "$@"; }

if [ "${1:-}" = "--password" ]; then
  # The DSNs are postgres://openhr:<password>@host:5432/db?sslmode=require
  aws_ secretsmanager get-secret-value --secret-id "openhr/staging/DB_URL_GLOBAL" --query SecretString --output text \
    | sed -E 's|^postgres(ql)?://[^:]+:([^@]+)@.*|\2|'
  exit 0
fi

LOCAL_PORT="${1:-15432}"

command -v session-manager-plugin >/dev/null || {
  echo "session-manager-plugin is not installed: brew install --cask session-manager-plugin" >&2; exit 1; }

instance="$(aws_ ec2 describe-instances \
  --filters "Name=tag:Name,Values=$NAME-bastion" "Name=instance-state-name,Values=running" \
  --query 'Reservations[0].Instances[0].InstanceId' --output text)"
if [ -z "$instance" ] || [ "$instance" = "None" ]; then
  echo "No running $NAME-bastion instance. Set bastion_enabled = true in envs/staging and apply." >&2; exit 1
fi

read -r host status < <(aws_ rds describe-db-instances --db-instance-identifier "$DB" \
  --query 'DBInstances[0].[Endpoint.Address,DBInstanceStatus]' --output text)
if [ "$status" != "available" ]; then
  echo "RDS $DB is '$status'. If staging is stopped: scripts/staging-power.sh start" >&2; exit 1
fi

cat <<EOF
Tunnel: localhost:$LOCAL_PORT -> $host:5432 (via $instance)
  Host localhost   Port $LOCAL_PORT   User openhr   SSL require
  Databases: openhr_global, openhr_uk, openhr_us, openhr_eu, openhr_asia, openhr_africa
  Password:  scripts/db-tunnel.sh --password
Ctrl-C to close.
EOF

exec aws --region "$REGION" ssm start-session \
  --target "$instance" \
  --document-name AWS-StartPortForwardingSessionToRemoteHost \
  --parameters "{\"host\":[\"$host\"],\"portNumber\":[\"5432\"],\"localPortNumber\":[\"$LOCAL_PORT\"]}"
