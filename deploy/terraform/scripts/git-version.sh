#!/usr/bin/env bash
set -euo pipefail

sha="$(git rev-parse HEAD)"
printf '{"sha":"%s"}\n' "$sha"
