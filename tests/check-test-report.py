"""Daml-independent checks for the `dpm trace test` parsing and source mapping.

Runs against committed fixtures so it needs no Daml toolchain. Exercises the
transaction-HTML decoder, the JUnit parser, the Canton error decoration stripper,
and the failure -> source resolver. Exits non-zero (printing what failed) so it
can be driven directly by lit.
"""
from __future__ import annotations

import sys
import os
import io
import json
import tempfile
import urllib.error
import urllib.request
from pathlib import Path


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-test-report.py <repo-root>", file=sys.stderr)
        return 2
    repo_root = Path(sys.argv[1]).resolve()
    sys.path.insert(0, str(repo_root / "src"))
    from dpm_trace.cli import (  # noqa: E402
        SourceIndex,
        _eval_replay,
        completion_source_diagnostics,
        daml_child_env,
        find_config,
        http_json,
        parse_junit,
        register_component_in_manifest,
        strip_canton_error_decoration,
        test_failure_locations,
        transaction_html_to_text,
        transaction_locations,
        transaction_stats,
    )

    fixtures = repo_root / "tests" / "fixtures"
    errors: list[str] = []

    def check(condition: bool, message: str) -> None:
        if not condition:
            errors.append(message)

    # 1. Transaction HTML -> readable tree + stats.
    html = (fixtures / "transaction-testTransfer.html").read_text(encoding="utf-8")
    text = transaction_html_to_text(html)
    check("<style" not in text and "<br>" not in text, "HTML tags were not stripped")
    check("creates Asset:Asset" in text, "decoded tree is missing the create")
    check("exercises Transfer on" in text, "decoded tree is missing the exercise")
    check("consumed by:" in text, "decoded tree is missing the archive linkage")
    stats = transaction_stats(text)
    check(stats["transactions"] == 2, f"transactions stat wrong: {stats}")
    check(stats["creates"] == 2, f"creates stat wrong: {stats}")
    check(stats["exercises"] == 1, f"exercises stat wrong: {stats}")
    check(stats["archives"] == 1, f"archives stat wrong: {stats}")
    locs = transaction_locations(text)
    check(any(loc.startswith("Test:") for loc in locs), f"no source locations parsed: {locs}")

    # 2. JUnit parsing: one pass, one failure.
    junit = (
        '<?xml version="1.0" ?><testsuites><testsuite name="s">'
        '<testcase name="ok" classname="s"/>'
        '<testcase name="bad" classname="s"><failure>boom</failure></testcase>'
        "</testsuite></testsuites>"
    )
    tmp = Path(tempfile.mkstemp(suffix=".xml")[1])
    try:
        tmp.write_text(junit, encoding="utf-8")
        cases = {c.name: c for c in parse_junit(tmp)}
    finally:
        tmp.unlink(missing_ok=True)
    check(set(cases) == {"ok", "bad"}, f"unexpected cases: {sorted(cases)}")
    check(cases["ok"].status == "passed", "passing case not marked passed")
    check(cases["bad"].status == "failed", "failing case not marked failed")
    check(cases["bad"].message == "boom", f"failure message wrong: {cases['bad'].message!r}")

    # 3. Canton error decoration stripping.
    decorated = (
        "Script execution failed: Failed with status: UNHANDLED_EXCEPTION/"
        "DA.Exception.AssertionFailed:AssertionFailed: sample boundary violated "
        "Using Canton Error Category InvalidGivenCurrentSystemStateOther"
    )
    cleaned = strip_canton_error_decoration(decorated)
    check(cleaned == "sample boundary violated", f"decoration not stripped: {cleaned!r}")

    # 4. Failure -> source mapping (explicit call site + assertMsg literal).
    index = SourceIndex(source_roots=[str(fixtures)])
    message = f"Script execution failed on commit at Sample:9:5: {decorated}"
    mapped, capped = test_failure_locations(message, index)
    sample_lines = (fixtures / "Sample.daml").read_text(encoding="utf-8").splitlines()
    explicit = any(Path(m.path).name == "Sample.daml" and m.line == 9 for m in mapped)
    literal = any(
        Path(m.path).name == "Sample.daml"
        and m.line - 1 < len(sample_lines)
        and "sample boundary violated" in sample_lines[m.line - 1]
        for m in mapped
    )
    check(explicit, f"explicit Sample:9 location not resolved: {[(Path(m.path).name, m.line) for m in mapped]}")
    check(literal, f"assertMsg literal not mapped into source: {[(Path(m.path).name, m.line) for m in mapped]}")

    # 4b. The cap is configurable and signals when more locations were available.
    small, small_capped = test_failure_locations(message, index, max_source_locations=1)
    check(len(small) <= 1, f"max_source_locations=1 should return at most 1: {len(small)}")
    check(small_capped, f"max_source_locations=1 should flag capping: {small}")

    # 4c. completion_source_diagnostics honors the cap and signals truncation.
    diag_index = SourceIndex(source_roots=[str(fixtures)])
    completion = {"status": {"code": 9, "message": 'AssertionFailed: "sample boundary violated"'}}
    full, full_capped = completion_source_diagnostics(completion, diag_index)
    one, one_capped = completion_source_diagnostics(completion, diag_index, max_source_locations=1)
    check(bool(full), "completion_source_diagnostics should resolve the Sample fixture")
    check(len(one) <= 1, f"completion cap=1 should return at most 1: {len(one)}")
    check(one_capped, f"completion cap=1 should flag capping when full had {len(full)}")

    # 4d. daml_child_env forces a UTF-8 locale under a non-UTF-8 inherited locale
    #     and drops DPM_RESOLUTION_FILE. This is the root-cause fix that makes the
    #     `daml test` Unicode-retry fallback expected-dead; regression-test it so
    #     a future change cannot silently drop the locale guard.
    saved = {k: os.environ.get(k) for k in ("LANG", "LC_ALL", "LC_CTYPE", "DPM_RESOLUTION_FILE")}
    try:
        os.environ["LANG"] = "C"
        os.environ["LC_ALL"] = "C"
        os.environ.pop("LC_CTYPE", None)
        os.environ["DPM_RESOLUTION_FILE"] = "/tmp/should-be-dropped"
        env = daml_child_env()
    finally:
        for k, v in saved.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
    check("DPM_RESOLUTION_FILE" not in env, "daml_child_env did not drop DPM_RESOLUTION_FILE")
    check("utf" in env.get("LANG", "").lower(), f"daml_child_env did not force a UTF-8 LANG under C locale: {env.get('LANG')}")
    check("utf" in env.get("LC_ALL", "").lower(), f"daml_child_env did not force a UTF-8 LC_ALL under C locale: {env.get('LC_ALL')}")

    # 4e. find_config bounds its upward walk at the nearest project boundary
    #     marker so a parent workspace .dpm-trace.json cannot leak into an
    #     unrelated subproject; an explicit path bypasses the boundary.
    import json as _json
    import tempfile as _tempfile
    workspace = Path(_tempfile.mkdtemp()).resolve()
    proj = workspace / "proj"
    (proj / "pkg").mkdir(parents=True)
    (proj / ".git").mkdir()
    proj_config = proj / ".dpm-trace.json"
    proj_config.write_text(_json.dumps({"ledgerUrl": "http://proj"}), encoding="utf-8")
    parent_config = workspace / ".dpm-trace.json"
    parent_config.write_text(_json.dumps({"ledgerUrl": "http://parent"}), encoding="utf-8")
    cwd = Path.cwd().resolve()
    try:
        os.chdir(proj / "pkg")
        check(find_config(None) == proj_config, "find_config did not pick up the in-project config from a subdir")
        proj_config.unlink()
        check(find_config(None) is None, "find_config crossed a .git boundary to a parent workspace config")
        os.chdir(workspace)
        check(find_config(None) == parent_config, "find_config did not fall back to cwd-only when no boundary marker is present")
        check(find_config(str(parent_config)) == parent_config, "explicit --config did not bypass the boundary walk")
    finally:
        os.chdir(cwd)

    # 4f. http_json retries transient failures (5xx + connection errors) with
    #     bounded backoff, distinct from the ingestion --wait loop, and does not
    #     retry 4xx or when retry=False (e.g. submit-and-wait).
    import dpm_trace.cli as _cli

    class _FakeResp:
        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

        def read(self):
            return b'{"ok": true}'

    def _http_error(code):
        return urllib.error.HTTPError("u", code, "x", {}, io.BytesIO(b"{}"))

    _orig_urlopen = urllib.request.urlopen
    _orig_sleep = _cli.time.sleep
    _cli.time.sleep = lambda seconds: None
    try:
        counter = {"n": 0}

        def _then_200(req, timeout):
            counter["n"] += 1
            if counter["n"] == 1:
                raise _http_error(503)
            return _FakeResp()

        urllib.request.urlopen = _then_200
        counter["n"] = 0
        ok = _cli.http_json("GET", "http://x")
        check(ok == {"ok": True}, f"503-then-200 should succeed via retry: {ok}")
        check(counter["n"] == 2, f"transient retry should use 2 attempts, got {counter['n']}")

        def _always_503(req, timeout):
            counter["n"] += 1
            raise _http_error(503)

        urllib.request.urlopen = _always_503
        counter["n"] = 0
        try:
            _cli.http_json("GET", "http://x")
            check(False, "persistent 503 should raise")
        except RuntimeError:
            pass
        check(counter["n"] == 3, f"persistent 503 should exhaust 3 attempts, got {counter['n']}")

        def _always_404(req, timeout):
            counter["n"] += 1
            raise _http_error(404)

        urllib.request.urlopen = _always_404
        counter["n"] = 0
        try:
            _cli.http_json("GET", "http://x")
            check(False, "404 should raise")
        except RuntimeError:
            pass
        check(counter["n"] == 1, f"404 should not be retried, got {counter['n']}")

        urllib.request.urlopen = _always_503
        counter["n"] = 0
        try:
            _cli.http_json("GET", "http://x", retry=False)
            check(False, "retry=False should raise on 503")
        except RuntimeError:
            pass
        check(counter["n"] == 1, f"retry=False should not retry, got {counter['n']}")
    finally:
        urllib.request.urlopen = _orig_urlopen
        _cli.time.sleep = _orig_sleep

    # 5. install-plugin: the component registers under `components:` (before `assistant:`).
    manifest = Path(tempfile.mkstemp(suffix=".yaml")[1])
    try:
        manifest.write_text(
            "apiVersion: x\nspec:\n  components:\n    damlc:\n      version: 3.4.11\n"
            "  assistant:\n    version: 1.0.10\n",
            encoding="utf-8",
        )
        register_component_in_manifest(manifest, "dpm-trace", "0.1.0")
        text = manifest.read_text(encoding="utf-8")
    finally:
        manifest.unlink(missing_ok=True)
    check("    dpm-trace:" in text, "install-plugin did not register dpm-trace in the manifest")
    check("dpm-trace:" in text and text.index("dpm-trace:") < text.index("  assistant:"),
          "install-plugin placed dpm-trace under assistant: instead of components:")

    # 6. Source-linked replay confidence flag: the PoC evaluator must mark
    #    un-reducable expressions (e.g. `*`, `if`) as not evaluated rather than
    #    silently returning None, so the visualizer does not present a missing
    #    result as a faithful replay.
    plus = _eval_replay("2 + 3", {})
    check(plus == (5, True, None), f"2+3 should evaluate: {plus}")
    star = _eval_replay("a * b", {"a": 2, "b": 3})
    check(star[0] is None and star[1] is False, f"`*` should be flagged not evaluated: {star}")
    lookup = _eval_replay("owner", {"owner": "Alice"})
    check(lookup == ("Alice", True, None), f"env lookup should evaluate: {lookup}")
    empty = _eval_replay("   ", {})
    check(empty[1] is True, f"empty expression should not be flagged unsupported: {empty}")

    if errors:
        print("dpm trace test parser/mapping checks FAILED:")
        for err in errors:
            print(f"  - {err}")
        return 1
    print("dpm trace test parser/mapping checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
