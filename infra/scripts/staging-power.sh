#!/usr/bin/env bash
# Manually start/stop the staging stack to save cost. Usage: scripts/staging-power.sh start|stop|status
# Uses AWS_PROFILE (default openhr-staging) and signs in if the session has expired. Needs jq.
# NAT, ALB, ElastiCache and WAF cannot be stopped and keep billing either way.
set -euo pipefail

REGION="${AWS_REGION:-eu-west-2}"   # staging region; override with AWS_REGION
NAME="openhr-staging"
CLUSTER="$NAME"
DB="$NAME"
declare -A COUNTS=( [clamav]=1 [api]=1 [email-worker]=1 [fluvio-ui]=1 [client]=1 [admin]=1 )  # keep in sync with local.running_counts

aws_() { aws --region "$REGION" "$@"; }

# shellcheck source=_aws-session.sh
source "$(dirname "${BASH_SOURCE[0]}")/_aws-session.sh"
case "${1:-}" in start|stop|status) ensure_aws_login ;; esac

set_schedules() { # ENABLED|DISABLED for every job schedule
  local state="$1" n
  for n in $(aws_ scheduler list-schedules --name-prefix "$NAME-" --query 'Schedules[].Name' --output text); do
    aws_ scheduler get-schedule --name "$n" \
      | jq --arg s "$state" 'del(.Arn,.CreationDate,.LastModificationDate,.ResponseMetadata) | .State=$s' > /tmp/sched.json
    aws_ scheduler update-schedule --cli-input-json file:///tmp/sched.json >/dev/null
    echo "  schedule $n -> $state"
  done
  rm -f /tmp/sched.json
}

set_services() { # desired count mode: up|down
  local svc c out
  for svc in "${!COUNTS[@]}"; do
    c=0; [ "$1" = up ] && c="${COUNTS[$svc]}"
    if ! out="$(aws_ ecs update-service --cluster "$CLUSTER" --service "$svc" --desired-count "$c" 2>&1 >/dev/null)"; then
      if [[ "$out" == *ServiceNotFoundException* ]]; then echo "  service $svc not deployed, skipped"; continue; fi
      echo "$out" >&2; exit 1
    fi
    echo "  service $svc -> $c"
  done
}

db_status() { aws_ rds describe-db-instances --db-instance-identifier "$DB" --query 'DBInstances[0].DBInstanceStatus' --output text; }

case "${1:-}" in
  stop)
    echo "Disabling job schedules, scaling services to 0, stopping RDS"
    set_schedules DISABLED
    set_services down
    [ "$(db_status)" = available ] && aws_ rds stop-db-instance --db-instance-identifier "$DB" >/dev/null
    echo "Done. RDS auto-starts after 7 days stopped: stop it again if staging stays idle that long."
    ;;
  start)
    echo "Starting RDS, waiting until available"
    [ "$(db_status)" = stopped ] && aws_ rds start-db-instance --db-instance-identifier "$DB" >/dev/null
    aws_ rds wait db-instance-available --db-instance-identifier "$DB"
    set_services up
    set_schedules ENABLED
    echo "Done. Services take a few minutes to become healthy (ClamAV is slowest)."
    ;;
  status)
    echo "RDS: $(db_status)"
    aws_ ecs describe-services --cluster "$CLUSTER" --services "${!COUNTS[@]}" --query 'services[].[serviceName,desiredCount,runningCount]' --output text 2>/dev/null || true
    ;;
  *) echo "usage: $0 start|stop|status" >&2; exit 2 ;;
esac
