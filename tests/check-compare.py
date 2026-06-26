"""Daml-independent checks for the `dpm trace compare` family.

Exercises compare_traces, compare_prepared_to_trace, and
compare_prepared_to_completion via the CLI --print-json path against committed
fixtures, so it needs no Canton node, no Daml toolchain, and no network.
"""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

# ── output helpers ────────────────────────────────────────────────────────────

_LABEL_W = 43
_errors: list[str] = []
_section = ""


def section(title: str) -> None:
    global _section
    _section = title
    print(f"\n{title}")
    print("─" * max(len(title), 40))


def check(
    label: str, condition: bool, actual=None, *, fail_msg: str | None = None
) -> None:
    status = "PASS" if condition else "FAIL"
    suffix = f"  {actual}" if actual is not None else ""
    print(f"  {label:<{_LABEL_W}} {status}{suffix}")
    if not condition:
        _errors.append(fail_msg or f"{_section} / {label}")


# ── CLI helpers ───────────────────────────────────────────────────────────────


def _invoke(*flags: str, src: str) -> subprocess.CompletedProcess:
    result = subprocess.run(
        [sys.executable, "-m", "dpm_trace.cli", "compare", "--color", "never"]
        + list(flags),
        capture_output=True,
        text=True,
        env={**__import__("os").environ, "PYTHONPATH": src},
    )
    if result.returncode != 0:
        raise RuntimeError(f"rc={result.returncode}\n{result.stderr}")
    return result


def json_compare(src: str, *flags: str) -> dict:
    return json.loads(_invoke("--print-json", *flags, src=src).stdout)


def text_compare(src: str, *flags: str) -> str:
    return _invoke(*flags, src=src).stdout


