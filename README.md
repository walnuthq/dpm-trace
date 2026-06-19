# dpm trace

DPM component POC for participant-scoped Canton transaction visualization.

It demonstrates the proposal surface:

- `trace`: inspect a successful transaction by update id.
- `trace --command-id`: inspect a failed submission through completion data.
- `open`: reopen an exported trace artifact.
- `prepare`: prepare a command without committing it.
- `submit`: submit a command (submit-and-wait) and print the resulting update id.
- `compare`: compare prepared transactions, successful transactions, or completions.
- `test`: run Daml Script unit tests (unit mode) or an lit suite against a managed local Canton (integration mode).

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

Inspect a successful transaction:

```bash
dpm trace <update-id>
```

With explicit participant context:

```bash
dpm trace <update-id> \
  --submitter http://localhost:<json-ledger-api-port> \
  --read-as '<party-id>' \
  --access-token-file ./token.txt
```

The bearer token can also be passed with `--token`, `DPM_TRACE_TOKEN`, or `DPM_TRACE_TOKEN_FILE`.

Inspect a failed submission by command id:

```bash
dpm trace --command-id <command-id> \
  --submitter http://localhost:<json-ledger-api-port> \
  --act-as '<party-id>' \
  --log-file /tmp/canton-participant.log \
  --access-token-file ./token.txt
```

Or inspect captured completion JSON:

```bash
dpm trace --completion-file completion.json \
  --log-file /tmp/canton-participant.log
```

With a local Daml project and DAR available, failed completions can point back
to the contract line and column. When `--dar` is provided, `dpm trace` uses
`damlc inspect` to confirm the failure text exists in the compiled package
before resolving it against local sources.

```bash
dpm trace --completion-file completion.json \
  --daml-yaml <path-to-daml-project>/daml.yaml \
  --dar <path-to-daml-project>/.daml/dist/app.dar \
  --damlc daml
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
  --submitter http://localhost:<json-ledger-api-port> \
  --act-as '<party-id>' \
  --template '<package-id>:Counter:Counter' \
  --arg owner='<party-id>' \
  --arg count=0 \
  --export prepared.json
```

Or pass a command file:

```bash
dpm trace prepare \
  --submitter http://localhost:<json-ledger-api-port> \
  --act-as '<party-id>' \
  --commands commands.json \
  --export prepared.json
```

`prepare` calls Canton's non-committing prepare API. It does not submit to the ledger.

## Compare

Compare a prepared transaction with a successful transaction:

```bash
dpm trace compare \
  --prepared prepared.json \
  --update <update-id> \
  --submitter http://localhost:<json-ledger-api-port> \
  --read-as '<party-id>'
```

Compare a prepared transaction with a failed submission:

```bash
dpm trace compare \
  --prepared prepared.json \
  --command-id <command-id> \
  --submitter http://localhost:<json-ledger-api-port> \
  --act-as '<party-id>' \
  --log-file /tmp/canton-participant.log
```

Compare two successful transactions:

```bash
dpm trace compare <update-id-a> <update-id-b> \
  --submitter http://localhost:<json-ledger-api-port> \
  --read-as '<party-id>'
```

Compare a prepared transaction with a captured completion JSON:

```bash
dpm trace compare \
  --prepared prepared.json \
  --completion-file completion.json
```

## Test (Daml Script unit tests)

Run a package's Daml Script unit tests, render each script's transaction tree,
and map any failed test back to source. It wraps `daml test` on the in-memory
IDE ledger, so it needs **no Canton node** and runs locally in CI/CD.

```bash
dpm trace test <package-dir> --daml daml
```

`daml test` already runs the tests and gates CI by exit code. `dpm trace test`
adds what it does not:

- **Failure triage.** A red test is resolved to source and rendered with a caret
  — both the test call site *and* the contract invariant (`assertMsg` / `abort` /
  `ensure`) that rejected it. With `--dar`, the contract match is verified against
  the compiled package using `damlc inspect`, not just grepped from local files.
- **Transaction trees in the terminal and as JSON.** `daml test` only writes these
  to IDE-only HTML; here they appear inline and in `--print-json`.
- **A structured report** to build CI automation on (PR comments, custom gates).

### Usage

```bash
dpm trace test .                  # run all Script tests in the current package
dpm trace test . --no-trees       # summary + failures only (compact CI logs)
dpm trace test . --print-json     # machine-readable report (dpm-trace/test-report/v0)
dpm trace test . --junit out.xml  # also write JUnit XML for CI
dpm trace test . -p testSplit     # run a subset by test pattern
dpm trace test . --dar .daml/dist/<pkg>.dar   # damlc-inspect-verified failure mapping
```

`--daml` selects the toolchain (`daml`, `damlc`, or `dpm`) and defaults to `daml`.

### Output

A passing run renders each script's decoded transaction tree and a per-test summary:

```
DPM trace test
  package:  daml-tests
  command:  daml test
  result:   all 7 passed (7 total)

Results
  PASS  testTransfer         2 tx  +2 create  >1 exercise  x1 archive
  PASS  testSplit            2 tx  +3 create  >1 exercise  x1 archive
  PASS  testCannotIssueZero  1 tx  !1 expected-fail
  ...
```

