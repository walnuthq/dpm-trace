# Real Update Smoke Test

Checklist for exercising `dpm trace` against a real local Canton participant.
Replace `<path-to-canton.jar>` with a path from your workspace; do not commit
concrete local values.

## Prerequisites

- A Java runtime and a Canton jar as `<path-to-canton.jar>`. A DPM install has
  one under `~/.dpm/cache/components/canton-open-source/<version>/lib/`.
- A Daml toolchain, to build the example DAR.

## Boot

```bash
(cd examples/asset && daml build)

java -Ddpm.dar-path="$PWD/examples/asset/.daml/dist/asset-tests-1.0.0.dar" \
  -jar <path-to-canton.jar> daemon \
  -c examples/devnet-trace.conf \
  --bootstrap examples/devnet-trace.bootstrap.canton --no-tty
```

The bootstrap uploads the DAR, allocates the parties, and prints the ready
banner. Read the party ids off the participant:

```bash
export LEDGER=http://127.0.0.1:6113
curl -s $LEDGER/v2/parties | python3 -m json.tool | grep '"party"'
export ISSUER='Issuer::1220...'
export ALICE='Alice::1220...'
```

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
pkill -f 'canton-open-source-.*\.jar'
```

`java` can be a shim, so killing the shell job may leave the JVM holding the
ports; match on the jar.
