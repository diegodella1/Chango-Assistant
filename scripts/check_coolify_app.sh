#!/usr/bin/env bash
set -euo pipefail

APP_UUID="${1:?app uuid required}"
COOLIFY_URL="${COOLIFY_URL:-http://127.0.0.1:8000}"

TOKEN_PLAIN="$(openssl rand -hex 32)"
TOKEN_HASH="$(printf %s "$TOKEN_PLAIN" | sha256sum | cut -d' ' -f1)"
SQL="INSERT INTO personal_access_tokens (tokenable_type, tokenable_id, name, token, abilities, team_id, created_at, updated_at) VALUES ('App\\\\Models\\\\User', 0, 'codex-api', '${TOKEN_HASH}', '[\"*\"]', 0, now(), now()) RETURNING id;"

TOKEN_ID="$(
  docker exec coolify-db psql -U coolify -d coolify -t -A -c "$SQL" \
  | grep -Eo '^[0-9]+' \
  | head -n1
)"

cleanup() {
  docker exec coolify-db psql -U coolify -d coolify -c \
    "DELETE FROM personal_access_tokens WHERE id = ${TOKEN_ID};" >/dev/null || true
}
trap cleanup EXIT

curl -sS -w "\nHTTP:%{http_code}\n" \
  -H "Authorization: Bearer ${TOKEN_ID}|${TOKEN_PLAIN}" \
  -H "Accept: application/json" \
  "${COOLIFY_URL}/api/v1/deployments/applications/${APP_UUID}"
