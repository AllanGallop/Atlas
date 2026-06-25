#!/usr/bin/env bash
set -euo pipefail

API="${API:-http://localhost:8090}"
RUNS="${RUNS:-500}"
COLLECTORS="${COLLECTORS:-ct}"
TIMEOUT_SEC="${TIMEOUT_SEC:-600}"

now_ms() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import time; print(int(time.time() * 1000))'
  elif date +%s%3N >/dev/null 2>&1; then
    date +%s%3N
  else
    python - <<'PY'
import time
print(int(time.time() * 1000))
PY
  fi
}

read_progress_field() {
  local json="$1"
  local field="$2"
  printf "%s" "$json" | grep -o "\"$field\":[0-9]*" | head -1 | cut -d: -f2
}

build_seeds_json() {
  local count="$1"
  local suffix="${BENCH_SUFFIX:-$(date +%s)}"
  local seeds=""
  local i
  for i in $(seq 1 "$count"); do
    if [ -n "$seeds" ]; then
      seeds+=","
    fi
    seeds+="\"bench-${suffix}-${i}.invalid\""
  done
  printf '[%s]' "$seeds"
}

echo "== Atlas benchmark =="
echo "Jobs: $RUNS (1 campaign, collectors: $COLLECTORS)"

curl -fsS "$API/health" >/dev/null

SEEDS_JSON="$(build_seeds_json "$RUNS")"
COLLECTORS_JSON="[\"$(echo "$COLLECTORS" | sed 's/,/","/g')\"]"

PAYLOAD_FILE="$(mktemp)"
trap 'rm -f "$PAYLOAD_FILE"' EXIT
cat >"$PAYLOAD_FILE" <<EOF
{
  "seeds": $SEEDS_JSON,
  "collectors": $COLLECTORS_JSON,
  "limits": { "max_depth": 1, "max_entities": $((RUNS + 10)) }
}
EOF

TOTAL_START_MS="$(now_ms)"
QUEUE_START_MS="$(now_ms)"

CREATE_RESPONSE="$(curl -fsS -X POST "$API/campaigns" \
  -H "Content-Type: application/json" \
  -d @"$PAYLOAD_FILE")"

CAMPAIGN_ID="$(printf "%s" "$CREATE_RESPONSE" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)"
if [ -z "$CAMPAIGN_ID" ]; then
  echo "Failed to create campaign: $CREATE_RESPONSE"
  exit 1
fi

QUEUE_END_MS="$(now_ms)"
QUEUE_MS=$((QUEUE_END_MS - QUEUE_START_MS))
echo "Queued campaign $CAMPAIGN_ID in ${QUEUE_MS}ms"

WORKER_START_MS="$(now_ms)"
DEADLINE=$((SECONDS + TIMEOUT_SEC))

while true; do
  if [ "$SECONDS" -ge "$DEADLINE" ]; then
    echo "Timed out after ${TIMEOUT_SEC}s"
    curl -fsS "$API/campaigns/$CAMPAIGN_ID/progress" || true
    exit 1
  fi

  PROGRESS_JSON="$(curl -fsS "$API/campaigns/$CAMPAIGN_ID/progress")"
  TOTAL="$(read_progress_field "$PROGRESS_JSON" "total_jobs")"
  COMPLETED="$(read_progress_field "$PROGRESS_JSON" "completed_jobs")"
  FAILED="$(read_progress_field "$PROGRESS_JSON" "failed_jobs")"
  QUEUED="$(read_progress_field "$PROGRESS_JSON" "queued_jobs")"
  RUNNING="$(read_progress_field "$PROGRESS_JSON" "running_jobs")"

  TOTAL="${TOTAL:-0}"
  COMPLETED="${COMPLETED:-0}"
  FAILED="${FAILED:-0}"
  QUEUED="${QUEUED:-0}"
  RUNNING="${RUNNING:-0}"

  DONE=$((COMPLETED + FAILED))
  echo "queued=$QUEUED running=$RUNNING completed=$COMPLETED failed=$FAILED total=$TOTAL"

  if [ "$TOTAL" -gt 0 ] && [ "$DONE" -ge "$TOTAL" ]; then
    break
  fi

  sleep 0.25
done

WORKER_END_MS="$(now_ms)"
TOTAL_END_MS="$(now_ms)"

WORKER_MS=$((WORKER_END_MS - WORKER_START_MS))
TOTAL_MS=$((TOTAL_END_MS - TOTAL_START_MS))

echo
echo "== Results =="
echo "Jobs:        $RUNS"
echo "Collectors:  $COLLECTORS"
echo "Queue time:  ${QUEUE_MS}ms"
echo "Worker time: ${WORKER_MS}ms"
echo "Total time:  ${TOTAL_MS}ms"

awk "BEGIN { if ($QUEUE_MS > 0) printf \"Queue rate:  %.0f jobs/sec\n\", $RUNS / ($QUEUE_MS / 1000); else print \"Queue rate:  n/a\" }"
awk "BEGIN { if ($WORKER_MS > 0) printf \"Worker rate: %.0f jobs/sec\n\", $RUNS / ($WORKER_MS / 1000); else print \"Worker rate: n/a\" }"
awk "BEGIN { if ($TOTAL_MS > 0) printf \"Total rate:  %.0f jobs/sec\n\", $RUNS / ($TOTAL_MS / 1000); else print \"Total rate:  n/a\" }"
