# AGENTS.md

Guidance for agents working in this repository.

## Project

`dpm-trace` is a DPM component for participant-scoped Canton transaction
visualization, and for turning Daml Script unit tests into a source-mapped CI
gate.

Command surface:

- `dpm trace <update-id>`: inspect a successful transaction.
- `dpm trace --command-id <id>` / `--completion-file <file>`: inspect a failed submission through completion data.
- `dpm trace open <artifact>`: reopen an exported trace artifact.
- `dpm trace prepare`: prepare a transaction without committing it.
- `dpm trace submit`: submit-and-wait a command and print the update id (integration-test primitive).
- `dpm trace compare`: compare prepared transactions, successful transactions, or completion data.
- `dpm trace test`: run Daml Script unit tests (unit mode) or an lit suite against a managed local Canton (`--integration`).
- `dpm trace ... --visualize`: open the interactive CLI visualizer.
- `dpm-trace install-plugin`: install the binary as a DPM component, so `dpm trace` works without a repo clone.

`main()` strips a leading `trace` arg, so `dpm-trace trace <id>` behaves like
the plugin's `dpm trace <id>`.

## Code layout

Go, standard library only -- no cobra, no viper, so `go.mod` stays free of
requires. `cmd/trace/` is one file per subcommand; `internal/` holds model,
ledger, render, source, testrunner, integration, scaffold, plugin, visualizer
and config.

The lit suite and the golden harness drive the compiled binary and locate it
through `DPM_TRACE_BIN`; both refuse to run without it. The Python
implementation this was ported from has been removed.

Key areas to orient in the tree:

- Transaction model + normalization: `internal/model` -- `Trace`, `Event`, `NormalizeTrace`, `NormalizeEvent`. Event kinds (`create`/`exercise`/`archive`/`assign`/`unassign`) come from `eventVariantKinds`; add new Ledger API variant wrappers there rather than in `NormalizeEvent`. `linkRangeChildren` reconstructs the tree from `lastDescendantNodeId`, and `inferRoots` keeps document order because `compare` pairs roots positionally.
- Ledger access: `internal/ledger` -- the JSON Ledger API client with bounded retry (`JSON`, `LoadUpdate`, `Prepare`, `SubmitAndWait`, `FetchCompletion`), plus command building (`CommandSpec`). Decode through `internal/model`, never `encoding/json`: the latter loses numeric precision and key order.
- Pretty + interactive rendering: `internal/render` (`PrettyTrace`, `CompletionTrace`, `SubmitFailure`, the compare views) and `internal/visualizer` (the `--visualize` REPL: `Stepper`, `Breakpoint`, plus `Filter` for search and `ShowTree`/`Collapse`/`Expand` for the foldable tree). The REPL's command list lives in three places that drift apart -- `Dispatch`, the `help` output and the startup banner; `TestHelpNamesEveryFilterField` and `TestBannerMentionsSearch` guard two of them.
- Source mapping: `internal/source` -- `Index` loads `daml.yaml` sources, `--debug-info` files and, with `--dar`, `damlc inspect` output; `FindFailureText`, `EntityContaining`, `Snippet`.
- Test runner (`dpm trace test`): `internal/testrunner` -- `Run` → `Command`, `ParseJUnit`, `TransactionHTMLToText`, `FailureLocations`, `ReportJSON` (`dpm-trace/test-report/v0`). Exit 1 means tests failed; operational errors exit 2.
- Integration runner (`--integration`): `internal/integration` -- `Run` boots a local Canton (`ConfigText`, `BootstrapText`, `FreePorts`, `WaitForParties`, `BuildDAR`), exports `DPM_TRACE_IT_*`, runs `lit`, tears down. `--parties Name@N` (`ParsePlacements`) provisions N participants; tests reach participant K via `%ledger{K}` and tolerate ingestion lag with `dpm trace --wait`.
- Scaffolder (`--init`): `internal/scaffold` writes `itests/` and a self-contained `unittests/` package from the embedded templates. `templates/lit.cfg.py.tmpl` is kept in sync with the canonical `daml-contracts/itests/lit.cfg.py` by `tests/check-scaffolder-sync.py`.
- DPM component: `internal/plugin` -- `Install` writes the component into the DPM home and registers it in the SDK manifest.
- Spawning daml/damlc/canton: `testrunner.ChildEnv` (drops `DPM_RESOLUTION_FILE`, forces a UTF-8 locale).

