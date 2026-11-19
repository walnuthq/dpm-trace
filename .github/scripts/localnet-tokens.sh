#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "--once" ]; then
  once=true
  shift
else
  once=false
fi

if [ "$#" -lt 3 ]; then
  echo "usage: localnet-tokens.sh [--once] TOKEN_DIR REFRESH_SECONDS SLOT..." >&2
  exit 2
fi

token_dir="$1"
refresh_seconds="$2"
shift 2
slots=("$@")

umask 077
mkdir -p "$token_dir"

write_token() {
  local slot="$1" tmp
  tmp="$(mktemp "$token_dir/.${slot}.XXXXXX")"
  if ! canton-localnet auth token --slot "$slot" >"$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  mv "$tmp" "$token_dir/${slot}.token"
}

if [ "$once" = true ]; then
  for slot in "${slots[@]}"; do
    write_token "$slot"
    echo "minted token for slot ${slot}" >&2
  done
  exit 0
fi

while true; do
  sleep "$refresh_seconds"
  for slot in "${slots[@]}"; do
    if write_token "$slot"; then
      echo "refreshed token for slot ${slot}" >&2
    else
      echo "::warning::token refresh failed for slot ${slot}; keeping the previous token" >&2
    fi
  done
done
