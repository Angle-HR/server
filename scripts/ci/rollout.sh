#!/usr/bin/env bash
# Rolls the backend images tagged $TAG out to one environment's ECS cluster. Run by Container Images after the
# push; the deploy role needs `rollout` set on module.github_deploy in Angle-HR/infra.
#
#   1. Registers a revision of the migrate task with the new image and runs it (skipped while staging is asleep).
#   2. Registers revisions of every service whose image is one of ours and points the service at it.
#   3. Does the same for the job schedules ($SCHEDULE_PREFIX*), keeping each schedule's ENABLED/DISABLED state.
#   4. Waits for the services to settle.
#
# Terraform resolves the image to the newest sha in ECR, so a later `terraform apply` lands on the same image.
#
# Env: CLUSTER, TAG, MIGRATE_TASK_DEFINITION, MIGRATE_SUBNETS, MIGRATE_SECURITY_GROUP, SCHEDULE_PREFIX,
#      API_SERVICE (default api; desiredCount 0 means staging is asleep).
set -euo pipefail

: "${CLUSTER:?}" "${TAG:?}" "${MIGRATE_TASK_DEFINITION:?}" "${MIGRATE_SUBNETS:?}" "${MIGRATE_SECURITY_GROUP:?}" "${SCHEDULE_PREFIX:?}"
API_SERVICE="${API_SERVICE:-api}"

# Repositories built by Container Images; images from anywhere else (clamav, redis, frontends) are left alone.
OUR_IMAGES='/openhr/(server|email-worker|jobs|migrate):'

# Prints the ARN of a revision of $1 whose images from OUR_IMAGES carry $TAG, registering one if needed.
retag() {
  local td="$1" def new
  def="$(aws ecs describe-task-definition --task-definition "$td" --query taskDefinition --output json)"
  new="$(jq --arg tag "$TAG" --arg re "$OUR_IMAGES" '
    .containerDefinitions |= map(if (.image | test($re)) then .image = (.image | sub(":[^:/]+$"; ":" + $tag)) else . end)' <<<"$def")"
  if [[ "$(jq -c .containerDefinitions <<<"$def")" == "$(jq -c .containerDefinitions <<<"$new")" ]]; then
    jq -r .taskDefinitionArn <<<"$def"
    return
  fi
  jq 'del(.taskDefinitionArn, .revision, .status, .requiresAttributes, .compatibilities,
          .registeredAt, .registeredBy, .deregisteredAt)' <<<"$new" >"$RUNNER_TEMP/td.json"
  aws ecs register-task-definition --cli-input-json "file://$RUNNER_TEMP/td.json" \
    --query taskDefinition.taskDefinitionArn --output text
}

RUNNER_TEMP="${RUNNER_TEMP:-$(mktemp -d)}"

# --- 1. migrations -------------------------------------------------------------------------------------------
migrate_td="$(retag "$MIGRATE_TASK_DEFINITION")"
echo "migrate -> ${migrate_td##*/}"

awake="$(aws ecs describe-services --cluster "$CLUSTER" --services "$API_SERVICE" \
  --query 'services[0].desiredCount' --output text)"
if [[ "$awake" == "0" ]]; then
  echo "::warning::$API_SERVICE is scaled to 0 (asleep, RDS stopped): migrations NOT run. Wake staging, then run Database Migrations."