A worked example package for the test runner (Asset contract, Script tests in
a separate `unittests/` package, `itests/`, CI workflow, regression demo) lives
in the sibling `daml-contracts` directory; `examples/asset` in this repo is a
copy kept in sync by `tests/check-examples-sync.py`. The Script tests depend on
the asset DAR, so build with `dpm build --all` before running them. Both
packages pull Splice DARs as `git:` dependencies, which `dpm build` does not
fetch: run `dpm install package` in `examples/asset` and in its `unittests/`
first.

## Development Rules

- Keep the docs in step. The user docs live in a separate repo, [walnuthq/docs](https://github.com/walnuthq/docs) (`docs-canton/`). Any change to a version (SDK, DPM, Canton), a command, flag, exit code or output format, the install steps, or `examples/asset` (sources, test names and counts, line numbers shown in output) needs a check of those pages, and the same update there when they show it. Re-run the commands a page shows and copy the new output rather than editing it by hand.
- Keep examples generic. Do not commit local machine paths, usernames, hostnames, or personal temp paths. Use placeholders such as `<path-to-daml-project>`, `<path-to-canton.jar>`, `<package-dir>`, and `<party-id>`.
- Do not commit `.venv/`, `.dpm-home/`, `.dpm-trace.json`, `tests/.lit/`, or generated caches.
- Stdlib only: `go.mod` stays free of requires. The test drivers under `tests/` are Python and must run on a clean Python 3.10+ with no pip installs beyond `lit` and `filecheck`.
- Keep the tool participant-scoped in wording and behavior. Do not describe output as a global Canton transaction.
- Failed submissions may not have an update id. Use completion/error data for those workflows.
- Source diagnostics should prefer `damlc inspect` plus local project/source metadata when available, with local source matching only as a fallback.
- When spawning `daml`/`damlc`, build the child environment with `testrunner.ChildEnv`, which drops `DPM_RESOLUTION_FILE` so the child resolves the target package rather than the dpm-trace component's plugin resolution context.
- `dpm trace test` is a CI gate: it must exit non-zero when any test fails. Keep the `--print-json` report (`dpm-trace/test-report/v0`) and `--junit` output stable for downstream consumers.

## Setup

```bash
go build -o /tmp/dpm-trace ./cmd/trace
/tmp/dpm-trace install-plugin     # registers `dpm trace`
```

Optional local config:

```bash
cp .dpm-trace.example.json .dpm-trace.json
```

Do not commit `.dpm-trace.json`.

## Tests

`lit` and `FileCheck` come from pip -- `pip install lit filecheck` -- so no
LLVM install is needed. `filecheck` installs lowercase; symlink it as
`FileCheck`, which is the name the suites invoke.

Run the suites (the binary is required; both refuse to run without it):

```bash
go build -o /tmp/dpm-trace ./cmd/trace
DPM_TRACE_BIN=/tmp/dpm-trace lit tests -v
DPM_TRACE_BIN=/tmp/dpm-trace python3 tests/check-golden.py .
```

Run whitespace checks before commit:

```bash
git diff --check
```

Run the inspect-backed source diagnostic test when touching source mapping:

```bash
DPM_TRACE_RUN_DAMLC_INSPECT=1 \
DPM_TRACE_DAMLC=dpm \
lit tests/completion-source-inspect.test
```

The `dpm trace test` parsing and source mapping are covered by the
daml-independent `tests/test-report-parse.test` (committed fixtures in
`tests/fixtures/`, always run). The end-to-end runner is opt-in and uses the
sibling `daml-contracts` package:

```bash
DPM_TRACE_RUN_DAML_TEST=1 \
DPM_TRACE_DAML=dpm \
lit tests/daml-script-test.test
```

## Path Hygiene

The test suite includes `tests/no-local-paths.test`, which scans Git-visible files for local path leaks.

If a path leak appears, replace it with a placeholder rather than adding it to an allowlist.

For a broader manual check, run the local path guard through `lit tests/no-local-paths.test`.

## Commit Hygiene

- Keep commits focused.
- Do not stage unrelated changes.
- Do not rewrite ignored local notes unless explicitly asked.
