#!/usr/bin/env bash
set -euo pipefail

API="${API:-http://localhost:8090}"

echo "== Atlas E2E test =="

echo "Checking health..."
curl -fsS "$API/health" | grep -q "ok"

echo "Fetching metrics..."
curl -fsS "$API/metrics" | grep -q "campaigns"
curl -fsS "$API/metrics/prometheus" | grep -q "atlas_campaigns"

echo "Reading CT ingestor config..."
curl -fsS "$API/ct/config" | grep -q "target_tlds"

echo "Reading CT status..."
curl -fsS "$API/ct/status" | grep -q "certificate_count"

echo "Seeding domain for enrichment..."
SEED_RESPONSE="$(curl -fsS -X POST "$API/domains" \
  -H "Content-Type: application/json" \
  -d '{"domains": ["example.com"], "collectors": ["dns", "rdap"]}')"
echo "$SEED_RESPONSE"
echo "$SEED_RESPONSE" | grep -q "jobs_queued"

echo "Creating discovery campaign..."
CAMPAIGN_RESPONSE="$(curl -fsS -X POST "$API/campaigns" \
  -H "Content-Type: application/json" \
  -d '{
    "seeds": ["example.com"],
    "collectors": ["dns", "ct"],
    "limits": { "max_depth": 1, "max_entities": 100 }
  }')"
echo "$CAMPAIGN_RESPONSE"

CAMPAIGN_ID="$(echo "$CAMPAIGN_RESPONSE" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')"
if [ -z "$CAMPAIGN_ID" ]; then
  echo "Failed to extract campaign id"
  exit 1
fi
echo "Campaign ID: $CAMPAIGN_ID"

echo "Polling campaign progress..."
for i in $(seq 1 30); do
  PROGRESS="$(curl -fsS "$API/campaigns/$CAMPAIGN_ID/progress")"
  STATUS="$(echo "$PROGRESS" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
  COMPLETED="$(echo "$PROGRESS" | sed -n 's/.*"completed_jobs":\([0-9]*\).*/\1/p')"
  TOTAL="$(echo "$PROGRESS" | sed -n 's/.*"total_jobs":\([0-9]*\).*/\1/p')"
  echo "status=$STATUS completed=$COMPLETED total=$TOTAL"

  if [ -n "$TOTAL" ] && [ "$TOTAL" -gt 0 ] && [ "$COMPLETED" = "$TOTAL" ]; then
    break
  fi
  if [ "$STATUS" = "completed" ] || [ "$STATUS" = "completed_with_errors" ]; then
    break
  fi
  sleep 2
done

echo "Fetching campaign report..."
curl -fsS "$API/campaigns/$CAMPAIGN_ID/report" | grep -q "example.com"

echo "Listing campaigns..."
curl -fsS "$API/campaigns?status=active&limit=5" | grep -q "campaigns"

echo "PASS"