# ── main ──────────────────────────────────────────────────────────────────────


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-compare.py <repo-root>", file=sys.stderr)
        return 2

    repo_root = Path(sys.argv[1]).resolve()
    src = str(repo_root / "src")
    fix = repo_root / "tests" / "fixtures" / "compare"
    a = str(fix / "trace-a.json")
    b = str(fix / "trace-b.json")
    prep = str(fix / "prepared.json")
    fail = str(fix / "completion-fail.json")
    ok = str(fix / "completion-ok.json")

    # ── 1. update-vs-update ───────────────────────────────────────────────────
    section("update-vs-update")
    uvu = json_compare(src, a, b)
    left = uvu.get("left") or {}
    right = uvu.get("right") or {}
    diff = uvu.get("diff") or {}
    lc = (diff.get("eventCounts") or {}).get("left") or {}
    rc = (diff.get("eventCounts") or {}).get("right") or {}
    ol = diff.get("templatesOnlyInLeft") or []
    or_ = diff.get("templatesOnlyInRight") or []
    lr = (diff.get("rootEvents") or {}).get("left") or []
    rr = (diff.get("rootEvents") or {}).get("right") or []

    check("kind", uvu.get("kind") == "update-vs-update", uvu.get("kind"))
    check(
        "left.updateId",
        left.get("updateId") == "update-fixture-trace-a-0001",
        left.get("updateId"),
    )
    check(
        "right.updateId",
        right.get("updateId") == "update-fixture-trace-b-0002",
        right.get("updateId"),
    )
    check("left.events", left.get("events") == 1, left.get("events"))
    check("right.events", right.get("events") == 2, right.get("events"))
    check(
        "left counts  (create=1,exercise=0)",
        lc.get("create") == 1 and lc.get("exercise") == 0,
        f"create={lc.get('create')}, exercise={lc.get('exercise')}",
    )
    check(
        "right counts (create=1,exercise=1)",
        rc.get("create") == 1 and rc.get("exercise") == 1,
        f"create={rc.get('create')}, exercise={rc.get('exercise')}",
    )
    check(
        "templatesOnlyInLeft  has Counter", any("Counter:Counter" in t for t in ol), ol
    )
    check("templatesOnlyInRight has Token", any("Token:Token" in t for t in or_), or_)
    check(
        "Counter not in templatesOnlyInRight",
        not any("Counter:Counter" in t for t in or_),
    )
    check(
        "Token    not in templatesOnlyInLeft", not any("Token:Token" in t for t in ol)
    )
    check(
        "left  roots[0].kind",
        bool(lr) and lr[0].get("kind") == "create",
        lr[0].get("kind") if lr else None,
    )
    check(
        "right roots[0].kind",
        bool(rr) and rr[0].get("kind") == "exercise",
        rr[0].get("kind") if rr else None,
    )
    check(
        "right roots[0].choice",
        bool(rr) and rr[0].get("choice") == "Transfer",
        rr[0].get("choice") if rr else None,
    )

    # ── 2. prepared-vs-update ─────────────────────────────────────────────────
    section("prepared-vs-update")
    pvu = json_compare(src, "--prepared", prep, "--update", a)
    left = pvu.get("left") or {}
    right = pvu.get("right") or {}
    diff = pvu.get("diff") or {}
    cc = diff.get("commandCount") or {}
    sd = diff.get("stateDiff") or {}
    cmds = diff.get("commands") or []
    rel = diff.get("rootEvents") or []
    hsh = left.get("preparedTransactionHash")

    check("kind", pvu.get("kind") == "prepared-vs-update", pvu.get("kind"))
    check(
        "left.commandId",
        left.get("commandId") == "dpm-trace-prepare-001",
        left.get("commandId"),
    )
    check("left.committed", left.get("committed") is False, left.get("committed"))
    check("left.commands", left.get("commands") == 1, left.get("commands"))
    check("left.preparedTxHash", hsh is not None, (hsh[:16] + "...") if hsh else None)
    check(
        "right.updateId",
        right.get("updateId") == "update-fixture-trace-a-0001",
        right.get("updateId"),
    )
    check("right.events", right.get("events") == 1, right.get("events"))
    check("commandCount.prepared", cc.get("prepared") == 1, cc.get("prepared"))
    check(
        "commandCount.rootEvents",
        cc.get("updateRootEvents") == 1,
        cc.get("updateRootEvents"),
    )
    check("stateDiff.create", sd.get("create") == 1, sd.get("create"))
    check("stateDiff.exercise", sd.get("exercise") == 0, sd.get("exercise"))
    check("stateDiff.archive", sd.get("archive") == 0, sd.get("archive"))
    check(
        "commands[0].kind",
        bool(cmds) and cmds[0].get("kind") == "create",
        cmds[0].get("kind") if cmds else None,
    )
    check(
        "commands[0].template",
        bool(cmds) and cmds[0].get("template") == "pkg1aabb:Counter:Counter",
        cmds[0].get("template") if cmds else None,
    )
    check(
        "rootEvents[0].kind",
        bool(rel) and rel[0].get("kind") == "create",
        rel[0].get("kind") if rel else None,
    )

    # ── 3. prepared-vs-completion (failed) ────────────────────────────────────
    section("prepared-vs-completion (failed)")
    pvc = json_compare(src, "--prepared", prep, "--completion-file", fail)
    left = pvc.get("left") or {}
    right = pvc.get("right") or {}
    diff = pvc.get("diff") or {}

    check("kind", pvc.get("kind") == "prepared-vs-completion", pvc.get("kind"))
    check(
        "left.commandId",
        left.get("commandId") == "dpm-trace-prepare-001",
        left.get("commandId"),
    )
    check("left.committed", left.get("committed") is False, left.get("committed"))
    check(
        "right.commandId",
        right.get("commandId") == "dpm-trace-fail-001",
        right.get("commandId"),
    )
    check(
        "right.statusCode",
        right.get("statusCode") == "FAILED_PRECONDITION",
        right.get("statusCode"),
    )
    check(
        "right.message",
        right.get("message") == "count must be non-negative",
        right.get("message"),
    )
    check(
        "right.updateId absent",
        right.get("updateId") is None,
        right.get("updateId") or "(absent)",
    )
    check(
        "committedUpdateAvailable",
        diff.get("committedUpdateAvailable") is False,
        diff.get("committedUpdateAvailable"),
    )

    # ── 4. prepared-vs-completion (committed) ─────────────────────────────────
    section("prepared-vs-completion (committed)")
    pvc_ok = json_compare(src, "--prepared", prep, "--completion-file", ok)
    right = pvc_ok.get("right") or {}
    diff = pvc_ok.get("diff") or {}

    check("kind", pvc_ok.get("kind") == "prepared-vs-completion", pvc_ok.get("kind"))
    check("right.commandId", right.get("commandId") == "dpm-trace-ok-001", right.get("commandId"))
    check("right.statusCode", right.get("statusCode") == "OK", right.get("statusCode"))
    check(
        "right.updateId present",
        right.get("updateId") == "update-fixture-trace-a-0001",
        right.get("updateId"),
    )
    check(
        "committedUpdateAvailable",
        diff.get("committedUpdateAvailable") is True,
        diff.get("committedUpdateAvailable"),
    )

    # ── 5. --print-json shape stability ──────────────────────────────────────
    section("--print-json shape stability")
    required = ["kind", "left", "right", "diff"]
    for label, data in [
        ("update-vs-update", uvu),
        ("prepared-vs-update", pvu),
        ("prepared-vs-completion", pvc),
    ]:
        present = sorted(k for k in required if k in data)
        check(f"{label} keys", all(k in data for k in required), present)

    # ── 6. CLI output (human-readable, no --print-json) ──────────────────────
    rendered_cases = [
        (
            "cli output — update-vs-update",
            [a, b],
            [
                ("header", "DPM trace comparison"),
                ("kind label", "update-vs-update"),
                ("differences banner", "visible differences"),
                ("Counter:Counter", "Counter:Counter"),
                ("Token:Token", "Token:Token"),
            ],
        ),
        (
            "cli output — prepared-vs-update",
            ["--prepared", prep, "--update", a],
            [
                ("header", "DPM trace comparison"),
                ("kind label", "prepared-vs-update"),
                ("no visible diff", "no visible differences"),
                ("Counter:Counter", "Counter:Counter"),
                ("Committed state diff", "Committed state diff"),
            ],
        ),
        (
            "cli output — prepared-vs-completion (fail)",
            ["--prepared", prep, "--completion-file", fail],
            [
                ("header", "DPM trace comparison"),
                ("kind label", "prepared-vs-completion"),
                ("completion failed", "completion failed"),
                ("status code", "FAILED_PRECONDITION"),
                ("error message", "count must be non-negative"),
            ],
        ),
        (
            "cli output — prepared-vs-completion (ok)",
            ["--prepared", prep, "--completion-file", ok],
            [("completion committed", "completion committed")],
        ),
    ]
    for title, flags, needles in rendered_cases:
        section(title)
        text = text_compare(src, *flags)
        for label, needle in needles:
            check(label, needle in text, needle)

    # ── result ────────────────────────────────────────────────────────────────
    print()
    if _errors:
        print("dpm trace compare checks FAILED:")
        for err in _errors:
            print(f"  - {err}")
        return 1
    print("dpm trace compare checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
