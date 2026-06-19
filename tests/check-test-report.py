"""Daml-independent checks for the `dpm trace test` parsing and source mapping.

Runs against committed fixtures so it needs no Daml toolchain. Exercises the
transaction-HTML decoder, the JUnit parser, the Canton error decoration stripper,
and the failure -> source resolver. Exits non-zero (printing what failed) so it
can be driven directly by lit.
"""
from __future__ import annotations

import sys
import tempfile
from pathlib import Path


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-test-report.py <repo-root>", file=sys.stderr)
        return 2
    repo_root = Path(sys.argv[1]).resolve()
    sys.path.insert(0, str(repo_root / "src"))
    from dpm_trace.cli import (  # noqa: E402
        SourceIndex,
        parse_junit,
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
    mapped = test_failure_locations(message, index)
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

    if errors:
        print("dpm trace test parser/mapping checks FAILED:")
        for err in errors:
            print(f"  - {err}")
        return 1
    print("dpm trace test parser/mapping checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
