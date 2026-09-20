#!/usr/bin/env bash
set -euo pipefail

: "${CAMPAIGN_FILE:?}"
: "${CAMPAIGN_ID:?}"
: "${TASK_QUEUE:?}"
: "${TEMPORAL_ADDRESS:?}"
: "${TEMPORAL_API_KEY:?}"
: "${TEMPORAL_NAMESPACE:?}"

temporal workflow start \
  --workflow-id "wordflow-campaign/$CAMPAIGN_ID" \
  --type WordflowCampaignWorkflow \
  --task-queue "$TASK_QUEUE" \
  --id-conflict-policy UseExisting \
  --id-reuse-policy AllowDuplicateFailedOnly \
  --static-summary "Palm Springs Offsite Wordflow campaign" \
  --input-file "$CAMPAIGN_FILE" \
  >/dev/null

temporal workflow query \
  --workflow-id "wordflow-campaign/$CAMPAIGN_ID" \
  --type campaign-summary \
  >/dev/null
