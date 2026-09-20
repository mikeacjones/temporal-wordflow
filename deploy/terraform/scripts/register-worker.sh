#!/usr/bin/env bash
set -euo pipefail

: "${DEPLOYMENT_NAME:?}"
: "${EXTERNAL_ID:?}"
: "${INVOCATION_ROLE_ARN:?}"
: "${LAMBDA_FUNCTION_ARN:?}"
: "${TASK_QUEUE:?}"
: "${TEMPORAL_ADDRESS:?}"
: "${TEMPORAL_API_KEY:?}"
: "${TEMPORAL_NAMESPACE:?}"
: "${WORKER_BUILD_ID:?}"

if ! temporal worker deployment describe --name "$DEPLOYMENT_NAME" >/dev/null 2>&1; then
  temporal worker deployment create --name "$DEPLOYMENT_NAME"
fi

if ! temporal worker deployment describe-version \
  --deployment-name "$DEPLOYMENT_NAME" \
  --build-id "$WORKER_BUILD_ID" \
  >/dev/null 2>&1; then
  temporal worker deployment create-version \
    --deployment-name "$DEPLOYMENT_NAME" \
    --build-id "$WORKER_BUILD_ID" \
    --aws-lambda-function-arn "$LAMBDA_FUNCTION_ARN" \
    --aws-lambda-assume-role-arn "$INVOCATION_ROLE_ARN" \
    --aws-lambda-assume-role-external-id "$EXTERNAL_ID"
fi

for _ in {1..60}; do
  version="$(temporal worker deployment describe-version \
    --deployment-name "$DEPLOYMENT_NAME" \
    --build-id "$WORKER_BUILD_ID" \
    --report-task-queue-stats \
    --output json)"
  if jq -e --arg task_queue "$TASK_QUEUE" '.. | strings | select(. == $task_queue)' <<<"$version" >/dev/null; then
    temporal worker deployment set-current-version \
      --deployment-name "$DEPLOYMENT_NAME" \
      --build-id "$WORKER_BUILD_ID" \
      --yes
    exit 0
  fi
  sleep 2
done

echo "The Worker Deployment Version did not bind task queue $TASK_QUEUE." >&2
exit 1
