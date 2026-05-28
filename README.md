# dpm trace

DPM component POC for participant-scoped Canton transaction visualization.

It demonstrates the proposal surface:

- `trace`: inspect an already committed update.
- `open`: reopen an exported trace artifact.
- `prepare`: prepare a command without committing it.
- `compare`: compare prepared results, committed updates, or captured completions.

## Setup

```bash
.venv/bin/python -m pip install -e .
./scripts/install-local-dpm-trace.sh
```

Optional local config:

```bash
cp .dpm-trace.example.json .dpm-trace.json
```

Example config:

```json
{
  "ledgerUrl": "http://localhost:<json-ledger-api-port>",
  "readAs": "<party-id>",
  "darPaths": ["./path/to/app.dar"]
}
```

## Trace

Inspect an already committed update:

```bash
dpm trace <update-id>
```

With explicit participant context:

```bash
dpm trace <update-id> \
  --participant-url http://localhost:<json-ledger-api-port> \
  --read-as '<party-id>' \
  --access-token-file ./token.txt
```

Export a trace artifact:

```bash
dpm trace <update-id> --export trace.json
```

Open the interactive transaction visualizer:

```bash
dpm trace <update-id> --visualize
```

## Open

Reopen an exported trace artifact:

```bash
dpm trace open trace.json
dpm trace open trace.json --visualize
```

## Prepare

Prepare a command without committing it:

```bash
dpm trace prepare \
  --participant-url http://localhost:<json-ledger-api-port> \
  --act-as '<party-id>' \
  --template '<package-id>:Counter:Counter' \
  --arg owner='<party-id>' \
  --arg count=0 \
  --export prepared.json
```

Or pass a command file:

```bash
dpm trace prepare \
  --participant-url http://localhost:<json-ledger-api-port> \
  --act-as '<party-id>' \
  --commands commands.json \
  --export prepared.json
```

`prepare` calls Canton's non-committing prepare API. It does not submit to the ledger.

## Compare

Compare a prepared command with a committed update:

```bash
dpm trace compare \
  --prepared prepared.json \
  --update <update-id> \
  --participant-url http://localhost:<json-ledger-api-port> \
  --read-as '<party-id>'
```

Compare two committed updates:

```bash
dpm trace compare <update-id-a> <update-id-b> \
  --participant-url http://localhost:<json-ledger-api-port> \
  --read-as '<party-id>'
```

Compare a prepared command with a captured completion JSON:

```bash
dpm trace compare \
  --prepared prepared.json \
  --completion-file completion.json
```

## Notes

- Output is participant-scoped. It is not a global Canton transaction.
- Failed submissions may not have an update id. In that case comparison uses completion/error data and optional logs.
- Source-level debugging and compiler debug-info generation are out of scope for this PoC.
