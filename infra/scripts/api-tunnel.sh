#!/usr/bin/env bash
# Open a tunnel from this machine to the running staging API task through the SSM bastion, e.g. for the API docs
# at http://localhost:18080/api/docs before (or instead of) going through CloudFront.
# Usage: scripts/api-tunnel.sh [local-port]      (default 18080; Ctrl-C closes the tunnel)
# Uses AWS_PROFILE (default openhr-staging) and signs in if the session has expired. A new deployment replaces the
# task (new IP): run the script again.
# Needs: the Session Manager plugin (brew install --cask session-manager-plugin), bastion_enabled = true.
set -euo pipefail

REGION="${AWS_REGION:-eu-west-2}"   # staging region; override with AWS_REGION
NAME="openhr-staging"
CLUSTER="$NAME"
LOCAL_PORT="${1:-18080}"

aws_() { aws --region "$REGION" "$@"; }

# shellcheck source=_aws-session.sh
source "$(dirname "${BASH_SOURCE[0]}")/_aws-session.sh"
ensure_aws_login

command -v session-manager-plugin >/dev/null || {
  echo "session-manager-plugin is not installed: brew install --cask session-manager-plugin" >&2; exit 1; }

instance="$(aws_ ec2 describe-instances \
  --filters "Name=tag:Name,Values=$NAME-bastion" "Name=instance-state-name,Values=running" \
  --query 'Reservations[0].Instances[0].InstanceId' --output text)"
if [ -z "$instance" ] || [ "$instance" = "None" ]; then
  echo "No running $NAME-bastion instance. Set bastion_enabled = true in envs/staging and apply." >&2; exit 1
fi

task="$(aws_ ecs list-tasks --cluster "$CLUSTER" --service-name api --desired-status RUNNING --query 'taskArns[0]' --output text)"
if [ -z "$task" ] || [ "$task" = "None" ]; then
  echo "No running api task. If staging is stopped: scripts/staging-power.sh start" >&2; exit 1
fi
ip="$(aws_ ecs describe-tasks --cluster "$CLUSTER" --tasks "$task" \
  --query 'tasks[0].attachments[0].details[?name==`privateIPv4Address`].value | [0]' --output text)"

cat <<EOM
Tunnel: localhost:$LOCAL_PORT -> api task $ip:8080 (via $instance)
  API docs:  http://localhost:$LOCAL_PORT/api/docs
  API:       http://localhost:$LOCAL_PORT/api/v1/...
Ctrl-C to close.
EOM

exec aws --region "$REGION" ssm start-session \
  --target "$instance" \
  --document-name AWS-StartPortForwardingSessionToRemoteHost \
  --parameters "{\"host\":[\"$ip\"],\"portNumber\":[\"8080\"],\"localPortNumber\":[\"$LOCAL_PORT\"]}"
