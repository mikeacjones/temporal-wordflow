#!/usr/bin/env bash
set -euo pipefail

: "${AWS_REGION:?}"
: "${SECRET_ID:?}"
: "${TEMPORAL_API_KEY:?}"

aws secretsmanager put-secret-value \
  --region "$AWS_REGION" \
  --secret-id "$SECRET_ID" \
  --secret-string "$TEMPORAL_API_KEY" \
  >/dev/null
