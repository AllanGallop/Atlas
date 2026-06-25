#!/usr/bin/env bash
set -euo pipefail

API="${API:-http://localhost:8090}"
COLLECTORS="${COLLECTORS:-ct}"

echo "== Atlas benchmark sweep (collectors: $COLLECTORS) =="
printf "| Jobs | Queue (ms) | Worker (ms) | Total (ms) | Queue rate | Worker rate | Total rate |\n"
printf "| ---- | ---------- | ----------- | ---------- | ---------- | ----------- | ---------- |\n"

for RUNS in 100 500 1000 5000; do
  OUTPUT="$(RUNS="$RUNS" COLLECTORS="$COLLECTORS" API="$API" bash "$(dirname "$0")/bench.sh")"
  QUEUE_MS="$(printf "%s\n" "$OUTPUT" | awk '/^Queue time:/ {print $3}' | tr -d ms)"
  WORKER_MS="$(printf "%s\n" "$OUTPUT" | awk '/^Worker time:/ {print $3}' | tr -d ms)"
  TOTAL_MS="$(printf "%s\n" "$OUTPUT" | awk '/^Total time:/ {print $3}' | tr -d ms)"
  QUEUE_RATE="$(printf "%s\n" "$OUTPUT" | awk '/^Queue rate:/ {print $3}')"
  WORKER_RATE="$(printf "%s\n" "$OUTPUT" | awk '/^Worker rate:/ {print $3}')"
  TOTAL_RATE="$(printf "%s\n" "$OUTPUT" | awk '/^Total rate:/ {print $3}')"
  printf "| %s | %s | %s | %s | %s | %s | %s |\n" \
    "$RUNS" "$QUEUE_MS" "$WORKER_MS" "$TOTAL_MS" "$QUEUE_RATE" "$WORKER_RATE" "$TOTAL_RATE"
done
