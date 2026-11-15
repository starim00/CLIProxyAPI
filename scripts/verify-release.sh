#!/bin/sh
# Run once on the target platform before building/promoting the same revision.
set -eu
export GOMAXPROCS="${GOMAXPROCS:-2}"
scope="${1:-custom}"
case "$scope" in
  custom)
    go test -p 2 -run 'AntiTruncation|Embedding|TTS|ConfigV8PersistsIndependentKeyNames' \
      ./internal/runtime/executor/... ./internal/api/handlers/management \
      ./sdk/api/handlers ./sdk/cliproxy
    ;;
  config)
    go test -p 2 ./internal/config ./internal/api/handlers/management ./internal/watcher/synthesizer
    ;;
  auth)
    go test -p 2 ./sdk/cliproxy/auth ./internal/watcher/synthesizer
    ;;
  executor)
    go test -p 2 ./internal/runtime/executor/... ./internal/translator/... ./sdk/api/handlers/...
    ;;
  full)
    go test -p 1 ./...
    ;;
  *)
    printf 'Unknown scope: %s (choose custom, config, auth, executor, full)\n' "$scope" >&2
    exit 2
    ;;
esac