else
  task_arn="$(aws ecs run-task --cluster "$CLUSTER" --task-definition "$migrate_td" --launch-type FARGATE \
    --network-configuration "awsvpcConfiguration={subnets=[${MIGRATE_SUBNETS}],securityGroups=[${MIGRATE_SECURITY_GROUP}],assignPublicIp=DISABLED}" \
    --started-by "github-${GITHUB_RUN_ID:-local}" --query 'tasks[0].taskArn' --output text)"
  [[ -n "$task_arn" && "$task_arn" != "None" ]] || { echo "run-task did not start a migrate task" >&2; exit 1; }
  echo "Migrating: $task_arn"
  for _ in 1 2 3; do aws ecs wait tasks-stopped --cluster "$CLUSTER" --tasks "$task_arn" && break; done
  family="${MIGRATE_TASK_DEFINITION##*/}"; family="${family%%:*}"
  aws logs get-log-events --log-group-name "/ecs/${family}" --log-stream-name "job/${family}/${task_arn##*/}" \
    --start-from-head --query 'events[].message' --output text | tr '\t' '\n' || echo "(no migrate logs)"
  read -r exit_code reason < <(aws ecs describe-tasks --cluster "$CLUSTER" --tasks "$task_arn" \
    --query 'tasks[0].[containers[0].exitCode, stoppedReason]' --output text)
  if [[ "$exit_code" != "0" ]]; then
    echo "Migrations failed (exit ${exit_code}: ${reason}); nothing rolled out." >&2
    exit 1
  fi
fi

# --- 2. services ---------------------------------------------------------------------------------------------
updated=()
mapfile -t services < <(aws ecs list-services --cluster "$CLUSTER" --query 'serviceArns[]' --output text | tr '\t' '\n' | sed '/^$/d')
for ((i = 0; i < ${#services[@]}; i += 10)); do
  while read -r name current; do
    next="$(retag "$current")"
    [[ "$next" == "$current" ]] && continue
    aws ecs update-service --cluster "$CLUSTER" --service "$name" --task-definition "$next" >/dev/null
    echo "service $name -> ${next##*/}"
    updated+=("$name")
  done < <(aws ecs describe-services --cluster "$CLUSTER" --services "${services[@]:i:10}" \
    --query 'services[].[serviceName, taskDefinition]' --output text)
done

# --- 3. job schedules ----------------------------------------------------------------------------------------
for name in $(aws scheduler list-schedules --name-prefix "$SCHEDULE_PREFIX" --query 'Schedules[].Name' --output text); do
  sched="$(aws scheduler get-schedule --name "$name" --output json)"
  current="$(jq -r '.Target.EcsParameters.TaskDefinitionArn // empty' <<<"$sched")"
  [[ -n "$current" ]] || continue
  next="$(retag "$current")"
  [[ "$next" == "$current" ]] && continue
  jq --arg td "$next" '.Target.EcsParameters.TaskDefinitionArn = $td
    | {Name, GroupName, ScheduleExpression, ScheduleExpressionTimezone, StartDate, EndDate, Description, State,
       KmsKeyArn, FlexibleTimeWindow, Target, ActionAfterCompletion}
    | with_entries(select(.value != null))' <<<"$sched" >"$RUNNER_TEMP/schedule.json"
  aws scheduler update-schedule --cli-input-json "file://$RUNNER_TEMP/schedule.json" >/dev/null
  echo "schedule $name -> ${next##*/} ($(jq -r .State <<<"$sched"))"
done

# --- 4. wait -------------------------------------------------------------------------------------------------
if ((${#updated[@]})); then
  echo "Waiting for ${updated[*]} to settle"
  stable=false
  for _ in 1 2 3; do aws ecs wait services-stable --cluster "$CLUSTER" --services "${updated[@]}" && { stable=true; break; }; done
  $stable || { echo "Services did not settle within 30 minutes" >&2; exit 1; }
  # A circuit-breaker rollback also ends "stable", on the old revision; check each service kept the new image.
  failed=0
  while read -r name td state; do
    image="$(aws ecs describe-task-definition --task-definition "$td" --query 'taskDefinition.containerDefinitions[0].image' --output text)"
    if [[ "${image##*:}" != "$TAG" || "$state" == "FAILED" ]]; then
      echo "::error::$name is on ${td##*/} ($image, rollout $state), not $TAG" >&2
      failed=1
    fi
  done < <(aws ecs describe-services --cluster "$CLUSTER" --services "${updated[@]}" \
    --query 'services[].[serviceName, deployments[?status==`PRIMARY`]|[0].taskDefinition, deployments[?status==`PRIMARY`]|[0].rolloutState]' --output text)
  exit "$failed"
fi
