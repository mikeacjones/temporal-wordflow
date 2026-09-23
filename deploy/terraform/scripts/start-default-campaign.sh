#!/usr/bin/env bash
set -euo pipefail

: "${CAMPAIGN_FILE:?}"
: "${CAMPAIGN_ID:?}"
: "${REPO_ROOT:?}"
: "${TASK_QUEUE:?}"
: "${TEMPORAL_ADDRESS:?}"
: "${TEMPORAL_API_KEY:?}"
: "${TEMPORAL_NAMESPACE:?}"

cd "$REPO_ROOT"
go run ./cmd/campaign -file "$CAMPAIGN_FILE" -task-queue "$TASK_QUEUE" >/dev/null

for _ in {1..60}; do
	catalog="$(temporal workflow query \
		--workflow-id catalog/global \
		--type catalog \
		--input '{"player":{}}' \
		--output json 2>/dev/null || true)"
	if jq -e --arg campaign_id "$CAMPAIGN_ID" \
		'.. | objects | select(.campaignId? == $campaign_id)' <<<"$catalog" >/dev/null; then
		exit 0
	fi
	sleep 2
done

echo "Campaign $CAMPAIGN_ID did not register with the Catalog." >&2
exit 1
