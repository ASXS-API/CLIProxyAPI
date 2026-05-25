#!/usr/bin/env bash

set -euo pipefail

API_PATH="/v0/management/upstream-response-header-timeout"
CONTAINER_PORT="${CPA_CONTAINER_PORT:-8317}"
HOST="${CPA_HOST:-127.0.0.1}"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

json_scalar() {
  local value="$1"
  if [[ "$value" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
    printf '%s' "$value"
    return
  fi
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '"%s"' "$value"
}

normalize_bool() {
  local value
  value="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  case "$value" in
    true|t|yes|y|1)
      printf 'true'
      ;;
    false|f|no|n|0)
      printf 'false'
      ;;
    *)
      echo "invalid enabled value: $1 (use true/false)" >&2
      exit 1
      ;;
  esac
}

compose_services() {
  docker compose ps --services | grep -E '^cli-proxy-api' || true
}

host_port_for_service() {
  local service="$1"
  local cid
  cid="$(docker compose ps -q "$service" 2>/dev/null | head -n 1)"
  if [[ -z "$cid" ]]; then
    return 1
  fi
  docker port "$cid" "${CONTAINER_PORT}/tcp" 2>/dev/null |
    sed -nE 's/.*:([0-9]+)$/\1/p' |
    head -n 1
}

call_endpoint() {
  local service="$1"
  local port="$2"
  local payload="$3"
  local key="$4"
  local url="http://${HOST}:${port}${API_PATH}"
  local response status body

  echo "[$service] PUT $url"
  if ! response="$(curl -sS -w $'\n%{http_code}' -X PUT "$url" \
    -H "Authorization: Bearer ${key}" \
    -H "Content-Type: application/json" \
    -d "$payload")"; then
    echo "  request failed"
    return 1
  fi

  status="${response##*$'\n'}"
  body="${response%$'\n'"$status"}"
  if [[ "$status" =~ ^2 ]]; then
    echo "  ok: $body"
  else
    echo "  failed: HTTP $status $body"
    return 1
  fi

  if response="$(curl -sS "$url" -H "Authorization: Bearer ${key}")"; then
    echo "  current: $response"
  fi
}

require_command docker
require_command curl

echo "docker compose ps:"
docker compose ps
echo

mapfile -t services < <(compose_services)
if [[ "${#services[@]}" -eq 0 ]]; then
  echo "no cli-proxy-api services found from docker compose ps --services" >&2
  exit 1
fi

declare -a targets=()
for service in "${services[@]}"; do
  port="$(host_port_for_service "$service" || true)"
  if [[ -z "$port" ]]; then
    echo "skip $service: no host port mapped to container port ${CONTAINER_PORT}/tcp" >&2
    continue
  fi
  targets+=("${service}:${port}")
done

if [[ "${#targets[@]}" -eq 0 ]]; then
  echo "no callable cli-proxy-api management endpoints found" >&2
  exit 1
fi

echo "detected endpoints:"
for target in "${targets[@]}"; do
  echo "  ${target%%:*}: http://${HOST}:${target#*:}${API_PATH}"
done
echo

read -r -s -p "management key: " key
echo
if [[ -z "$key" ]]; then
  echo "management key is required" >&2
  exit 1
fi

read -r -p "enabled [true]: " enabled
enabled="${enabled:-true}"
enabled="$(normalize_bool "$enabled")"

read -r -p "initial seconds/duration [10]: " initial
initial="${initial:-10}"

read -r -p "max seconds/duration [80]: " max
max="${max:-80}"

payload='{"value":{"enabled":'"${enabled}"',"initial":'"$(json_scalar "$initial")"',"max":'"$(json_scalar "$max")"'}}'

echo
echo "payload: $payload"
echo

failed=0
for target in "${targets[@]}"; do
  service="${target%%:*}"
  port="${target#*:}"
  if ! call_endpoint "$service" "$port" "$payload" "$key"; then
    failed=1
  fi
done

exit "$failed"
