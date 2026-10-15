#!/usr/bin/env bash
# Boots the local Canton the examples are reproduced against and prints the
# allocated party ids as exports, also kept in examples/.env.
#
#   source examples/up.sh
#   ./examples/up.sh down
#
# Needs java 17+, curl and python3; dpm too when the DAR has to be built.
# CANTON_JAR overrides the jar from the DPM cache.

# Sourced from bash or zsh: run as a child and apply only the exports, so none
# of the script's options or functions leak into the interactive shell.
if [ -n "${ZSH_VERSION:-}" ]; then
  case "${ZSH_EVAL_CONTEXT:-}" in *:file*) _up_self="$0" ;; esac
elif [ -n "${BASH_VERSION:-}" ] && [ "${BASH_SOURCE[0]}" != "$0" ]; then
  _up_self="${BASH_SOURCE[0]}"
fi
if [ -n "${_up_self:-}" ]; then
  _up_out="$(bash "$_up_self" "$@")" && eval "$_up_out"
  _up_rc=$?
  unset _up_self _up_out
  return "$_up_rc"
fi

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE="$HERE/.up"
PID_FILE="$STATE/canton.pid"
LOG="$STATE/canton.log"
ENV_FILE="$HERE/.env"
DAR="$HERE/asset/.daml/dist/asset-tests-1.0.0.dar"
LEDGER="http://127.0.0.1:6113"
LEDGER2="http://127.0.0.1:6123"
TIMEOUT="${UP_TIMEOUT:-180}"

die() { echo "error: $*" >&2; exit 2; }

running_pid() {
  [[ -f "$PID_FILE" ]] || return 1
  local pid
  pid="$(cat "$PID_FILE")"
  kill -0 "$pid" 2>/dev/null || return 1
  printf '%s' "$pid"
}

down() {
  local pid
  if ! pid="$(running_pid)"; then
    echo "Canton is not running" >&2
    rm -f "$PID_FILE" "$ENV_FILE"
    return 0
  fi
  kill "$pid"
  for _ in $(seq 1 30); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 1
  done
  kill -9 "$pid" 2>/dev/null || true
  # A java shim that forks rather than execs leaves the JVM behind.
  pkill -f -- "-c $HERE/devnet-trace.conf" 2>/dev/null || true
  rm -f "$PID_FILE" "$ENV_FILE"
  echo "stopped Canton (pid $pid)" >&2
}

find_jar() {
  if [[ -n "${CANTON_JAR:-}" ]]; then
    [[ -f "$CANTON_JAR" ]] || die "CANTON_JAR does not exist: $CANTON_JAR"
    printf '%s' "$CANTON_JAR"
    return
  fi
  local sdk jar
  sdk="$(sed -n 's/^sdk-version: *//p' "$HERE/asset/daml.yaml")"
  jar="${DPM_HOME:-$HOME/.dpm}/cache/components/canton-open-source/$sdk/lib/canton-open-source-$sdk.jar"
  [[ -f "$jar" ]] || die "no Canton $sdk jar at $jar; run \`dpm install $sdk\` or set CANTON_JAR"
  printf '%s' "$jar"
}

check_java() {
  command -v java >/dev/null 2>&1 || die "java not found on PATH (Java 17 or newer is required)"
  local major
  major="$(java -version 2>&1 | sed -n 's/.* version "\([0-9]*\).*/\1/p' | head -1)"
  [[ -n "$major" && "$major" -ge 17 ]] || die "Java 17 or newer is required (found: ${major:-unknown})"
}

party() {
  curl -sf "$1/v2/parties" | python3 -c '
import sys, json
hint = sys.argv[1] + "::"
print(next((p["party"] for p in json.load(sys.stdin).get("partyDetails", []) if p["party"].startswith(hint)), ""))
' "$2"
}

up() {
  local pid
  if pid="$(running_pid)"; then
    die "Canton is already running (pid $pid); stop it with: $0 down"
  fi
  check_java
  command -v curl >/dev/null 2>&1 || die "curl not found on PATH"
  command -v python3 >/dev/null 2>&1 || die "python3 not found on PATH"
  local jar
  jar="$(find_jar)"
  if curl -sf "$LEDGER/v2/version" >/dev/null 2>&1; then
    die "something is already listening on $LEDGER"
  fi

  if [[ ! -f "$DAR" ]]; then
    command -v dpm >/dev/null 2>&1 || die "the example DAR is missing and dpm is not on PATH to build it"
    echo "building the example DAR ..." >&2
    (cd "$HERE/asset" && dpm build >/dev/null)
  fi

  mkdir -p "$STATE"
  rm -f "$ENV_FILE"
  # Canton writes log/ and reference.conf into its working directory.
  (cd "$STATE" && exec java -Ddpm.dar-path="$DAR" -jar "$jar" daemon \
    -c "$HERE/devnet-trace.conf" \
    --bootstrap "$HERE/devnet-trace.bootstrap.canton" --no-tty \
    </dev/null >"$LOG" 2>&1) &
  local pid=$!
  echo "$pid" >"$PID_FILE"
  echo "starting Canton (pid $pid, log $LOG) ..." >&2

  local waited=0
  until grep -q "dpm-trace examples ready" "$LOG" 2>/dev/null; do
    if ! kill -0 "$pid" 2>/dev/null; then
      rm -f "$PID_FILE"
      tail -n 40 "$LOG" >&2
      die "Canton exited before it was ready; full log: $LOG"
    fi
    if (( waited >= TIMEOUT )); then
      tail -n 40 "$LOG" >&2
      down >/dev/null
      die "Canton was not ready after ${TIMEOUT}s (set UP_TIMEOUT to wait longer); full log: $LOG"
    fi
    sleep 1
    waited=$((waited + 1))
  done

  local issuer="" alice="" bob=""
  for _ in $(seq 1 30); do
    issuer="$(party "$LEDGER" Issuer)"
    alice="$(party "$LEDGER" Alice)"
    bob="$(party "$LEDGER2" Bob)"
    [[ -n "$issuer" && -n "$alice" && -n "$bob" ]] && break
    sleep 1
  done
  [[ -n "$issuer" && -n "$alice" && -n "$bob" ]] \
    || die "Canton is up but the Issuer, Alice and Bob parties were not found"

  cat >"$ENV_FILE" <<EOF
export LEDGER='$LEDGER'
export LEDGER2='$LEDGER2'
export ISSUER='$issuer'
export ALICE='$alice'
export BOB='$bob'
EOF
  cat "$ENV_FILE"

  cat >&2 <<EOF
Canton is ready: participant1 $LEDGER, participant2 $LEDGER2
To use in another shell run: source examples/.env
Stop Canton with: $0 down
EOF
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  *) echo "usage: $0 [up|down]" >&2; exit 2 ;;
esac
