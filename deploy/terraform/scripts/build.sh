#!/usr/bin/env bash
set -euo pipefail

: "${DIST_DIR:?}"
: "${GOARCH:?}"
: "${REPO_ROOT:?}"

mkdir -p "$DIST_DIR/worker" "$DIST_DIR/api"

cd "$REPO_ROOT"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath -tags lambda.norpc -o "$DIST_DIR/worker/bootstrap" ./cmd/lambda-worker
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath -tags lambda.norpc -o "$DIST_DIR/api/bootstrap" ./cmd/lambda-api

chmod +x "$DIST_DIR/worker/bootstrap" "$DIST_DIR/api/bootstrap"
(cd "$DIST_DIR/worker" && zip -X -q -j ../worker.zip bootstrap)
(cd "$DIST_DIR/api" && zip -X -q -j ../api.zip bootstrap)
