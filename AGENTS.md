# AGENTS.md

Guidance for agents working in this repository.

## Project

`dpm-trace` is a DPM component proof of concept for participant-scoped Canton
transaction visualization, and for turning Daml Script unit tests into a
source-mapped CI gate.

Command surface:

- `dpm trace <update-id>`: inspect a successful transaction.
- `dpm trace --command-id <id>` / `--completion-file <file>`: inspect a failed submission through completion data.
- `dpm trace open <artifact>`: reopen an exported trace artifact.
- `dpm trace prepare`: prepare a transaction without committing it.
- `dpm trace submit`: submit-and-wait a command and print the update id (integration-test primitive).
- `dpm trace compare`: compare prepared transactions, successful transactions, or completion data.
- `dpm trace test`: run Daml Script unit tests (unit mode) or an lit suite against a managed local Canton (`--integration`).
- `dpm trace ... --visualize`: open the interactive CLI visualizer.

`main()` strips a leading `trace` arg, so `dpm_trace.cli trace <id>` behaves like
the plugin's `dpm trace <id>`.

## Code layout

The whole tool is a single, stdlib-only module: `src/dpm_trace/cli.py` (no
third-party runtime dependencies). Subcommands are dispatched by the first
argument in `main()`; everything else is plain functions.

Key areas to orient in the file:

- Transaction model + normalization: `NormalizedTrace`, `TraceEvent`, `normalize_trace`, `load_update`.
- Pretty + interactive rendering: `print_pretty_trace`, `Stepper` (the `--visualize` REPL).
- Failed submissions / completions: `fetch_completion_by_command_id`, `normalize_completion`, `print_completion_trace`.
- Source mapping: `SourceIndex` (loads `daml.yaml` sources and, with `--dar`, `damlc inspect`), `completion_source_needles`, `render_source_diagnostic`.
- Test runner (`dpm trace test`): `test_main` → `run_test` → `daml_test_command`, `parse_junit`, `transaction_html_to_text`, `transaction_stats`, `test_failure_locations`, `print_test_report` / `test_report_json`.
- Integration runner (`--integration`): `run_integration_tests` boots a local Canton (`canton_config_text`, `canton_bootstrap_text`, `find_free_ports`, `wait_for_parties`, `build_dar`), exports `DPM_TRACE_IT_*` env, runs `lit`, tears down. `--parties Name@N` (`parse_party_placements`) provisions N participants; tests reach participant K via `%ledger{K}` and tolerate ingestion lag with `dpm trace --wait`.
- Scaffolder (`--init`): `run_init` writes `itests/` (from `integration_lit_cfg_text` / `integration_example_test_text` — keep `daml-tests/itests/lit.cfg.py` in sync) and a self-contained `unittests/` package (`unit_test_daml_yaml_text` / `unit_test_example_text`).
- Submit primitive (`dpm trace submit`): `submit_main` → `run_submit` (submit-and-wait, prints the update id).
- Spawning daml/damlc/canton: `daml_child_env()` (drops `DPM_RESOLUTION_FILE`, forces a UTF-8 locale).

A worked example package for the test runner (Asset contract + Script tests +
CI workflow + regression demo) lives in the sibling `daml-tests` directory.

## Development Rules

- Keep examples generic. Do not commit local machine paths, usernames, hostnames, or personal temp paths. Use placeholders such as `<path-to-daml-project>`, `<path-to-canton.jar>`, `<package-dir>`, and `<party-id>`.
- Do not commit `.venv/`, `.dpm-home/`, `.dpm-trace.json`, `tests/.lit/`, or generated caches.
- Stdlib only. The tool must run on a clean Python 3.10+ with no pip installs; do not add third-party runtime imports.
- Keep the tool participant-scoped in wording and behavior. Do not describe output as a global Canton transaction.
- Failed submissions may not have an update id. Use completion/error data for those workflows.
- Source diagnostics should prefer `damlc inspect` plus local project/source metadata when available, with local source matching only as a fallback.
- When spawning `daml`/`damlc`, build the child environment with `daml_child_env()`, which drops `DPM_RESOLUTION_FILE` so the child resolves the target package rather than the dpm-trace component's plugin resolution context.
- `dpm trace test` is a CI gate: it must exit non-zero when any test fails. Keep the `--print-json` report (`dpm-trace/test-report/v0`) and `--junit` output stable for downstream consumers.

## Setup

```bash
.venv/bin/python -m pip install -e .
./scripts/install-local-dpm-trace.sh
```

Optional local config:

```bash
cp .dpm-trace.example.json .dpm-trace.json
```

Do not commit `.dpm-trace.json`.

## Tests

Run the fast suite before handing off changes:

```bash
lit tests
```

Run Python syntax checks when editing Python files:

```bash
.venv/bin/python -m py_compile src/dpm_trace/cli.py tests/check-no-local-paths.py tests/check-test-report.py
```

Run whitespace checks before commit:

```bash
git diff --check
```

Run the inspect-backed source diagnostic test when touching source mapping:

```bash
DPM_TRACE_RUN_DAMLC_INSPECT=1 \
DPM_TRACE_DAMLC=daml \
lit tests/completion-source-inspect.test
```

The `dpm trace test` parsing and source mapping are covered by the
daml-independent `tests/test-report-parse.test` (committed fixtures in
`tests/fixtures/`, always run). The end-to-end runner is opt-in and uses the
sibling `daml-tests` package:

```bash
DPM_TRACE_RUN_DAML_TEST=1 \
DPM_TRACE_DAML=daml \
lit tests/daml-script-test.test
```

Run the local Canton integration test only when Canton, Daml, and daml-helper paths are available:

```bash
DPM_TRACE_RUN_REAL_CANTON=1 \
DPM_TRACE_DAML=daml \
DPM_TRACE_DAMLC=daml \
DPM_TRACE_CANTON_JAR=<path-to-canton.jar> \
DPM_TRACE_DAML_HELPER=<path-to-daml-helper> \
lit tests/real-canton-failed-completion.test
```

## Path Hygiene

The test suite includes `tests/no-local-paths.test`, which scans Git-visible files for local path leaks.

If a path leak appears, replace it with a placeholder rather than adding it to an allowlist.

For a broader manual check, run the local path guard through `lit tests/no-local-paths.test`.

## Commit Hygiene

- Keep commits focused.
- Do not stage unrelated changes.
- Do not rewrite ignored local notes unless explicitly asked.
