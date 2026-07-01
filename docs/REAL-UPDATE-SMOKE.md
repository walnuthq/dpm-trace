# Real Update Smoke Test

This is the redacted checklist for exercising `dpm trace` against a real local
Canton participant. Replace placeholders with paths and parties from your
workspace; do not commit concrete local values.

## Prerequisites

- Daml SDK on `PATH`, or set `DPM_TRACE_DAML=daml`.
- Canton jar available as `<path-to-canton.jar>`.
- Daml helper available as `<path-to-daml-helper>`.
- The sibling example package available as `<path-to-daml-tests>`.

## Failed Completion Smoke

Run the opt-in lit test that starts Canton, submits a rejected command, fetches
completion data, and verifies source diagnostics:

```bash
DPM_TRACE_RUN_REAL_CANTON=1 \
DPM_TRACE_DAML=daml \
DPM_TRACE_DAMLC=daml \
DPM_TRACE_CANTON_JAR=<path-to-canton.jar> \
DPM_TRACE_DAML_HELPER=<path-to-daml-helper> \
lit tests/real-canton-failed-completion.test
```

Expected result: the test passes and the trace output reports a participant-
scoped failed completion with source diagnostics. If it fails before submission,
check the Canton jar/helper placeholders and Java version first. If completion
lookup times out, rerun with a larger completion timeout or inspect the managed
Canton logs printed by the lit test.

## Successful Update Smoke

Use the integration runner when you need a committed update id:

```bash
DPM_TRACE_RUN_REAL_CANTON=1 \
DPM_TRACE_DAML=daml \
DPM_TRACE_DAMLC=daml \
DPM_TRACE_CANTON_JAR=<path-to-canton.jar> \
DPM_TRACE_DAML_HELPER=<path-to-daml-helper> \
dpm-trace test <path-to-daml-tests> --integration itests
```

The integration suite provisions parties and exports `%ledger`, `%ledger2`,
`%alice`, `%bob`, `%dar`, and related lit substitutions. Tests should use
`dpm trace --wait` when reading from a participant that may still be ingesting a
transaction committed through another participant.
