#!/usr/bin/env bash
set -euo pipefail

ROOT="${1:?repo root is required}"
CANTON_ROOT="$(cd "$ROOT/.." && pwd)"
DAML_PROJECT="$CANTON_ROOT/daml-examples/daml-3x"
DAR="$DAML_PROJECT/.daml/dist/counter-example-1.0.0.dar"
DEPLOY_SCRIPT="$CANTON_ROOT/daml-examples/canton-config/counter-deploy-3x.canton"

HOME_DIR="${HOME:-}"
DAML="${DPM_TRACE_DAML:-$HOME_DIR/.daml/sdk/3.4.11/daml/daml}"
CANTON_JAR="${DPM_TRACE_CANTON_JAR:-$HOME_DIR/.daml/sdk/3.4.11/canton/canton.jar}"
HELPER="${DPM_TRACE_DAML_HELPER:-$CANTON_ROOT/daml/sdk/bazel-bin/daml-assistant/daml-helper/daml-helper}"
PYTHON="${DPM_TRACE_PYTHON:-python3}"

if [[ ! -x "$DAML" ]]; then
  echo "missing Daml assistant: $DAML" >&2
  exit 2
fi
if [[ ! -f "$CANTON_JAR" ]]; then
  echo "missing Canton jar: $CANTON_JAR" >&2
  exit 2
fi
if [[ ! -x "$HELPER" ]]; then
  echo "missing daml-helper: $HELPER" >&2
  exit 2
fi

TMPDIR="$(mktemp -d "${TMPDIR:-/tmp}/dpm-trace-real-canton.XXXXXX")"
LOG="$TMPDIR/canton.log"
CANTON_PID=""

read -r SEQUENCER_PUBLIC SEQUENCER_ADMIN MEDIATOR_ADMIN P1_LEDGER P1_ADMIN P1_HTTP P2_LEDGER P2_ADMIN P2_HTTP < <("$PYTHON" - <<'PY'
import socket

sockets = []
ports = []
for _ in range(9):
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    sockets.append(sock)
    ports.append(sock.getsockname()[1])
print(" ".join(map(str, ports)))
PY
)
CONFIG="$TMPDIR/devnet-trace-poc.conf"

cat >"$CONFIG" <<EOF
canton {
  sequencers {
    sequencer1 {
      storage.type = memory
      public-api.port = $SEQUENCER_PUBLIC
      admin-api.port = $SEQUENCER_ADMIN
      sequencer.type = BFT
    }
  }

  mediators {
    mediator1 {
      storage.type = memory
      admin-api.port = $MEDIATOR_ADMIN
    }
  }

  participants {
    participant1 {
      storage.type = memory
      admin-api.port = $P1_ADMIN
      ledger-api.port = $P1_LEDGER
      http-ledger-api.port = $P1_HTTP
    }

    participant2 {
      storage.type = memory
      admin-api.port = $P2_ADMIN
      ledger-api.port = $P2_LEDGER
      http-ledger-api.port = $P2_HTTP
    }
  }
}
EOF

cleanup() {
  if [[ -n "$CANTON_PID" ]] && kill -0 "$CANTON_PID" 2>/dev/null; then
    kill "$CANTON_PID" 2>/dev/null || true
    wait "$CANTON_PID" 2>/dev/null || true
  fi
  rm -rf "$TMPDIR"
}
trap cleanup EXIT

(cd "$DAML_PROJECT" && "$DAML" build >/dev/null)

java \
  -Dcounter.dar-path="$DAR" \
  -jar "$CANTON_JAR" \
  daemon \
  -c "$CONFIG" \
  --bootstrap "$DEPLOY_SCRIPT" \
  --no-tty \
  >"$LOG" 2>&1 &
CANTON_PID="$!"

ALICE=""
for _ in $(seq 1 90); do
  if "$HELPER" ledger list-parties --host localhost --port "$P1_LEDGER" --json >"$TMPDIR/parties.json" 2>/dev/null; then
    ALICE="$("$PYTHON" -c '
import json, sys
try:
    data = json.load(open(sys.argv[1]))
except Exception:
    raise SystemExit(1)
for item in data:
    party = item.get("party", "")
    if party.startswith("Alice::"):
        print(party)
        raise SystemExit(0)
raise SystemExit(1)
' "$TMPDIR/parties.json" 2>/dev/null || true)"
    if [[ -n "$ALICE" ]]; then
      break
    fi
  fi
  if ! kill -0 "$CANTON_PID" 2>/dev/null; then
    cat "$LOG" >&2
    exit 1
  fi
  sleep 1
