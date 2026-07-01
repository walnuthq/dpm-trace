# Technical Notes

`dpm-trace` is a single stdlib-only CLI module for participant-scoped Canton
transaction inspection and Daml Script CI reporting. These notes capture the
operational shape without local environment details.

## Participant Scope

Ledger API fetches are participant-scoped and require `--read-as` or `--party`.
Do not describe the resulting tree as a global Canton transaction. It is the
view visible to the supplied participant parties.

## Failed Submissions

Failed submissions may not have an update id. Use completion data for those
workflows:

- `dpm trace --command-id <id>` fetches recent completions from the participant.
- `dpm trace --completion-file <file>` reopens captured completion JSON.
- `dpm trace compare --prepared <artifact> --completion-file <file>` compares a
  prepared command against completion/error data.

`dpm trace submit --allow-fail --print-json` is useful in integration tests
because it prints the rejection body for follow-up tracing.

## Source Diagnostics

Source mapping prefers package metadata from `damlc inspect`, DAR files, and
local `daml.yaml` source roots. Text matching is a fallback for user-authored
failure strings such as `assertMsg`, `abort`, and assertion messages.

Large source matches are capped by default. Use `--max-source-locations <n>` on
`trace`, `compare`, or `test` to raise the cap. Human reports and JSON test
reports expose when the cap binds.

## Test Runner

`dpm trace test` is a CI gate and exits non-zero when any test fails. The JSON
report schema is `dpm-trace/test-report/v0`; keep existing fields stable and add
new fields only additively.

When spawning `daml` or `damlc`, use `daml_child_env()`. It drops
`DPM_RESOLUTION_FILE` so the child resolves the target package rather than the
dpm-trace component context, and it forces a UTF-8 locale under non-UTF-8
inherited locales.

## Integration Runner

`dpm trace test --integration <dir>` boots managed local Canton, builds a DAR,
provisions parties, exports `DPM_TRACE_IT_*`, runs lit, and tears down Canton.
The generated lit config intentionally registers `%ledger2` before `%ledger`
because lit substitutions are applied as ordered literal replacements.

When updating `integration_lit_cfg_text()`, keep it in sync with the sibling
`daml-tests/itests/lit.cfg.py`; `tests/scaffolder-sync.test` enforces this when
the sibling package is available.
