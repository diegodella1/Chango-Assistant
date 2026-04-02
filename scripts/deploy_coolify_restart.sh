#!/usr/bin/env bash
set -euo pipefail

APP_UUID="${1:-vk4goko0koc8k4c48sckwsk8}"
COOLIFY_URL="${COOLIFY_URL:-http://127.0.0.1:8000}"
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:18790/health}"
REPRESENTATIVE_URL="${REPRESENTATIVE_URL:-http://127.0.0.1:18790/}"
EXPECTED_PORT="${EXPECTED_PORT:-18790}"

TOKEN_PLAIN="$(openssl rand -hex 32)"
TOKEN_HASH="$(printf %s "$TOKEN_PLAIN" | sha256sum | cut -d' ' -f1)"

TOKEN_ID="$(
  docker exec coolify-db psql -U coolify -d coolify -t -A -c \
  "INSERT INTO personal_access_tokens (tokenable_type, tokenable_id, name, token, abilities, team_id, created_at, updated_at) VALUES ('App\\Models\\User', 0, 'codex-deploy', '${TOKEN_HASH}', '[\"*\"]', 0, now(), now()) RETURNING id;" \
  | grep -Eo '^[0-9]+' \
  | head -n1
)"

if [[ -z "${TOKEN_ID}" ]]; then
  echo "ERROR: failed to create temp token in coolify-db" >&2
  exit 1
fi

cleanup() {
  docker exec coolify-db psql -U coolify -d coolify -c \
    "DELETE FROM personal_access_tokens WHERE id = ${TOKEN_ID};" >/dev/null || true
}
trap cleanup EXIT

response="$(
curl -sS -X POST "${COOLIFY_URL}/api/v1/applications/${APP_UUID}/restart" \
  -H "Authorization: Bearer ${TOKEN_ID}|${TOKEN_PLAIN}" \
  -H "Accept: application/json"
)"

echo "${response}"

deployment_uuid="$(printf '%s' "${response}" | grep -Eo '"deployment_uuid"\s*:\s*"[^"]+"' | head -n1 | cut -d'"' -f4)"

if [[ -n "${deployment_uuid}" ]]; then
  echo "Waiting for deployment ${deployment_uuid} to finish..."
  for _ in $(seq 1 24); do
    deploy_status="$(scripts/check_coolify_deploy.sh "${deployment_uuid}" 2>/dev/null || true)"
    if printf '%s' "${deploy_status}" | grep -q '"status"\s*:\s*"finished"'; then
      break
    fi
    if printf '%s' "${deploy_status}" | grep -q '"status"\s*:\s*"failed"'; then
      echo "ERROR: deployment ${deployment_uuid} failed" >&2
      printf '%s\n' "${deploy_status}" >&2
      exit 1
    fi
    sleep 15
  done
fi

echo "Running smoke checks..."
if ! curl -fsS "${HEALTH_URL}" >/dev/null; then
  echo "ERROR: health check failed at ${HEALTH_URL}" >&2
  exit 1
fi

if ! ss -ltn "( sport = :${EXPECTED_PORT} )" | grep -q ":${EXPECTED_PORT}"; then
  echo "ERROR: expected port ${EXPECTED_PORT} is not listening" >&2
  exit 1
fi

if ! curl -fsS "${REPRESENTATIVE_URL}" >/dev/null; then
  echo "ERROR: representative route failed at ${REPRESENTATIVE_URL}" >&2
  exit 1
fi

echo "Deploy smoke checks passed."
