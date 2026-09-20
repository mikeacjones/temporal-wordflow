#!/usr/bin/env bash
set -euo pipefail

if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  echo "The repository must be clean so the Worker Build ID identifies the deployed source exactly." >&2
  exit 1
fi

sha="$(git rev-parse HEAD)"
printf '{"sha":"%s"}\n' "$sha"
