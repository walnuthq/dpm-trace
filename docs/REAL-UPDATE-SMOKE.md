# Real Update Smoke Test

Checklist for exercising `dpm trace` against a real local Canton participant.
Do not commit concrete local values from your workspace.

## Prerequisites

- DPM, which brings both the Canton jar and the Daml toolchain. Install it with
  `curl https://get.digitalasset.com/install/install.sh | sh`, then
  `dpm install 3.5.1`.
- Java 17 or newer, to run Canton.

## Boot

```bash
source examples/up.sh
```

This boots Canton in the background, uploads the DAR, allocates the parties and
exports `LEDGER`, `ISSUER`, `ALICE` and `BOB`.

## Smoke

Run these from a directory without a `.dpm-trace.json`, or pass `--config`:
discovery walks up from the working directory, and a placeholder party from one
reaches the ledger as `non expected character 0x2e in Daml-LF Party`.

```bash
# committed update
UPDATE_ID=$(dpm trace submit --submitter "$LEDGER" --act-as "$ISSUER" \
  --template '#asset-tests:Asset:Asset' \
  --arg issuer="$ISSUER" --arg owner="$ALICE" --arg name=GOLD --arg quantity=100)
dpm trace "$UPDATE_ID" --submitter "$LEDGER" --read-as "$ISSUER"

# the same update as another party: a different projection
dpm trace "$UPDATE_ID" --submitter "$LEDGER" --read-as "$ALICE"

# artifact round-trip, with no ledger connection
dpm trace "$UPDATE_ID" --submitter "$LEDGER" --read-as "$ISSUER" --export /tmp/trace.json
dpm trace open /tmp/trace.json
```

Expected: a `CREATE Asset:Asset` with payload, signatories, observers and
witnesses, under a header labelling it a participant-visible projection.

For an archive, exercise a consuming choice — `Burn` is consuming and creates
nothing. It is controlled by the owner, and archives the contract, so each run
needs a fresh one:

```bash
CID=$(dpm trace "$UPDATE_ID" --submitter "$LEDGER" --read-as "$ISSUER" --print-json \
  | python3 -c 'import sys,json; d=json.load(sys.stdin); print(next(e["contractId"] for e in d["eventsById"].values() if e.get("contractId")))')
BURN_ID=$(dpm trace submit --submitter "$LEDGER" --act-as "$ALICE" \
  --template '#asset-tests:Asset:Asset' --choice Burn --contract-id "$CID")
dpm trace "$BURN_ID" --submitter "$LEDGER" --read-as "$ISSUER"
```

Expected: `EXERCISE Asset:Asset.Burn` with `consuming: true` and `x1 archive`.

## Teardown

```bash
./examples/up.sh down
```
