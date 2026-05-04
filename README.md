# dpm trace POC

Small DPM component that adds a `dpm trace` command for Canton transaction/update inspection.

This is a POC for three flows:

- `trace`: inspect an already committed update.
- `simulate`: re-simulate a committed update without submitting anything.
- `bundle` / `replay --interactive`: capture participant-visible replay context and open an interactive debugger.

## Setup

```bash
cd dpm-trace
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
  "darPaths": ["./path/to/app.dar"],
  "debugInfoPaths": ["./path/to/app.debug-info.json"]
}
```

Run commands through the local DPM home:

```bash
dpm trace --help
```

## Trace

Inspect an already committed update:

```bash
dpm trace <update-id>
```

With explicit participant context:

```bash
dpm trace <update-id> \
  --ledger-url http://localhost:<json-ledger-api-port> \
  --read-as '<party-id>'
```

Useful options:

```bash
--interactive              open event-by-event trace inspector
--print-json               print normalized trace JSON
--debug-info FILE          attach source/debug metadata
--dar FILE                 attach local DAR metadata
--color always|never|auto  control ANSI colors
```

Examples:

```bash
dpm trace <update-id> --interactive

dpm trace <update-id> \
  --debug-info ./path/to/app.debug-info.json
```

## Simulate

Run a non-committing prepare call from a committed update:

```bash
dpm trace simulate <update-id>
```

Override reconstructed command arguments:

```bash
dpm trace simulate <update-id> \
  --override amount=1000
```

Explicit command preparation is also supported:

```bash
dpm trace simulate \
  --ledger-url http://localhost:<json-ledger-api-port> \
  --act-as '<party-id>' \
  --template '<package-id>:Counter:Counter' \
  --arg owner='<party-id>' \
  --arg count=0
```

`simulate` calls Canton’s non-committing prepare API. It does not submit to the ledger.

## Interactive Debugger

Capture replay/debug context:

```bash
dpm trace bundle <update-id> \
  --out ./counter.bundle.json
```

Replay the bundle:

```bash
dpm trace replay ./counter.bundle.json
```

Open the interactive debugger:

```bash
dpm trace replay ./counter.bundle.json \
  --interactive \
  --debug-info ./path/to/app.debug-info.json
```

Useful REPL commands:

```text
n / next     next event
p / prev     previous event
s / source   show source snippet
expr         list source expression steps
si           step into source expression
vars         show visible variables
b <spec>     set breakpoint: event id, template.choice, or file:line
bp           list breakpoints
c            continue to next breakpoint
tree         show transaction tree
json         print current event JSON
q            quit
```

Breakpoint examples:

```text
b Counter:Counter.Increment
b Counter.daml:15
b 0
c
```

## Notes

- Output is participant-scoped. It is not a global Canton transaction.
- Source-line support requires matching debug-info metadata.
- `trace --interactive` steps through committed transaction events.
- `replay --interactive` can inspect captured replay context such as ACS/pre-state and input contracts.
