# Contributing to dpm-trace

Bug reports, fixes and features are welcome. Open an issue first for anything
larger than a small fix, so the approach can be agreed before the work.

[AGENTS.md](AGENTS.md) describes the code layout; read it before changing a
package you have not touched before.

## Setup

You need:

- Go 1.25 or newer.
- Python 3.10 or newer with `lit` and `filecheck` (`pip install lit filecheck`).
  `filecheck` installs lowercase; the suites call `FileCheck`, so symlink it:
  `ln -s "$(command -v filecheck)" <dir-on-PATH>/FileCheck`.
- [golangci-lint](https://golangci-lint.run) v2.13.0, the version CI runs.

For the opt-in suites and the example package you also need DPM with the SDK
pinned in `examples/asset/daml.yaml`, and Java 17 or newer.

## Build and test

Run what CI runs before opening a pull request:

```bash
gofmt -l cmd internal          # must print nothing
go vet ./...
golangci-lint run
go test ./...

go build -o /tmp/dpm-trace ./cmd/trace
DPM_TRACE_BIN=/tmp/dpm-trace lit tests -v
DPM_TRACE_BIN=/tmp/dpm-trace python3 tests/check-golden.py .

git diff --check
```

The lit suite and the golden harness drive the compiled binary and refuse to
run without `DPM_TRACE_BIN`.

### Golden output

`tests/golden/` holds the full stdout, stderr and exit code of each case in
`tests/check-golden.py`. If you change rendering on purpose, re-record and
review the diff before committing it:

```bash
DPM_TRACE_BIN=/tmp/dpm-trace python3 tests/check-golden.py . --update
git diff tests/golden
```

A golden diff you did not expect is a bug, not something to re-record.

### Opt-in suites

These need a Daml toolchain and are skipped by default. Run them when you touch
the matching code.

Source mapping (`internal/source`):

```bash
DPM_TRACE_RUN_DAMLC_INSPECT=1 DPM_TRACE_DAMLC=dpm \
  lit tests/completion-source-inspect.test
```

The `dpm trace test` runner end to end, against the sibling `daml-contracts`
package:

```bash
DPM_TRACE_RUN_DAML_TEST=1 DPM_TRACE_DAML=dpm \
  lit tests/daml-script-test.test
```

The integration runner against a local Canton, from the `daml-contracts`
directory:

```bash
/tmp/dpm-trace trace test --integration itests/ \
  --canton-jar <path-to-canton.jar> --daml dpm \
  --parties Alice@1,Bob@2,Issuer@1
```

CI runs this last one only on pushes to `main`, not on pull requests.

## Rules

- Standard library only. `go.mod` has no `require` lines; do not add one.
- Decode Ledger API JSON through `internal/model`, not `encoding/json`, which
  loses numeric precision and key order.
- Spawn `daml`, `damlc` and `canton` with the environment from
  `testrunner.ChildEnv`.
- Output is one participant's view. Do not describe it as a global Canton
  transaction, in code, output or docs.
- `dpm trace test` is a CI gate: exit 1 when tests fail, 2 on operational
  errors. Keep the `--print-json` report (`dpm-trace/test-report/v0`) and the
  `--junit` output backward compatible.
- Every behavior change gets a test: a Go unit test, a lit test, or a golden
  case for rendering.
- The visualizer's command list lives in `Dispatch`, the `help` output and the
  startup banner. Update all three.

### Files kept in sync

Checks in the lit suite fail when these drift:

- `examples/asset` is a copy of the sibling `daml-contracts` package
  (`tests/check-examples-sync.py`).
- `internal/scaffold/templates/lit.cfg.py.tmpl` matches
  `daml-contracts/itests/lit.cfg.py` (`tests/check-scaffolder-sync.py`).

Change both sides in the same pull request pair.

### Local paths

Do not commit machine paths, usernames, hostnames or temp paths. Use
placeholders such as `<path-to-daml-project>`, `<path-to-canton.jar>`,
`<package-dir>` and `<party-id>`. `tests/no-local-paths.test` scans tracked
files for the markers in `tests/forbidden-markers.txt`. If it fails, replace the
path; do not allowlist it.

Do not commit `.dpm-trace.json`, `.venv/`, `.dpm-home/`, `tests/.lit/` or
build output.

## Commits and pull requests

- One logical change per commit. Do not mix in unrelated edits.
- Commit subject: `type(scope): description`, for example
  `fix(render): keep party aliases stable across roots`. Types: `feat`, `fix`,
  `docs`, `test`, `refactor`, `chore`, `ci`. Add a body only when the reason is
  not visible in the diff.
- Pull request description: a few sentences on what changed, why, and anything
  still open.
- All `ci` jobs must pass: lint, build and unit tests, lit, the example DAR
  build, and coverage. Coverage has a floor in `.github/workflows/ci.yml`; do
  not lower it to make a build pass.

## Releases

Maintainers release by pushing a `v*` tag. The release workflow builds the
archives, publishes the DPM component to `ghcr.io/walnuthq/dpm-trace` and opens
a draft release for review.

## License

By contributing you agree that your work is licensed under the
[Apache License 2.0](LICENSE).