A failing run pinpoints the source and returns a non-zero exit code. When a
contract guard rejects a submission, the report shows both where the test failed
and why:

```
  FAIL  testSplitContractGuard
        message: ... AssertionFailed: splitQuantity must be between 1 and quantity - 1 ...
        daml/Test.daml:14:8      basis: daml test: Test        (where the test failed)
        daml/Asset.daml:37:20    basis: damlc inspect: Asset   (the invariant that rejected it)
        > 37 |   assertMsg "splitQuantity must be between 1 and quantity - 1"
                          ^
```

### CI

The command exits non-zero on any failure, so a CI step is a single line:

```bash
dpm trace test . --dar .daml/dist/<pkg>.dar --junit results.xml --no-trees
```

A worked example — an Asset contract, a Daml Script test suite, a GitHub Actions
workflow, and a regression demo — lives in the sibling `daml-tests` package.

### Integration tests (managed Canton + lit)

Unit tests run on the in-memory IDE ledger. For integration tests against a
**real local Canton node**, point `test` at an `lit` suite with `--integration`.

Scaffold the suite once with `--init` (writes `itests/lit.cfg.py` and a sample
test into the package):

```bash
dpm trace test . --init
```

Then run it:

```bash
dpm trace test . --integration itests \
  --canton-jar "$DPM_TRACE_CANTON_JAR" \
  --daml daml
```

This builds the package DAR, boots an in-memory Canton on random ports, uploads
the DAR, allocates parties (default `Alice,Bob`), then runs `lit` over the test
directory against the live node and tears Canton down. One boot serves the whole
suite, and the lit exit code gates CI.

Connection details are exposed to tests as `lit` substitutions: `%dpm`
(the CLI), `%ledger` (the participant JSON Ledger API URL), `%alice`, `%bob`,
and `%dar`. A test submits against the live ledger and asserts on the trace:

```
# REQUIRES: canton
# RUN: ID=$(%dpm submit --submitter %ledger --act-as %alice \
# RUN:        --template '#asset-tests:Asset:Asset' \
# RUN:        --arg issuer=%alice --arg owner=%alice --arg name=GOLD --arg quantity=100) \
# RUN:   && %dpm trace "$ID" --submitter %ledger --read-as %alice --color never | FileCheck %s
# CHECK: CREATE Asset:Asset
# CHECK: name: GOLD{{.*}}quantity: 100
```

It needs a Canton jar (`--canton-jar` or `DPM_TRACE_CANTON_JAR`), plus `lit` and
`FileCheck` on PATH. See `daml-tests/itests/` for a working suite.

## Submit

Submit a command to a participant (submit-and-wait) and print the update id —
the primitive integration tests use to create state and then trace it:

```bash
dpm trace submit \
  --submitter http://localhost:<json-ledger-api-port> \
  --act-as '<party-id>' \
  --template '#<package-name>:Mod:Template' \
  --arg owner='<party-id>' --arg count=0
```

Use `--print-json` for the full submit-and-wait response, or `--allow-fail` to
capture a rejected submission as JSON (which `dpm trace --completion-file` then
maps back to source) instead of erroring out.

## Failed submission source demo

This fixture shows the CI-style path: consume a captured completion/error JSON,
verify it against a compiled DAR with `damlc inspect`, and resolve it against
local Daml sources.

```bash
dpm trace --completion-file examples/failed-with-source.completion.json \
  --daml-yaml <path-to-daml-project>/daml.yaml \
  --dar <path-to-daml-project>/.daml/dist/app.dar \
  --damlc daml
```

The output includes a `Source diagnostics` block with `file:line:column` and a
caret under the matching Daml code.

## Tests

Fast source-diagnostic tests:

```bash
lit tests
```

Inspect-backed source diagnostic test:

```bash
DPM_TRACE_RUN_DAMLC_INSPECT=1 \
DPM_TRACE_DAMLC=daml \
lit tests/completion-source-inspect.test
```

Opt-in Daml Script test-runner integration test (uses a real Daml toolchain
against the sibling `daml-tests` package):

```bash
DPM_TRACE_RUN_DAML_TEST=1 \
DPM_TRACE_DAML=daml \
lit tests/daml-script-test.test
```

Opt-in local Canton integration test:

```bash
DPM_TRACE_RUN_REAL_CANTON=1 \
DPM_TRACE_DAML=daml \
DPM_TRACE_DAMLC=daml \
DPM_TRACE_CANTON_JAR=<path-to-canton.jar> \
DPM_TRACE_DAML_HELPER=<path-to-daml-helper> \
lit tests/real-canton-failed-completion.test
```

## Notes

- Output is participant-scoped. It is not a global Canton transaction.
- Failed submissions may not have an update id. In that case comparison uses completion/error data.
- Source diagnostics use `damlc inspect` plus local source/project metadata when available, with local source matching as a fallback. Compiler debug-info generation is out of scope for this PoC.