done

if [[ -z "$ALICE" ]]; then
  cat "$LOG" >&2
  echo "Alice party not found" >&2
  exit 1
fi

for _ in $(seq 1 60); do
  if "$HELPER" ledger submit create '#counter-example:FailureDemo:GuardedAccount' \
    --arg owner="$ALICE" \
    --arg balance=100 \
    --host localhost \
    --port "$P1_LEDGER" \
    --act-as "$ALICE" \
    --user-id participant_admin \
    --dar "$DAR" \
    --json >"$TMPDIR/create.json" 2>"$TMPDIR/create.err"; then
    break
  fi
  if grep -q "UNKNOWN_SUBMITTERS" "$TMPDIR/create.err"; then
    sleep 1
    continue
  fi
  cat "$TMPDIR/create.err" >&2
  exit 1
done

if [[ ! -s "$TMPDIR/create.json" ]]; then
  cat "$TMPDIR/create.err" >&2
  echo "failed to create GuardedAccount" >&2
  exit 1
fi

CONTRACT_ID="$("$PYTHON" -c '
import json, sys
def walk(value):
    if isinstance(value, dict):
        cid = value.get("contractId") or value.get("contract_id")
        if cid:
            return cid
        for item in value.values():
            found = walk(item)
            if found:
                return found
    if isinstance(value, list):
        for item in value:
            found = walk(item)
            if found:
                return found
    return None
cid = walk(json.load(open(sys.argv[1])))
if not cid:
    raise SystemExit("contract id not found")
print(cid)
' "$TMPDIR/create.json")"

COMMAND_ID="dpm-trace-real-failed-${RANDOM}-${RANDOM}"
set +e
"$HELPER" ledger submit exercise '#counter-example:FailureDemo:GuardedAccount' \
  --contract-id "$CONTRACT_ID" \
  --choice Withdraw \
  --arg amount=250 \
  --host localhost \
  --port "$P1_LEDGER" \
  --act-as "$ALICE" \
  --user-id participant_admin \
  --command-id "$COMMAND_ID" \
  --dar "$DAR" \
  --json >"$TMPDIR/fail-submit.out" 2>"$TMPDIR/fail-submit.err"
SUBMIT_STATUS=$?
set -e

if PYTHONPATH="$ROOT/src" "$PYTHON" -m dpm_trace.cli \
  --command-id "$COMMAND_ID" \
  --submitter "http://127.0.0.1:$P1_HTTP" \
  --act-as "$ALICE" \
  --completion-user-id participant_admin \
  --completion-limit 1000 \
  --completion-timeout-ms 5000 \
  --daml-yaml "$DAML_PROJECT/daml.yaml" \
  --dar "$DAR" \
  --damlc "$DAML" \
  --color never >"$TMPDIR/trace-from-completion.out" 2>"$TMPDIR/trace-from-completion.err"; then
  cat "$TMPDIR/trace-from-completion.out"
  exit 0
fi

COMMAND_ID="$COMMAND_ID" SUBMIT_STATUS="$SUBMIT_STATUS" FAIL_OUT="$TMPDIR/fail-submit.out" FAIL_ERR="$TMPDIR/fail-submit.err" "$PYTHON" - <<'PY' >"$TMPDIR/captured-failure.json"
import json
import os
import re
import sys
from pathlib import Path

text = Path(os.environ["FAIL_OUT"]).read_text(errors="replace") + "\n" + Path(os.environ["FAIL_ERR"]).read_text(errors="replace")
message = "Insufficient balance" if "Insufficient balance" in text else text.strip().splitlines()[-1]
code = "FAILED_PRECONDITION"
match = re.search(r"\b([A-Z_]+)\(", text)
if match:
    code = match.group(1)
json.dump(
    {
        "commandId": os.environ["COMMAND_ID"],
        "submissionId": os.environ["COMMAND_ID"],
        "source": "captured-submit-error",
        "status": {"code": code, "message": message},
        "submitExitCode": int(os.environ["SUBMIT_STATUS"]),
    },
    fp=sys.stdout,
    indent=2,
)
PY

PYTHONPATH="$ROOT/src" "$PYTHON" -m dpm_trace.cli \
  --completion-file "$TMPDIR/captured-failure.json" \
  --daml-yaml "$DAML_PROJECT/daml.yaml" \
  --dar "$DAR" \
  --damlc "$DAML" \
  --color never
