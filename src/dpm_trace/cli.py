from __future__ import annotations

import argparse
import html
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import textwrap
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from uuid import uuid4


SCAN_UPDATE_PATH = "/v2/updates/{update_id}"
LEDGER_UPDATE_BY_ID_PATH = "/v2/updates/update-by-id"
LEDGER_ACTIVE_CONTRACTS_PATH = "/v2/state/active-contracts"
LEDGER_INTERACTIVE_PREPARE_PATH = "/v2/interactive-submission/prepare"
LEDGER_COMPLETIONS_PATH = "/v2/commands/completions"
TRACE_ARTIFACT_SCHEMA = "dpm-trace/trace-artifact/v0"
PREPARED_ARTIFACT_SCHEMA = "dpm-trace/prepared-artifact/v0"


@dataclass
class TraceEvent:
    event_id: str
    kind: str
    template: str | None = None
    contract_id: str | None = None
    choice: str | None = None
    consuming: bool | None = None
    acting_parties: list[str] = field(default_factory=list)
    witnesses: list[str] = field(default_factory=list)
    signatories: list[str] = field(default_factory=list)
    observers: list[str] = field(default_factory=list)
    child_event_ids: list[str] = field(default_factory=list)
    payload: Any = None
    argument: Any = None
    result: Any = None
    raw: dict[str, Any] = field(default_factory=dict)


@dataclass
class NormalizedTrace:
    update_id: str
    source: str
    source_url: str | None
    projection: dict[str, Any]
    root_event_ids: list[str]
    events_by_id: dict[str, TraceEvent]
    record_time: str | None = None
    offset: str | None = None
    synchronizer_id: str | None = None
    raw: dict[str, Any] = field(default_factory=dict)


@dataclass
class SourceLocation:
    path: str
    line: int
    label: str
    column: int = 1
    end_line: int | None = None
    end_column: int | None = None


@dataclass
class SourceLine:
    path: str
    line: int
    text: str


@dataclass
class ExpressionStep:
    line: SourceLine
    label: str
    expression: str
    variables: dict[str, Any] = field(default_factory=dict)
    result: Any = None
    note: str | None = None


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if argv and argv[0] == "open":
        return open_main(argv[1:])
    if argv and argv[0] == "prepare":
        return prepare_main(argv[1:])
    if argv and argv[0] == "compare":
        return compare_main(argv[1:])
    if argv and argv[0] == "test":
        return test_main(argv[1:])

    parser = build_trace_parser()
    args = parser.parse_args(argv)
    return run_trace(args)


def build_trace_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="dpm trace",
        description="POC for participant-scoped Canton transaction visualization.",
        epilog=(
            "Subcommands: "
            "dpm trace open <trace.json>, "
            "dpm trace prepare --commands commands.json, "
            "dpm trace --command-id <command-id>, "
            "dpm trace --completion-file completion.json, "
            "dpm trace compare <update-a> <update-b>."
        ),
    )
    parser.add_argument("target", nargs="?", help="Update id or CantonScan update URL.")
    parser.add_argument("--command-id", help="Command id for completion lookup when no update id exists.")
    parser.add_argument("--completion-file", help="JSON file containing a captured completion response/artifact.")
    parser.add_argument("--act-as", action="append", default=[], help="Submitting party for completion lookup. Repeatable.")
    parser.add_argument("--completion-user-id", help="Ledger API user id for completion lookup.")
    parser.add_argument("--begin-exclusive", default="0", help="Minimum completion offset to query from. Defaults to 0.")
    parser.add_argument("--completion-limit", type=int, default=100, help="Maximum completions to scan. Defaults to 100.")
    parser.add_argument("--completion-timeout-ms", type=int, default=1000, help="Completion query idle timeout. Defaults to 1000.")
    parser.add_argument("--log-file", action="append", default=[], help="Operator/application log file to attach and correlate. Repeatable.")
    parser.add_argument("--visualize", action="store_true", help="Open the interactive transaction visualizer.")
    add_common_connection_args(parser)
    parser.add_argument("--export", "--out", dest="export", help="Write a portable trace artifact JSON file.")
    parser.add_argument("--print-json", action="store_true", help="Print normalized trace JSON and exit.")
    parser.add_argument("--explain-apis", action="store_true", help="Explain Scan API vs Ledger API.")
    return parser


def add_common_connection_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--scan-url", help="Scan API base URL, e.g. https://.../api/scan.")
    parser.add_argument("--ledger-url", help="Ledger JSON API base URL, e.g. http://localhost:7575.")
    parser.add_argument("--participant-url", dest="ledger_url", help="Alias for --ledger-url.")
    parser.add_argument("--submitter", dest="ledger_url", help="Alias for --ledger-url / --participant-url.")
    parser.add_argument("--token-file", help="Bearer token file for Ledger JSON API.")
    parser.add_argument("--access-token-file", dest="token_file", help="Alias for --token-file.")
    parser.add_argument("--token", help="Bearer token for Ledger JSON API.")
    parser.add_argument("--read-as", action="append", default=[], help="Party to read as. Repeatable.")
    parser.add_argument("--party", action="append", default=[], help="Alias for --read-as.")
    parser.add_argument("--dar", action="append", default=[], help="Local DAR to attach as package metadata. Repeatable.")
    parser.add_argument("--damlc", help="Daml assistant or damlc executable for package inspection. Defaults to daml.")
    parser.add_argument("--debug-info", action="append", default=[], help=argparse.SUPPRESS)
    parser.add_argument("--daml-yaml", action="append", default=[], help="Path to daml.yaml for local source diagnostics. Repeatable.")
    parser.add_argument("--source-root", action="append", default=[], help="Local Daml source root for source diagnostics. Repeatable.")
    parser.add_argument(
        "--config",
        help="Trace config JSON. Defaults to .dpm-trace.json found in the current directory or a parent.",
    )
    parser.add_argument(
        "--color",
        choices=["auto", "always", "never"],
        default="auto",
        help="Colorize pretty trace output. Defaults to auto.",
    )
    parser.add_argument("--source", choices=["auto", "scan", "ledger"], default="auto")


def open_main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        prog="dpm trace open",
        description="Open an exported dpm trace artifact.",
    )
    parser.add_argument("artifact", help="Path to an exported trace artifact JSON file.")
    parser.add_argument("--visualize", action="store_true", help="Open the interactive transaction visualizer.")
    parser.add_argument("--print-json", action="store_true", help="Print the artifact JSON and exit.")
    parser.add_argument("--color", choices=["auto", "always", "never"], default="auto")
    parser.add_argument("--debug-info", action="append", default=[], help=argparse.SUPPRESS)
    args = parser.parse_args(argv)
    return run_open(args)


def prepare_main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        prog="dpm trace prepare",
        description="Prepare and visualize a non-committed Canton command result.",
    )
    add_common_connection_args(parser)
    parser.add_argument("--commands", help="JSON file containing a commands array or an object with a commands field.")
    parser.add_argument("--command-json", help="Raw JSON command envelope or commands array.")
    parser.add_argument("--act-as", action="append", default=[], help="Submitting party. Repeatable.")
    parser.add_argument("--template", help="Template id for an explicit command.")
    parser.add_argument("--choice", help="Choice name for an explicit exercise command.")
    parser.add_argument("--contract-id", help="Contract id for an explicit exercise command.")
    parser.add_argument("--args-json", help="JSON object/value to use as create arguments or choice argument.")
    parser.add_argument("--args-file", help="File containing JSON arguments.")
    parser.add_argument("--arg", action="append", default=[], help="Set one argument field, e.g. --arg count=1. Repeatable.")
    parser.add_argument("--command-id", help="Command id. Defaults to dpm-trace-prepare-<uuid>.")
    parser.add_argument("--user-id", help="Ledger API user id for PrepareSubmission.")
    parser.add_argument("--synchronizer-id", default="", help="Optional synchronizer id.")
    parser.add_argument("--export", "--out", dest="export", help="Write a prepared transaction artifact JSON file.")
    parser.add_argument("--print-json", action="store_true", help="Print the raw request and response.")
    args = parser.parse_args(argv)
    try:
        return run_prepare(args)
    except Exception as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


def compare_main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        prog="dpm trace compare",
        description="Compare prepared transactions, successful transactions, or completion/error files.",
    )
    parser.add_argument("updates", nargs="*", help="Two update ids to compare.")
    parser.add_argument("--prepared", help="Prepared transaction artifact produced by dpm trace prepare.")
    parser.add_argument("--update", help="Successful transaction update id to compare against --prepared.")
    parser.add_argument("--completion", dest="completion", help="Deprecated alias for --command-id.")
    parser.add_argument("--command-id", dest="completion", help="Command id for a failed/successful completion.")
    parser.add_argument("--completion-file", help="JSON file containing a completion response/artifact.")
    parser.add_argument("--completion-user-id", help="Ledger API user id for completion lookup.")
    parser.add_argument("--begin-exclusive", default="0", help="Minimum completion offset to query from. Defaults to 0.")
    parser.add_argument("--completion-limit", type=int, default=100, help="Maximum completions to scan. Defaults to 100.")
    parser.add_argument("--completion-timeout-ms", type=int, default=1000, help="Completion query idle timeout. Defaults to 1000.")
    parser.add_argument("--log-file", action="append", default=[], help="Operator/application log file to attach and correlate. Repeatable.")
    add_common_connection_args(parser)
    parser.add_argument("--act-as", action="append", default=[], help="Submitting party for completion lookup. Repeatable.")
    parser.add_argument("--print-json", action="store_true", help="Print machine-readable comparison JSON.")
    args = parser.parse_args(argv)
    try:
        return run_compare(args)
    except Exception as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


@dataclass
class TestCaseResult:
    name: str
    classname: str
    status: str
    message: str | None = None
    time: float | None = None
    transactions_text: str | None = None
    stats: dict[str, int] = field(default_factory=dict)
    touched_locations: list[str] = field(default_factory=list)
    diagnostics: list[SourceLocation] = field(default_factory=list)


def test_main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        prog="dpm trace test",
        description="Run Daml Script unit tests and render source-mapped trace results.",
        epilog=(
            "Runs `daml test` on a Daml package, decodes each script's transaction tree, "
            "and maps failed tests back to source. Returns a non-zero exit code when any "
            "test fails, so it works as a CI gate."
        ),
    )
    parser.add_argument("package_root", nargs="?", help="Daml package directory containing daml.yaml. Defaults to the current directory.")
    parser.add_argument("--daml", help="Daml assistant, damlc, or dpm executable used to run the tests. Defaults to daml.")
    parser.add_argument("--files", action="append", default=[], help="Restrict the run to these .daml files. Repeatable.")
    parser.add_argument("-p", "--test-pattern", dest="test_pattern", help="Only run test declarations matching this pattern.")
    parser.add_argument("--junit", help="Also copy the JUnit XML result to this path for CI consumption.")
    parser.add_argument("--no-trees", action="store_true", help="Suppress transaction-tree rendering; show the summary and failures only.")
    parser.add_argument("--keep-artifacts", action="store_true", help="Keep the temporary directory with JUnit/transaction/table outputs.")
    parser.add_argument("--print-json", action="store_true", help="Print a machine-readable test report and exit.")
    parser.add_argument("--dar", action="append", default=[], help="Built DAR used to verify failure text via damlc inspect. Repeatable.")
    parser.add_argument("--damlc", help="damlc/daml executable for inspect verification. Defaults to --daml.")
    parser.add_argument("--daml-yaml", action="append", default=[], help="Override the daml.yaml used for source diagnostics. Repeatable.")
    parser.add_argument("--source-root", action="append", default=[], help="Extra Daml source roots for diagnostics. Repeatable.")
    parser.add_argument("--debug-info", action="append", default=[], help=argparse.SUPPRESS)
    parser.add_argument("--config", help="Trace config JSON. Defaults to .dpm-trace.json found in the current directory or a parent.")
    parser.add_argument("--color", choices=["auto", "always", "never"], default="auto")
    args = parser.parse_args(argv)
    try:
        return run_test(args)
    except Exception as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2


def run_test(args: argparse.Namespace) -> int:
    apply_config_defaults(args, load_config(getattr(args, "config", None)))
    color = Color.from_mode(args.color)
    root = resolve_package_root(args)
    daml_yaml = root / "daml.yaml"
    if not daml_yaml.exists():
        print(f"error: no daml.yaml found in {root}; pass a package directory", file=sys.stderr)
        return 2
    if not getattr(args, "daml_yaml", None):
        args.daml_yaml = [str(daml_yaml)]
    if not getattr(args, "damlc", None):
        args.damlc = args.daml

    work = Path(tempfile.mkdtemp(prefix="dpm-trace-test-"))
    junit_path = work / "results.xml"
    txns_dir = work / "transactions"
    table_dir = work / "tables"
    try:
        command, env = daml_test_command(args, root, junit_path, txns_dir, table_dir)
        completed = subprocess.run(
            command,
            cwd=str(root),
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        if not junit_path.exists():
            sys.stderr.write(completed.stdout or "")
            print(
                f"\nerror: '{' '.join(display_command(command))}' produced no test results "
                f"(exit {completed.returncode}); the package may not compile",
                file=sys.stderr,
            )
            return 2

        cases = parse_junit(junit_path)
        source_index = source_index_from_args(args, None)
        for case in cases:
            if case.status == "passed":
                html_file = txns_dir / f"transaction-{case.name}.html"
                if html_file.exists():
                    case.transactions_text = transaction_html_to_text(html_file.read_text(encoding="utf-8", errors="replace"))
                    case.stats = transaction_stats(case.transactions_text)
                    case.touched_locations = transaction_locations(case.transactions_text)
            else:
                case.diagnostics = test_failure_locations(case.message or "", source_index)

        if getattr(args, "junit", None):
            shutil.copyfile(junit_path, args.junit)

        if args.print_json:
            print(json.dumps(test_report_json(args, root, command, cases), indent=2, sort_keys=True))
        else:
            print_test_report(args, root, command, cases, color, source_index)
        return 1 if any(case.status != "passed" for case in cases) else 0
    finally:
        if getattr(args, "keep_artifacts", False):
            if not args.print_json:
                print(f"\nartifacts kept in: {work}")
        else:
            shutil.rmtree(work, ignore_errors=True)


def resolve_package_root(args: argparse.Namespace) -> Path:
    candidate = getattr(args, "package_root", None) or "."
    return Path(candidate).expanduser().resolve()


def daml_test_command(
    args: argparse.Namespace,
    root: Path,
    junit_path: Path,
    txns_dir: Path,
    table_dir: Path,
) -> tuple[list[str], dict[str, str]]:
    executable = str(Path(args.daml or "daml").expanduser())
    name = Path(executable).name
    if name == "damlc":
        command = [executable, "test", "--package-root", str(root)]
    else:
        command = [executable, "test"]
        if name == "daml":
            command.append("--no-legacy-assistant-warning")
    command += [
        "--junit", str(junit_path),
        "--transactions-output", str(txns_dir),
        "--table-output", str(table_dir),
    ]
    if getattr(args, "test_pattern", None):
        command += ["--test-pattern", args.test_pattern]
    files = [f for f in getattr(args, "files", []) or [] if f]
    if files:
        command.append("--files")
        command += files
    return command, daml_child_env({"DAML_PACKAGE": str(root)})


def daml_child_env(extra: dict[str, str] | None = None) -> dict[str, str]:
    """Environment for spawning daml/damlc.

    When dpm runs this plugin it exports DPM_RESOLUTION_FILE, a resolution context
    scoped to the dpm-trace component. A child daml/damlc would wrongly apply it to
    the package under test ("Failed to find DPM package resolution"), so we drop it
    and let daml resolve the target package on its own.
    """
    env = dict(os.environ)
    for key in ("DPM_RESOLUTION_FILE",):
        env.pop(key, None)
    if extra:
        env.update(extra)
    return env


def display_command(command: list[str]) -> list[str]:
    if not command:
        return command
    cleaned: list[str] = [Path(command[0]).name]
    skip_next = False
    for token in command[1:]:
        if skip_next:
            skip_next = False
            continue
        if token in ("--junit", "--transactions-output", "--table-output", "--package-root"):
            skip_next = True
            continue
        if token == "--no-legacy-assistant-warning":
            continue
        cleaned.append(token)
    return cleaned


def parse_junit(path: Path) -> list[TestCaseResult]:
    tree = ET.parse(str(path))
    results: list[TestCaseResult] = []
    for suite in tree.getroot().iter("testsuite"):
        suite_name = suite.get("name") or ""
        for case in suite.findall("testcase"):
            failure = case.find("failure")
            error = case.find("error")
            if failure is not None:
                status, node = "failed", failure
            elif error is not None:
                status, node = "error", error
            elif case.find("skipped") is not None:
                status, node = "skipped", None
            else:
                status, node = "passed", None
            message = None
            if node is not None:
                message = (node.get("message") or node.text or "").strip() or None
            time_attr = case.get("time")
            results.append(
                TestCaseResult(
                    name=case.get("name") or "?",
                    classname=case.get("classname") or suite_name,
                    status=status,
                    message=message,
                    time=float(time_attr) if time_attr else None,
                )
            )
    return results


def transaction_html_to_text(html_text: str) -> str:
    text = re.sub(r"<style.*?</style>", "", html_text, flags=re.S)
    text = re.sub(r"(?i)<br\s*/?>", "\n", text)
    text = re.sub(r"<[^>]+>", "", text)
    text = html.unescape(text)
    lines = [line.rstrip() for line in text.splitlines()]
    while lines and not lines[0].strip():
        lines.pop(0)
    while lines and not lines[-1].strip():
        lines.pop()
    return "\n".join(lines)


def transaction_stats(text: str) -> dict[str, int]:
    return {
        "transactions": len(re.findall(r"(?m)^\s*TX\s+\d+", text)),
        "creates": len(re.findall(r"\bcreates\b", text)),
        "exercises": len(re.findall(r"\bexercises\b", text)),
        "archives": len(re.findall(r"consumed by:", text)),
        "expectedFailures": len(re.findall(r"\bmustFailAt\b", text)),
    }


def transaction_locations(text: str) -> list[str]:
    seen: list[str] = []
    for match in re.finditer(r"\(([A-Za-z0-9_.']+:\d+:\d+)\)", text):
        loc = match.group(1)
        if loc not in seen:
            seen.append(loc)
    return seen


def test_failure_locations(message: str, source_index: SourceIndex) -> list[SourceLocation]:
    """Resolve a failed test's message to source.

    Daml usually stamps the submit call site as Module:line:col (the "where"); the
    message body often also carries the assertMsg/abort literal that caused the
    rejection, which we match back into source (typically the contract — the "why").
    We surface both, call sites first.
    """
    locations: list[SourceLocation] = []
    seen: set[tuple[str, int, int]] = set()

    def add(loc: SourceLocation) -> None:
        key = (loc.path, loc.line, loc.column)
        if key not in seen:
            seen.add(key)
            locations.append(loc)

    # Explicit Module:line[:col] coordinates that Daml puts in the message (call sites).
    for match in re.finditer(r"([A-Za-z0-9_.']+):(\d+)(?::(\d+))?", message or ""):
        module = match.group(1)
        files = source_index.module_files.get(module)
        if not files:
            continue
        line = int(match.group(2))
        column = int(match.group(3) or 1)
        for path in files:
            add(SourceLocation(path, line, f"daml test: {module}", column))

    # The rejection text (assertMsg/abort/===) matched back into source (often the contract).
    for needle in completion_source_needles(strip_canton_error_decoration(message or "")):
        for loc in source_index.find_failure_text(needle):
            add(loc)
            if len(locations) >= 6:
                return locations[:6]
    return locations[:6]


def strip_canton_error_decoration(message: str) -> str:
    """Reduce a Daml/Canton failure message to the user-authored text.

    Turns e.g. "... DA.Exception.AssertionFailed:AssertionFailed: Insufficient
    balance Using Canton Error Category InvalidGivenCurrentSystemStateOther" into
    "Insufficient balance", so the source search matches the literal in the contract.
    """
    text = re.sub(r"\s+Using Canton Error Category.*$", "", message or "").strip()
    for marker in ("AssertionFailed:", "Aborted:", "Failed with status:"):
        if marker in text:
            text = text.split(marker)[-1].strip()
    return text


def test_result_banner(passed: int, failed: int, total: int, color: Color) -> str:
    if failed:
        return color.apply(f"{failed} failed, {passed} passed, {total} total", "red", "bold")
    return color.apply(f"all {passed} passed ({total} total)", "green", "bold")


def test_status_icon(status: str, color: Color) -> str:
    if status == "passed":
        return color.apply("PASS", "green", "bold")
    if status == "skipped":
        return color.apply("SKIP", "gray", "bold")
    return color.apply("FAIL", "red", "bold")


def test_stats_text(stats: dict[str, int], color: Color) -> str:
    if not stats or not stats.get("transactions"):
        return color.apply("no transactions", "gray")
    parts = [f"{stats.get('transactions', 0)} tx"]
    if stats.get("creates"):
        parts.append(color.apply(f"+{stats['creates']} create", "green"))
    if stats.get("exercises"):
        parts.append(color.apply(f">{stats['exercises']} exercise", "yellow"))
    if stats.get("archives"):
        parts.append(color.apply(f"x{stats['archives']} archive", "red"))
    if stats.get("expectedFailures"):
        parts.append(color.apply(f"!{stats['expectedFailures']} expected-fail", "blue"))
    return "  ".join(parts)


def print_test_report(
    args: argparse.Namespace,
    root: Path,
    command: list[str],
    cases: list[TestCaseResult],
    color: Color,
    source_index: SourceIndex,
) -> None:
    passed = [c for c in cases if c.status == "passed"]
    failed = [c for c in cases if c.status in ("failed", "error")]

    print(color.apply("DPM trace test", "bold"))
    print(f"  package:  {root}")
    print(f"  command:  {' '.join(display_command(command))}")
    print(f"  result:   {test_result_banner(len(passed), len(failed), len(cases), color)}")
    print("")

    print(color.apply("Results", "cyan", "bold"))
    width = max((len(c.name) for c in cases), default=0)
    for case in cases:
        icon = test_status_icon(case.status, color)
        line = f"  {icon}  {case.name:<{width}}"
        if case.status == "passed":
            line += "  " + test_stats_text(case.stats, color)
        print(line)
        if case.status in ("failed", "error"):
            print(f"        {color.apply('message:', 'gray')} {case.message or '-'}")
            for loc in case.diagnostics:
                snippet = render_source_diagnostic(loc, source_index, color)
                print(textwrap.indent(snippet, "        "))
            if not case.diagnostics:
                print(f"        {color.apply('source:', 'gray')} no matching local source found")

    if not getattr(args, "no_trees", False):
        trees = [c for c in cases if c.transactions_text]
        if trees:
            print("")
            print(color.apply("Transaction trees", "cyan", "bold"))
            for case in trees:
                print("")
                print("  " + color.apply(f"── {case.name} ──", "magenta", "bold"))
                print(textwrap.indent(case.transactions_text, "  "))


def test_report_json(
    args: argparse.Namespace,
    root: Path,
    command: list[str],
    cases: list[TestCaseResult],
) -> dict[str, Any]:
    passed = sum(1 for c in cases if c.status == "passed")
    failed = sum(1 for c in cases if c.status == "failed")
    errored = sum(1 for c in cases if c.status == "error")
    return {
        "schema": "dpm-trace/test-report/v0",
        "package": str(root),
        "command": command,
        "summary": {
            "total": len(cases),
            "passed": passed,
            "failed": failed,
            "errored": errored,
            "ok": failed == 0 and errored == 0,
        },
        "tests": [
            {
                "name": case.name,
                "classname": case.classname,
                "status": case.status,
                "time": case.time,
                "message": case.message,
                "stats": case.stats,
                "touchedLocations": case.touched_locations,
                "diagnostics": [
                    {"path": loc.path, "line": loc.line, "column": loc.column, "basis": loc.label}
                    for loc in case.diagnostics
                ],
                "transactions": case.transactions_text,
            }
            for case in cases
        ],
    }


def run_trace(args: argparse.Namespace) -> int:
    try:
        apply_config_defaults(args, load_config(args.config))
    except Exception as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    if args.explain_apis:
        print(explain_apis())
        if not args.target and not args.command_id:
            return 0

    if args.command_id or args.completion_file:
        try:
            if args.completion_file:
                data = json.loads(Path(args.completion_file).read_text(encoding="utf-8"))
                if not isinstance(data, dict):
                    raise ValueError("--completion-file must contain a JSON object")
                completion = attach_log_matches(args, normalize_completion(data))
            else:
                completion = attach_log_matches(args, fetch_completion_by_command_id(args, args.command_id))
        except Exception as exc:
            print(f"error: {exc}", file=sys.stderr)
            return 1
        if args.print_json:
            print(json.dumps(completion, indent=2, sort_keys=True))
            return 0
        print_completion_trace(completion, color=Color.from_mode(args.color), source_index=source_index_from_args(args, None))
        if args.visualize:
            print("")
            print("No transaction tree is available from completion data alone. Use dpm trace <update-id> if the completion includes an update id.")
        return 0

    try:
        update_id = extract_update_id(args.target)
        parties = parse_parties(args.read_as + args.party)
        raw, source, source_url = load_update(args, update_id, parties)
        trace = normalize_trace(raw, source=source, source_url=source_url, parties=parties)
    except Exception as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    if args.print_json:
        print(json.dumps(trace_to_json(trace), indent=2, sort_keys=True))
        return 0

    if getattr(args, "export", None):
        artifact = create_trace_artifact(args, trace)
        out_path = Path(args.export)
        out_path.write_text(json.dumps(artifact, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(f"wrote trace artifact: {out_path}")

    if getattr(args, "visualize", False):
        Stepper(
            trace,
            bundle=None,
            source_index=source_index_from_args(args, None),
            color=Color.from_mode(args.color),
        ).run()
        return 0

    print_pretty_trace(trace, color=Color.from_mode(args.color), source_index=source_index_from_args(args, None))
    return 0


def run_open(args: argparse.Namespace) -> int:
    try:
        artifact = load_trace_artifact(Path(args.artifact))
        trace = trace_from_artifact(artifact)
    except Exception as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    if args.print_json:
        print(json.dumps(artifact, indent=2, sort_keys=True))
        return 0

    print(trace_artifact_summary(artifact))
    if args.visualize:
        Stepper(trace, bundle=artifact, source_index=source_index_from_args(args, artifact), color=Color.from_mode(args.color)).run()
        return 0
    print_pretty_trace(trace, color=Color.from_mode(args.color), source_index=source_index_from_args(args, artifact))
    return 0


def run_prepare(args: argparse.Namespace) -> int:
    apply_config_defaults(args, load_config(args.config))
    commands = prepare_commands(args)
    act_as = parse_parties(args.act_as)
    if not act_as:
        raise ValueError("--act-as is required")
    read_as = [party for party in parse_parties(args.read_as + args.party) if party not in act_as]
    ledger_url = participant_ledger_url(args)
    token = args.token or read_token_file(args.token_file)
    request = {
        "commandId": args.command_id or f"dpm-trace-prepare-{uuid4().hex[:12]}",
        "commands": commands,
        "actAs": act_as,
        "readAs": read_as,
        "disclosedContracts": [],
        "synchronizerId": args.synchronizer_id or "",
        "packageIdSelectionPreference": [],
        "verboseHashing": True,
    }
    user_id = prepare_user_id(args)
    if user_id:
        request["userId"] = user_id

    url = join_url(ledger_url, LEDGER_INTERACTIVE_PREPARE_PATH)
    response = http_json("POST", url, body=request, token=token)
    artifact = create_prepared_artifact(args, request, response, url)

    if args.export:
        out_path = Path(args.export)
        out_path.write_text(json.dumps(artifact, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(f"wrote prepared artifact: {out_path}")

    if args.print_json:
        print(json.dumps(artifact, indent=2, sort_keys=True))
        return 0

    print(prepared_artifact_summary(artifact))
    return 0


def run_compare(args: argparse.Namespace) -> int:
    apply_config_defaults(args, load_config(args.config))
    color = Color.from_mode(args.color)
    if args.prepared:
        prepared = load_prepared_artifact(Path(args.prepared))
        if args.update:
            trace = fetch_trace_for_compare(args, args.update)
            comparison = compare_prepared_to_trace(prepared, trace)
        elif args.completion or args.completion_file:
            completion = load_completion_for_compare(args)
            comparison = compare_prepared_to_completion(prepared, completion)
        else:
            raise ValueError("--prepared needs --update, --command-id, or --completion-file")
    elif len(args.updates) == 2:
        left = fetch_trace_for_compare(args, args.updates[0])
        right = fetch_trace_for_compare(args, args.updates[1])
        comparison = compare_traces(left, right)
    else:
        raise ValueError("usage: dpm trace compare <update-a> <update-b> or --prepared prepared.json --update <update-id>")

    if args.print_json:
        print(json.dumps(comparison, indent=2, sort_keys=True))
        return 0
    print_comparison(comparison, color, source_index=source_index_from_args(args, None))
    return 0


def prepare_commands(args: argparse.Namespace) -> list[dict[str, Any]]:
    if args.commands and args.command_json:
        raise ValueError("use only one of --commands or --command-json")
    if args.commands:
        raw = parse_json_text(Path(args.commands).read_text(encoding="utf-8"), args.commands)
        return normalize_commands_json(raw)
    if args.command_json:
        return normalize_commands_json(parse_json_text(args.command_json, "--command-json"))
    if args.template:
        return [explicit_js_command(args)]
    raise ValueError("prepare needs --commands, --command-json, or --template")


def normalize_commands_json(raw: Any) -> list[dict[str, Any]]:
    if isinstance(raw, dict) and isinstance(raw.get("commands"), list):
        return raw["commands"]
    if isinstance(raw, list):
        return raw
    if isinstance(raw, dict):
        return [raw]
    raise ValueError("commands JSON must be an object, an array, or an object with a commands field")


def create_trace_artifact(args: argparse.Namespace, trace: NormalizedTrace) -> dict[str, Any]:
    package_ids = sorted({
        package
        for ev in trace.events_by_id.values()
        for package in [package_from_template(ev.template)]
        if package
    })
    return {
        "schema": TRACE_ARTIFACT_SCHEMA,
        "createdAt": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "kind": "committed-update",
        "trace": trace_to_json(trace),
        "participant": {
            "ledgerUrl": getattr(args, "ledger_url", None),
            "scanUrl": getattr(args, "scan_url", None),
            "readAs": trace.projection.get("readAs") or [],
        },
        "packages": package_metadata_context(getattr(args, "dar", []), getattr(args, "debug_info", []), package_ids),
        "privacy": {
            "scope": trace.projection.get("note"),
            "readAs": trace.projection.get("readAs") or [],
            "missingPrivateDataPolicy": "private data outside this participant projection is not present in the artifact",
        },
    }


def create_prepared_artifact(
    args: argparse.Namespace,
    request: dict[str, Any],
    response: Any,
    source_url: str,
) -> dict[str, Any]:
    return {
        "schema": PREPARED_ARTIFACT_SCHEMA,
        "createdAt": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "kind": "prepared-command",
        "source": "ledger-json-api",
        "sourceUrl": source_url,
        "participant": {
            "ledgerUrl": getattr(args, "ledger_url", None),
            "actAs": request.get("actAs") or [],
            "readAs": request.get("readAs") or [],
        },
        "committed": False,
        "request": request,
        "response": response,
        "privacy": {
            "scope": "Prepared command result from an authorized participant endpoint.",
            "missingPrivateDataPolicy": "counterparty-private data outside this authorization context is not present in the artifact",
        },
    }


def load_trace_artifact(path: Path) -> dict[str, Any]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise ValueError(f"trace artifact must be a JSON object: {path}")
    schema = data.get("schema")
    if schema != TRACE_ARTIFACT_SCHEMA:
        raise ValueError(f"unsupported trace artifact schema in {path}: {schema!r}")
    if not isinstance(data.get("trace"), dict):
        raise ValueError(f"trace artifact is missing trace object: {path}")
    return data


def load_prepared_artifact(path: Path) -> dict[str, Any]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise ValueError(f"prepared artifact must be a JSON object: {path}")
    if data.get("schema") != PREPARED_ARTIFACT_SCHEMA:
        raise ValueError(f"unsupported prepared artifact schema in {path}: {data.get('schema')!r}")
    return data


def trace_from_artifact(artifact: dict[str, Any]) -> NormalizedTrace:
    return trace_from_json(artifact["trace"])


def trace_artifact_summary(artifact: dict[str, Any]) -> str:
    trace = artifact.get("trace") or {}
    participant = artifact.get("participant") or {}
    events = trace.get("eventsById") if isinstance(trace.get("eventsById"), dict) else {}
    return "\n".join(
        [
            "Trace artifact",
            f"  schema:       {artifact.get('schema')}",
            f"  update:       {trace.get('updateId', '-')}",
            f"  kind:         {artifact.get('kind', '-')}",
            f"  source:       {trace.get('source', '-')}",
            f"  read-as:      {', '.join(list_str(participant.get('readAs') or [])) or '-'}",
            f"  events:       {len(events)}",
        ]
    )


def prepared_artifact_summary(artifact: dict[str, Any]) -> str:
    request = artifact.get("request") or {}
    response = artifact.get("response") or {}
    lines = [
        "Prepared command",
        f"  schema:       {artifact.get('schema')}",
        f"  endpoint:     {artifact.get('sourceUrl', '-')}",
        "  committed:    no",
        f"  command id:   {request.get('commandId', '-')}",
        f"  act-as:       {', '.join(list_str(request.get('actAs') or [])) or '-'}",
        f"  read-as:      {', '.join(list_str(request.get('readAs') or [])) or '-'}",
        f"  commands:     {len(request.get('commands') or [])}",
    ]
    if isinstance(response, dict):
        tx_hash = response.get("preparedTransactionHash")
        if tx_hash:
            lines.append(f"  prepared hash:{short(str(tx_hash), 80)}")
        if response.get("costEstimation") is not None:
            lines.append("  cost:         returned")
    lines.append("")
    lines.append("This is prepared transaction data from a non-committing prepare call.")
    return "\n".join(lines)


def fetch_trace_for_compare(args: argparse.Namespace, target: str) -> NormalizedTrace:
    path = Path(target)
    if path.exists() and path.is_file():
        return trace_from_artifact(load_trace_artifact(path))
    update_id = extract_update_id(target)
    parties = parse_parties(args.read_as + args.party)
    raw, source, source_url = load_update(args, update_id, parties)
    return normalize_trace(raw, source=source, source_url=source_url, parties=parties)


def load_completion_for_compare(args: argparse.Namespace) -> dict[str, Any]:
    if args.completion_file:
        data = json.loads(Path(args.completion_file).read_text(encoding="utf-8"))
        if not isinstance(data, dict):
            raise ValueError("--completion-file must contain a JSON object")
        return attach_log_matches(args, normalize_completion(data))
    if args.completion:
        path = Path(args.completion)
        if path.exists() and path.is_file():
            data = json.loads(path.read_text(encoding="utf-8"))
            if not isinstance(data, dict):
                raise ValueError("--command-id/--completion file must contain a JSON object")
            return attach_log_matches(args, normalize_completion(data))
        return attach_log_matches(args, fetch_completion_by_command_id(args, args.completion))
    raise ValueError("provide --command-id or --completion-file")


def fetch_completion_by_command_id(args: argparse.Namespace, command_id: str) -> dict[str, Any]:
    apply_config_defaults(args, load_config(args.config))
    ledger_url = participant_ledger_url(args)
    parties = completion_lookup_parties(args)
    if not parties:
        raise ValueError("--act-as, --read-as, or --party is required for completion lookup")
    token = args.token or read_token_file(args.token_file)
    body: dict[str, Any] = {"parties": parties}
    if args.completion_user_id:
        body["userId"] = args.completion_user_id
    elif not token:
        body["userId"] = prepare_user_id(args)
    try:
        body["beginExclusive"] = int(args.begin_exclusive)
    except ValueError as exc:
        raise ValueError("--begin-exclusive must be an integer offset") from exc

    url = join_url(ledger_url, LEDGER_COMPLETIONS_PATH)
    query = f"?limit={max(args.completion_limit, 1)}&stream_idle_timeout_ms={max(args.completion_timeout_ms, 1)}"
    raw = http_json("POST", url + query, body=body, token=token)
    completions = normalize_completion_list(raw)
    for completion in completions:
        if str(completion.get("commandId") or "") == command_id:
            completion["source"] = "ledger-json-api"
            completion["sourceUrl"] = url
            completion["lookup"] = {
                "commandId": command_id,
                "parties": parties,
                "beginExclusive": body.get("beginExclusive"),
                "limit": args.completion_limit,
            }
            return completion
    raise ValueError(
        f"completion {command_id!r} not found in the queried completion window; "
        "try --begin-exclusive with an earlier offset or --completion-file with captured JSON"
    )


def completion_lookup_parties(args: argparse.Namespace) -> list[str]:
    return parse_parties(
        list(getattr(args, "act_as", []) or [])
        + list(getattr(args, "read_as", []) or [])
        + list(getattr(args, "party", []) or [])
    )


def normalize_completion_list(raw: Any) -> list[dict[str, Any]]:
    if isinstance(raw, dict):
        candidates = raw.get("completions") or raw.get("items") or raw.get("responses") or raw.get("completionResponses")
        if candidates is None:
            candidates = [raw]
    else:
        candidates = raw
    if not isinstance(candidates, list):
        return []
    result: list[dict[str, Any]] = []
    for item in candidates:
        completion = normalize_completion(item)
        if completion:
            result.append(completion)
    return result


def normalize_completion(raw: Any) -> dict[str, Any]:
    if not isinstance(raw, dict):
        return {}
    completion = raw
    for key in ("completionResponse", "Completion", "completion", "value"):
        value = completion.get(key)
        if isinstance(value, dict):
            completion = value
    if "Completion" in completion and isinstance(completion["Completion"], dict):
        return normalize_completion(completion["Completion"])
    if "Empty" in completion or "OffsetCheckpoint" in completion:
        return {}
    if not isinstance(completion, dict):
        return {}
    return completion


def attach_log_matches(args: argparse.Namespace, completion: dict[str, Any]) -> dict[str, Any]:
    if not getattr(args, "log_file", None):
        return completion
    terms = completion_correlation_terms(completion)
    matches: list[dict[str, Any]] = []
    for raw_path in args.log_file:
        path = Path(raw_path)
        try:
            lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError as exc:
            matches.append({"file": str(path), "error": str(exc)})
            continue
        for line_no, line in enumerate(lines, start=1):
            if any(term and term in line for term in terms):
                matches.append({"file": str(path), "line": line_no, "text": line[:500]})
    completion = dict(completion)
    completion["logMatches"] = matches
    completion["logMatchTerms"] = sorted(terms)
    return completion


def completion_correlation_terms(completion: dict[str, Any]) -> set[str]:
    terms: set[str] = set()
    for key in ("commandId", "command_id", "updateId", "update_id", "submissionId", "submission_id", "traceId", "correlationId"):
        value = completion.get(key)
        if value:
            terms.add(str(value))
    status = completion.get("status")
    if isinstance(status, dict):
        for key in ("traceId", "correlationId"):
            value = status.get(key)
            if value:
                terms.add(str(value))
    trace_context = completion.get("traceContext")
    if isinstance(trace_context, dict):
        for value in trace_context.values():
            if value:
                terms.add(str(value))
    return {term for term in terms if len(term) >= 6}


def compare_prepared_to_trace(prepared: dict[str, Any], trace: NormalizedTrace) -> dict[str, Any]:
    request = prepared.get("request") or {}
    commands = request.get("commands") if isinstance(request.get("commands"), list) else []
    roots = [trace.events_by_id[event_id] for event_id in trace.root_event_ids if event_id in trace.events_by_id]
    return {
        "kind": "prepared-vs-update",
        "left": prepared_summary_for_compare(prepared),
        "right": trace_summary_for_compare(trace),
        "diff": {
            "commandCount": {"prepared": len(commands), "updateRootEvents": len(roots)},
            "rootEvents": [event_compare_row(ev) for ev in roots],
            "commands": [command_compare_row(command) for command in commands if isinstance(command, dict)],
            "stateDiff": state_diff_counts(trace),
            "notes": [
                "Prepared data is not committed.",
                "The comparison checks visible command/event shape; it is not proof of semantic equivalence.",
            ],
        },
    }


def compare_prepared_to_completion(prepared: dict[str, Any], completion: dict[str, Any]) -> dict[str, Any]:
    code, message = completion_status_fields(completion)
    return {
        "kind": "prepared-vs-completion",
        "left": prepared_summary_for_compare(prepared),
        "right": {
            "commandId": pick(completion, "commandId", "command_id"),
            "updateId": pick(completion, "updateId", "update_id"),
            "offset": pick(completion, "offset"),
            "submissionId": pick(completion, "submissionId", "submission_id"),
            "statusCode": code,
            "message": message,
            "source": completion.get("source"),
            "logMatches": completion.get("logMatches") or [],
        },
        "diff": {
            "committedUpdateAvailable": bool(pick(completion, "updateId", "update_id")),
            "logMatches": completion.get("logMatches") or [],
        },
    }


def completion_status_fields(completion: dict[str, Any]) -> tuple[Any, Any]:
    status = pick(completion, "status", "completionStatus")
    if isinstance(status, dict):
        code = pick(status, "code", "grpcCode", "grpcCodeValue")
        message = pick(status, "message", "details")
        if code is not None or message is not None:
            return code, message
    elif status:
        return None, status

    code = pick(completion, "code", "grpcCode", "grpcCodeValue", "errorCategory")
    message = pick(completion, "message", "details", "cause")
    if code is not None or message is not None:
        return code, message
    if pick(completion, "updateId", "update_id"):
        return "OK", "committed"
    return None, None


def compare_traces(left: NormalizedTrace, right: NormalizedTrace) -> dict[str, Any]:
    return {
        "kind": "update-vs-update",
        "left": trace_summary_for_compare(left),
        "right": trace_summary_for_compare(right),
        "diff": {
            "eventCounts": {"left": state_diff_counts(left), "right": state_diff_counts(right)},
            "rootEvents": {
                "left": [event_compare_row(left.events_by_id[event_id]) for event_id in left.root_event_ids if event_id in left.events_by_id],
                "right": [event_compare_row(right.events_by_id[event_id]) for event_id in right.root_event_ids if event_id in right.events_by_id],
            },
            "templatesOnlyInLeft": sorted(event_templates(left) - event_templates(right)),
            "templatesOnlyInRight": sorted(event_templates(right) - event_templates(left)),
        },
    }


def trace_summary_for_compare(trace: NormalizedTrace) -> dict[str, Any]:
    return {
        "updateId": trace.update_id,
        "source": trace.source,
        "offset": trace.offset,
        "recordTime": trace.record_time,
        "readAs": trace.projection.get("readAs") or [],
        "events": len(trace.events_by_id),
    }


def prepared_summary_for_compare(prepared: dict[str, Any]) -> dict[str, Any]:
    request = prepared.get("request") or {}
    response = prepared.get("response") or {}
    return {
        "commandId": request.get("commandId"),
        "actAs": request.get("actAs") or [],
        "readAs": request.get("readAs") or [],
        "commands": len(request.get("commands") or []),
        "preparedTransactionHash": response.get("preparedTransactionHash") if isinstance(response, dict) else None,
        "committed": False,
    }


def event_compare_row(ev: TraceEvent) -> dict[str, Any]:
    value = None
    value_label = None
    if ev.kind == "create":
        value = ev.payload
        value_label = "payload"
    elif ev.kind == "exercise":
        value = ev.argument
        value_label = "argument"
    return {
        "eventId": ev.event_id,
        "kind": ev.kind,
        "template": ev.template,
        "choice": ev.choice,
        "contractId": short(ev.contract_id, 48),
        "value": value,
        "valueLabel": value_label,
    }


def command_compare_row(command: dict[str, Any]) -> dict[str, Any]:
    if isinstance(command.get("CreateCommand"), dict):
        body = command["CreateCommand"]
        return {
            "kind": "create",
            "template": body.get("templateId"),
            "value": body.get("createArguments"),
            "valueLabel": "arguments",
        }
    if isinstance(command.get("ExerciseCommand"), dict):
        body = command["ExerciseCommand"]
        return {
            "kind": "exercise",
            "template": body.get("templateId"),
            "choice": body.get("choice"),
            "contractId": short(body.get("contractId"), 48),
            "value": body.get("choiceArgument"),
            "valueLabel": "choice argument",
        }
    return {"kind": "unknown", "keys": sorted(command.keys())}


def state_diff_counts(trace: NormalizedTrace) -> dict[str, int]:
    counts = {"create": 0, "exercise": 0, "archive": 0, "other": 0}
    for ev in trace.events_by_id.values():
        if ev.kind in counts:
            counts[ev.kind] += 1
        else:
            counts["other"] += 1
    return counts


def event_templates(trace: NormalizedTrace) -> set[str]:
    return {ev.template for ev in trace.events_by_id.values() if ev.template}


def print_comparison(comparison: dict[str, Any], color: Color, source_index: SourceIndex | None = None) -> None:
    kind = comparison.get("kind")
    if kind == "update-vs-update":
        print_update_comparison(comparison, color)
        return
    if kind == "prepared-vs-update":
        print_prepared_update_comparison(comparison, color)
        return
    if kind == "prepared-vs-completion":
        print_prepared_completion_comparison(comparison, color, source_index=source_index)
        return

    print(color.apply("DPM trace comparison", "bold"))
    print(f"  kind: {kind}")
    print("")
    print(color.apply("Left", "cyan", "bold"))
    print(indent_json(comparison.get("left") or {}))
    print(color.apply("Right", "cyan", "bold"))
    print(indent_json(comparison.get("right") or {}))
    print(color.apply("Diff", "cyan", "bold"))
    print(indent_json(comparison.get("diff") or {}))


def print_update_comparison(comparison: dict[str, Any], color: Color) -> None:
    left = comparison.get("left") or {}
    right = comparison.get("right") or {}
    diff = comparison.get("diff") or {}
    counts = diff.get("eventCounts") or {}
    left_counts = counts.get("left") or {}
    right_counts = counts.get("right") or {}
    root_events = diff.get("rootEvents") or {}
    left_roots = root_events.get("left") or []
    right_roots = root_events.get("right") or []
    templates_left = diff.get("templatesOnlyInLeft") or []
    templates_right = diff.get("templatesOnlyInRight") or []
    left_root_keys = [event_exact_key(row) for row in left_roots if isinstance(row, dict)]
    right_root_keys = [event_exact_key(row) for row in right_roots if isinstance(row, dict)]
    has_differences = (
        left_counts != right_counts
        or left_root_keys != right_root_keys
        or bool(templates_left)
        or bool(templates_right)
    )

    print(color.apply("DPM trace comparison", "bold"))
    print("  kind:   update-vs-update")
    print(f"  result: {comparison_result(has_differences, color)}")
    print("")

    print(color.apply("Updates", "cyan", "bold"))
    print(f"  baseline:  {trace_compare_summary(left)}")
    print(f"  candidate: {trace_compare_summary(right)}")
    print("")

    print(color.apply("Event counts", "cyan", "bold"))
    print_count_diff(left_counts, right_counts, color)
    print("")

    print(color.apply("Root events", "cyan", "bold"))
    print_event_diff(left_roots, right_roots, color)
    if templates_left or templates_right:
        print("")
        print(color.apply("Template differences", "cyan", "bold"))
        print_template_list("only in baseline", templates_left)
        print_template_list("only in candidate", templates_right)


def print_prepared_update_comparison(comparison: dict[str, Any], color: Color) -> None:
    prepared = comparison.get("left") or {}
    update = comparison.get("right") or {}
    diff = comparison.get("diff") or {}
    commands = diff.get("commands") or []
    roots = diff.get("rootEvents") or []
    command_rows = [row for row in commands if isinstance(row, dict)]
    root_rows = [row for row in roots if isinstance(row, dict)]
    first_command = command_rows[0] if command_rows else None
    first_root = root_rows[0] if root_rows else None
    has_differences = prepared_update_has_differences(command_rows, root_rows)

    print(color.apply("DPM trace comparison", "bold"))
    print("  kind:   prepared-vs-update")
    print(f"  result: {comparison_result(has_differences, color)}")
    print("")

    print(color.apply("Operation", "cyan", "bold"))
    print(f"  prepared:  {command_row_text(first_command) if first_command else '-'}")
    print(f"  committed: {event_row_text(first_root) if first_root else '-'}")
    print(f"  shape:     {operation_shape_summary(first_command, first_root, color)}")
    print("")

    print(color.apply("Field diff", "cyan", "bold"))
    print_prepared_value_summary(first_command, first_root, color)
    print("")

    print(color.apply("Context", "cyan", "bold"))
    print(f"  command id: {prepared.get('commandId') or '-'}")
    if prepared.get("preparedTransactionHash"):
        print(f"  prep hash:  {short(str(prepared.get('preparedTransactionHash')), 80)}")
    print(f"  update:     {short(str(update.get('updateId') or '-'), 80)}")
    print(f"  offset:     {update.get('offset') or '-'}")
    print(f"  act-as:     {party_list_summary(prepared.get('actAs') or [])}")
    print(f"  read-as:    {party_list_summary(update.get('readAs') or [])}")
    print("")

    print(color.apply("Committed state diff", "cyan", "bold"))
    print_count_diff(diff.get("stateDiff") or {}, None, color)


def print_prepared_completion_comparison(
    comparison: dict[str, Any],
    color: Color,
    source_index: SourceIndex | None = None,
) -> None:
    prepared = comparison.get("left") or {}
    completion = comparison.get("right") or {}
    diff = comparison.get("diff") or {}
    status_code = completion.get("statusCode")
    committed = bool(diff.get("committedUpdateAvailable"))
    failed = status_code not in (None, "OK", 0, "0") and not committed

    print(color.apply("DPM trace comparison", "bold"))
    print("  kind:   prepared-vs-completion")
    print(f"  result: {completion_result(committed, failed, color)}")
    print("")
    print(color.apply("Prepared command", "cyan", "bold"))
    print(f"  command id: {prepared.get('commandId') or '-'}")
    print(f"  commands:   {prepared.get('commands', 0)}")
    print("")

    print(color.apply("Completion", "cyan", "bold"))
    print(f"  command id: {completion.get('commandId') or '-'}")
    print(f"  submission: {completion.get('submissionId') or '-'}")
    print(f"  update id:  {short(str(completion.get('updateId') or '-'), 80)}")
    print(f"  offset:     {completion.get('offset') or '-'}")
    print(f"  status:     {status_code if status_code is not None else '-'}")
    print(f"  message:    {completion.get('message') or '-'}")
    completion_for_diag = dict(completion)
    if status_code is not None:
        completion_for_diag["code"] = status_code
    source_matches = completion_source_diagnostics(completion_for_diag, source_index)
    if source_matches:
        print("")
        print(color.apply("Source diagnostics", "cyan", "bold"))
        for loc in source_matches:
            print(indent_text(render_source_diagnostic(loc, source_index, color)))
    log_matches = diff.get("logMatches") or []
    if log_matches:
        print("")
        print(color.apply("Log matches", "cyan", "bold"))
        for match in log_matches[:8]:
            if "error" in match:
                print(f"  {match.get('file')}: {match['error']}")
            else:
                print(f"  {match.get('file')}:{match.get('line')}: {match.get('text')}")
        if len(log_matches) > 8:
            print(f"  ... {len(log_matches) - 8} more")


def print_completion_trace(completion: dict[str, Any], color: Color, source_index: SourceIndex | None = None) -> None:
    status_code, message = completion_status_fields(completion)
    update_id = pick(completion, "updateId", "update_id")
    committed = bool(update_id)
    failed = status_code not in (None, "OK", 0, "0") and not committed
    lookup = completion.get("lookup") if isinstance(completion.get("lookup"), dict) else {}

    print(color.apply("DPM trace completion", "bold"))
    print(f"  result:     {completion_result(committed, failed, color)}")
    print(f"  command id: {pick(completion, 'commandId', 'command_id') or lookup.get('commandId') or '-'}")
    print(f"  submission: {pick(completion, 'submissionId', 'submission_id') or '-'}")
    print(f"  update id:  {short(str(update_id or '-'), 80)}")
    print(f"  offset:     {pick(completion, 'offset') or '-'}")
    print(f"  status:     {status_code if status_code is not None else '-'}")
    print(f"  message:    {message or '-'}")
    if lookup:
        print(f"  parties:    {party_list_summary(lookup.get('parties') or [])}")
        print(f"  source:     {completion.get('source') or '-'}")
    if not update_id:
        print("  trace:      no committed transaction tree is available for this completion")
    source_matches = completion_source_diagnostics(completion, source_index)
    if source_matches:
        print("")
        print(color.apply("Source diagnostics", "cyan", "bold"))
        for loc in source_matches:
            print(indent_text(render_source_diagnostic(loc, source_index, color)))
    log_matches = completion.get("logMatches") or []
    if log_matches:
        print("")
        print(color.apply("Log matches", "cyan", "bold"))
        for match in log_matches[:8]:
            if "error" in match:
                print(f"  {match.get('file')}: {match['error']}")
            else:
                print(f"  {match.get('file')}:{match.get('line')}: {match.get('text')}")
        if len(log_matches) > 8:
            print(f"  ... {len(log_matches) - 8} more")


def comparison_result(has_differences: bool, color: Color) -> str:
    if has_differences:
        return color.apply("visible differences found", "yellow", "bold")
    return color.apply("no visible differences", "green", "bold")


def completion_result(committed: bool, failed: bool, color: Color) -> str:
    if committed:
        return color.apply("completion committed", "green", "bold")
    if failed:
        return color.apply("completion failed", "red", "bold")
    return color.apply("completion status available", "yellow", "bold")


def completion_source_diagnostics(completion: dict[str, Any], source_index: SourceIndex | None) -> list[SourceLocation]:
    if source_index is None or not source_index.files:
        return []
    _, message = completion_status_fields(completion)
    needles = completion_source_needles(str(message or ""))
    seen: set[tuple[str, int, int]] = set()
    result: list[SourceLocation] = []
    for needle in needles:
        for loc in source_index.find_failure_text(needle):
            key = (loc.path, loc.line, loc.column)
            if key in seen:
                continue
            seen.add(key)
            result.append(loc)
            if len(result) >= 5:
                return result
    return result


def completion_source_needles(message: str) -> list[str]:
    candidates: list[str] = []
    for pattern in (r'"([^"]{4,})"', r"'([^']{4,})'", r"`([^`]{4,})`"):
        candidates.extend(match.group(1).strip() for match in re.finditer(pattern, message))
    for separator in (":", "because", "failed with"):
        if separator in message:
            candidates.append(message.rsplit(separator, 1)[-1].strip())
    candidates.append(message.strip())

    cleaned: list[str] = []
    for candidate in candidates:
        candidate = candidate.strip(" .\n\r\t")
        if len(candidate) < 4:
            continue
        if candidate not in cleaned:
            cleaned.append(candidate)
    return cleaned


def render_source_diagnostic(loc: SourceLocation, source_index: SourceIndex | None, color: Color) -> str:
    if source_index is None:
        return format_source_path(loc)
    rendered = source_index.snippet(loc, radius=2)
    lines = rendered.splitlines()
    if not lines:
        return format_source_path(loc)
    lines[0] = color.apply(lines[0], "cyan")
    if loc.label:
        lines.insert(1, f"basis: {loc.label}")
    return "\n".join(lines)


def trace_compare_summary(summary: dict[str, Any]) -> str:
    pieces = [short(str(summary.get("updateId") or "-"), 36)]
    if summary.get("offset"):
        pieces.append(f"offset {summary['offset']}")
    pieces.append(f"{summary.get('events', 0)} events")
    read_as = list_str(summary.get("readAs") or [])
    if read_as:
        pieces.append("read-as " + ", ".join(short_party(party) for party in read_as))
    return ", ".join(pieces)


def party_list_summary(value: Any) -> str:
    parties = list_str(value)
    if not parties:
        return "-"
    return ", ".join(short_party(party) for party in parties)


def print_count_diff(left: dict[str, Any], right: dict[str, Any] | None, color: Color) -> None:
    for key in ("create", "exercise", "archive", "other"):
        left_value = int(left.get(key) or 0)
        if right is None:
            print(f"  {key:<8} {left_value}")
            continue
        right_value = int(right.get(key) or 0)
        marker = count_marker(left_value, right_value, color)
        print(f"  {key:<8} {left_value:>3} -> {right_value:<3} {marker}")


def count_marker(left: int, right: int, color: Color) -> str:
    if left == right:
        return color.apply("same", "green")
    delta = right - left
    sign = "+" if delta > 0 else ""
    return color.apply(f"{sign}{delta}", "yellow")


def print_event_diff(left_rows: list[Any], right_rows: list[Any], color: Color) -> None:
    left = [row for row in left_rows if isinstance(row, dict)]
    right = [row for row in right_rows if isinstance(row, dict)]
    if not left and not right:
        print("  no root events")
        return
    for index in range(max(len(left), len(right))):
        left_row = left[index] if index < len(left) else None
        right_row = right[index] if index < len(right) else None
        if left_row and right_row and event_exact_key(left_row) == event_exact_key(right_row):
            print(f"  [{index}] {event_row_text(left_row):<72} {color.apply('same', 'green')}")
        elif left_row and right_row and event_compare_key(left_row) == event_compare_key(right_row):
            print(f"  [{index}] {event_row_text(left_row):<72} {color.apply('same shape', 'yellow')}")
        elif left_row and right_row:
            print(f"  [{index}] baseline:  {event_row_text(left_row)}")
            print(f"      candidate: {event_row_text(right_row)}")
        elif left_row:
            print(f"  [{index}] baseline only:  {event_row_text(left_row)}")
        elif right_row:
            print(f"  [{index}] candidate only: {event_row_text(right_row)}")


def print_command_event_diff(commands: list[Any], roots: list[Any], color: Color) -> None:
    command_rows = [row for row in commands if isinstance(row, dict)]
    root_rows = [row for row in roots if isinstance(row, dict)]
    if not command_rows and not root_rows:
        print("  no commands or root events")
        return
    for index in range(max(len(command_rows), len(root_rows))):
        command = command_rows[index] if index < len(command_rows) else None
        root = root_rows[index] if index < len(root_rows) else None
        if command and root and command_event_key(command) == event_compare_key(root):
            print(f"  [{index}] {command_row_text(command):<72} {color.apply('matches root event shape', 'green')}")
            print_value_diff(command, root, color)
        else:
            if command:
                print(f"  [{index}] command: {command_row_text(command)}")
            if root:
                print(f"      root:    {event_row_text(root)}")


def prepared_update_has_differences(commands: list[dict[str, Any]], roots: list[dict[str, Any]]) -> bool:
    if len(commands) != len(roots):
        return True
    for command, root in zip(commands, roots):
        if command_event_key(command) != event_compare_key(root):
            return True
        if comparable_value(command.get("value")) != comparable_value(root.get("value")):
            return True
    return False


def operation_shape_summary(command: dict[str, Any] | None, root: dict[str, Any] | None, color: Color) -> str:
    if command is None or root is None:
        return color.apply("different", "yellow")
    if command_event_key(command) == event_compare_key(root):
        return color.apply("matches", "green")
    return color.apply("different", "yellow")


def print_prepared_value_summary(command: dict[str, Any] | None, root: dict[str, Any] | None, color: Color) -> None:
    if command is None or root is None:
        print("  unavailable")
        return
    prepared_value = command.get("value")
    committed_value = root.get("value")
    if comparable_value(prepared_value) == comparable_value(committed_value):
        print(f"  values: {color.apply('match', 'green')}")
        return
    for line in prepared_committed_field_diffs(prepared_value, committed_value):
        print(f"  {color.apply(line, 'yellow')}")


def prepared_committed_field_diffs(prepared: Any, committed: Any) -> list[str]:
    if not isinstance(prepared, dict) or not isinstance(committed, dict):
        return [f"committed {compact_value(committed)}, prepared {compact_value(prepared)}"]
    keys = sorted(set(prepared) | set(committed))
    lines: list[str] = []
    for key in keys:
        prepared_value = prepared.get(key, "<missing>")
        committed_value = committed.get(key, "<missing>")
        if comparable_value(prepared_value) != comparable_value(committed_value):
            lines.append(f"{key}: committed {compact_value(committed_value)}, prepared {compact_value(prepared_value)}")
    return lines or ["values differ"]


def print_template_list(label: str, templates: list[Any]) -> None:
    if not templates:
        print(f"  {label}: -")
        return
    print(f"  {label}:")
    for template in templates:
        print(f"    - {short_template(str(template)) or template}")


def event_compare_key(row: dict[str, Any]) -> tuple[Any, Any, Any]:
    return (row.get("kind"), short_template(row.get("template")), row.get("choice"))


def event_exact_key(row: dict[str, Any]) -> tuple[Any, Any, Any, Any]:
    return (*event_compare_key(row), row.get("contractId"))


def command_event_key(row: dict[str, Any]) -> tuple[Any, Any, Any]:
    return (row.get("kind"), short_template(row.get("template")), row.get("choice"))


def event_row_text(row: dict[str, Any]) -> str:
    label = event_kind_label(str(row.get("kind") or "event"))
    template = short_template(row.get("template")) or "-"
    choice = row.get("choice")
    if choice:
        template = f"{template}.{choice}"
    contract = row.get("contractId")
    suffix = f" ({contract})" if contract else ""
    return f"{label} {template}{suffix}"


def command_row_text(row: dict[str, Any]) -> str:
    label = event_kind_label(str(row.get("kind") or "command"))
    template = short_template(row.get("template")) or "-"
    choice = row.get("choice")
    if choice:
        template = f"{template}.{choice}"
    contract = row.get("contractId")
    suffix = f" ({contract})" if contract else ""
    return f"{label} {template}{suffix}"


def event_kind_label(kind: str) -> str:
    return {
        "create": "CREATE",
        "exercise": "EXERCISE",
        "archive": "ARCHIVE",
    }.get(kind, kind.upper())


def print_value_diff(command: dict[str, Any], root: dict[str, Any], color: Color) -> None:
    command_value = command.get("value")
    root_value = root.get("value")
    if command_value is None and root_value is None:
        return
    if comparable_value(command_value) == comparable_value(root_value):
        print(f"      values: {color.apply('match', 'green')}")
        return

    command_label = command.get("valueLabel") or "command value"
    root_label = root.get("valueLabel") or "committed value"
    print(f"      {command_label}: {compact_value(command_value)}")
    print(f"      committed {root_label}: {compact_value(root_value)}")
    for line in value_field_diffs(command_value, root_value):
        print(f"      {color.apply(line, 'yellow')}")


def value_field_diffs(left: Any, right: Any) -> list[str]:
    if not isinstance(left, dict) or not isinstance(right, dict):
        return ["value differs"]
    keys = sorted(set(left) | set(right))
    lines: list[str] = []
    for key in keys:
        left_value = left.get(key, "<missing>")
        right_value = right.get(key, "<missing>")
        if comparable_value(left_value) != comparable_value(right_value):
            lines.append(f"{key}: {compact_value(left_value)} -> {compact_value(right_value)}")
    return lines or ["value differs"]


def comparable_value(value: Any) -> Any:
    if isinstance(value, dict):
        return {str(key): comparable_value(item) for key, item in sorted(value.items())}
    if isinstance(value, list):
        return [comparable_value(item) for item in value]
    if value is None:
        return None
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float, str)):
        return str(value)
    return value


def compact_value(value: Any) -> str:
    if isinstance(value, str):
        return short(value, 80)
    try:
        return short(json.dumps(value, sort_keys=True), 120)
    except TypeError:
        return short(str(value), 120)


def explicit_js_command(args: argparse.Namespace) -> dict[str, Any]:
    arguments = command_arguments(args)
    if args.contract_id or args.choice:
        if not args.contract_id:
            raise ValueError("--contract-id is required for an exercise simulation")
        if not args.choice:
            raise ValueError("--choice is required for an exercise simulation")
        return {
            "ExerciseCommand": {
                "templateId": args.template,
                "contractId": args.contract_id,
                "choice": args.choice,
                "choiceArgument": arguments,
            }
        }
    return {
        "CreateCommand": {
            "templateId": args.template,
            "createArguments": arguments,
        }
    }


def command_arguments(args: argparse.Namespace) -> Any:
    if args.args_json and args.args_file:
        raise ValueError("use only one of --args-json or --args-file")
    if args.args_json:
        base = parse_json_text(args.args_json, "--args-json")
    elif args.args_file:
        path = Path(args.args_file)
        base = parse_json_text(path.read_text(encoding="utf-8"), str(path))
    else:
        base = {}

    assignments = parse_arg_assignments(args.arg)
    if assignments:
        if base in (None, {}):
            base = {}
        if not isinstance(base, dict):
            raise ValueError("--arg can only be used when arguments are a JSON object")
        base.update(assignments)
    return base


def parse_arg_assignments(values: list[str]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for raw in values:
        if "=" not in raw:
            raise ValueError(f"--arg must use key=value syntax: {raw!r}")
        key, value = raw.split("=", 1)
        key = key.strip()
        if not key:
            raise ValueError(f"--arg key cannot be empty: {raw!r}")
        result[key] = parse_scalar(value.strip())
    return result


def parse_scalar(value: str) -> Any:
    if value == "null":
        return None
    if value == "true":
        return True
    if value == "false":
        return False
    if re.fullmatch(r"-?[0-9]+", value):
        try:
            return int(value)
        except ValueError:
            pass
    if re.fullmatch(r"-?[0-9]+\.[0-9]+", value):
        try:
            return float(value)
        except ValueError:
            pass
    if value.startswith(("{", "[")):
        try:
            return json.loads(value)
        except json.JSONDecodeError:
            pass
    return value


def parse_json_text(value: str, source: str) -> Any:
    try:
        return json.loads(value)
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid JSON in {source}: {exc}") from exc


def participant_ledger_url(args: argparse.Namespace) -> str:
    ledger_url = args.ledger_url
    if not ledger_url:
        raise ValueError("--submitter/--participant-url/--ledger-url is required")
    return str(ledger_url)


def prepare_user_id(args: argparse.Namespace) -> str | None:
    if getattr(args, "user_id", None):
        return args.user_id
    if not getattr(args, "token", None) and not getattr(args, "token_file", None):
        return "participant_admin"
    return None


def unique(values: list[str]) -> list[str]:
    seen: set[str] = set()
    result: list[str] = []
    for value in values:
        if value not in seen:
            seen.add(value)
            result.append(value)
    return result


def load_config(explicit_path: str | None) -> dict[str, Any]:
    path = find_config(explicit_path)
    if not path:
        return {}
    try:
        with path.open("r", encoding="utf-8") as f:
            data = json.load(f)
    except FileNotFoundError:
        if explicit_path:
            raise ValueError(f"config file not found: {explicit_path}") from None
        return {}
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid JSON config {path}: {exc}") from exc
    if not isinstance(data, dict):
        raise ValueError(f"config must be a JSON object: {path}")
    return data


def find_config(explicit_path: str | None) -> Path | None:
    if explicit_path:
        return Path(explicit_path)
    current = Path.cwd().resolve()
    for directory in (current, *current.parents):
        candidate = directory / ".dpm-trace.json"
        if candidate.exists():
            return candidate
    return None


def apply_config_defaults(args: argparse.Namespace, config: dict[str, Any]) -> None:
    set_default(args, "ledger_url", os.environ.get("DPM_TRACE_LEDGER_URL") or get_config(config, "ledgerUrl", "ledger_url"))
    set_default(args, "scan_url", os.environ.get("DPM_TRACE_SCAN_URL") or get_config(config, "scanUrl", "scan_url"))
    set_default(args, "token_file", os.environ.get("DPM_TRACE_TOKEN_FILE") or get_config(config, "tokenFile", "token_file"))
    set_default(args, "token", os.environ.get("DPM_TRACE_TOKEN") or get_config(config, "token"))

    if hasattr(args, "read_as") and hasattr(args, "party") and not args.read_as and not args.party:
        read_as = os.environ.get("DPM_TRACE_READ_AS") or get_config(config, "readAs", "read_as", "party")
        args.read_as = config_values(read_as)
    if hasattr(args, "dar") and not args.dar:
        dar_paths = os.environ.get("DPM_TRACE_DAR") or get_config(config, "darPaths", "dar_paths", "dar")
        args.dar = config_values(dar_paths)
    set_default(args, "damlc", os.environ.get("DPM_TRACE_DAMLC") or get_config(config, "damlc"))
    if hasattr(args, "debug_info") and not args.debug_info:
        debug_info_paths = os.environ.get("DPM_TRACE_DEBUG_INFO") or get_config(config, "debugInfoPaths", "debug_info_paths", "debugInfo", "debug_info")
        args.debug_info = config_values(debug_info_paths)
    if hasattr(args, "daml_yaml") and not args.daml_yaml:
        daml_yaml_paths = os.environ.get("DPM_TRACE_DAML_YAML") or get_config(config, "damlYamlPaths", "daml_yaml_paths", "damlYaml", "daml_yaml")
        args.daml_yaml = config_values(daml_yaml_paths)
    if hasattr(args, "source_root") and not args.source_root:
        source_roots = os.environ.get("DPM_TRACE_SOURCE_ROOT") or get_config(config, "sourceRoots", "source_roots", "sourceRoot", "source_root")
        args.source_root = config_values(source_roots)


def set_default(args: argparse.Namespace, attr: str, value: Any) -> None:
    if not hasattr(args, attr):
        return
    if getattr(args, attr) is None and value not in (None, ""):
        setattr(args, attr, str(value))


def get_config(config: dict[str, Any], *keys: str) -> Any:
    for key in keys:
        if key in config:
            return config[key]
    return None


def config_values(value: Any) -> list[str]:
    if isinstance(value, list):
        return [str(item) for item in value if str(item).strip()]
    if value is None:
        return []
    return [str(value)]


def load_update(
    args: argparse.Namespace,
    update_id: str | None,
    parties: list[str],
) -> tuple[dict[str, Any], str, str | None]:
    if args.source == "scan" or (args.source == "auto" and args.scan_url):
        if not update_id:
            raise ValueError("an update id or CantonScan update URL is required for Scan API")
        if not args.scan_url:
            raise ValueError("--scan-url is required for Scan API fetches")
        url = join_url(args.scan_url, SCAN_UPDATE_PATH.format(update_id=update_id))
        return http_json("GET", url), "scan", url

    if args.source == "ledger" or (args.source == "auto" and args.ledger_url):
        if not update_id:
            raise ValueError("an update id or CantonScan update URL is required for Ledger API")
        if not args.ledger_url:
            raise ValueError("--ledger-url is required for Ledger API fetches")
        if not parties:
            raise ValueError("--read-as/--party is required for participant-scoped Ledger API fetches")
        token = args.token or read_token_file(args.token_file)
        url = join_url(args.ledger_url, LEDGER_UPDATE_BY_ID_PATH)
        body = ledger_update_by_id_body(update_id, parties)
        return http_json("POST", url, body=body, token=token), "ledger-json-api", url

    raise ValueError("choose a source: --scan-url BASE with target, or --ledger-url BASE with target")


def package_metadata_context(dar_paths: list[str], debug_info_paths: list[str], package_ids: list[str]) -> dict[str, Any]:
    found: list[str] = []
    missing: list[str] = []
    for path in dar_paths:
        resolved = str(Path(path).expanduser())
        if Path(resolved).exists():
            found.append(resolved)
        else:
            missing.append(resolved)
    found_debug_info: list[str] = []
    missing_debug_info: list[str] = []
    for path in debug_info_paths:
        resolved = str(Path(path).expanduser())
        if Path(resolved).exists():
            found_debug_info.append(resolved)
        else:
            missing_debug_info.append(resolved)
    return {
        "available": bool(found or found_debug_info),
        "packageIds": package_ids,
        "darPaths": found,
        "debugInfoPaths": found_debug_info,
        "missingDarPaths": missing,
        "missingDebugInfoPaths": missing_debug_info,
        "status": (
            "local package/source metadata attached"
            if found or found_debug_info
            else "package ids captured; package/source metadata must be supplied by local project or registry"
        ),
    }


def http_json(method: str, url: str, body: dict[str, Any] | None = None, token: str | None = None) -> Any:
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = f"Bearer {token.strip()}"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"{method} {url} failed with HTTP {exc.code}: {detail}") from exc
    except urllib.error.URLError as exc:
        raise RuntimeError(f"{method} {url} failed: {exc.reason}") from exc


def ledger_update_by_id_body(update_id: str, parties: list[str]) -> dict[str, Any]:
    filters_by_party = {
        party: {
            "cumulative": [
                {
                    "identifierFilter": {
                        "WildcardFilter": {
                            "value": {
                                "includeCreatedEventBlob": True,
                            }
                        }
                    }
                }
            ]
        }
        for party in parties
    }
    event_format = {
        "filtersByParty": filters_by_party,
        "verbose": True,
    }
    return {
        "updateId": update_id,
        "updateFormat": {
            "includeTransactions": {
                "eventFormat": event_format,
                "transactionShape": "TRANSACTION_SHAPE_LEDGER_EFFECTS",
            },
            "includeReassignments": event_format,
        },
    }


def normalize_trace(
    raw: dict[str, Any],
    source: str,
    source_url: str | None,
    parties: list[str],
) -> NormalizedTrace:
    tx = unwrap_transaction(raw)
    update_id = str(pick(tx, "update_id", "updateId", "id") or pick(raw, "update_id", "updateId") or "")
    if not update_id:
        raise ValueError("could not find update_id/updateId in response")

    events_raw = pick(tx, "events_by_id", "eventsById", "events") or {}
    events_by_id = normalize_events_map(events_raw)

    root_event_ids = list_str(pick(tx, "root_event_ids", "rootEventIds") or [])
    if not root_event_ids:
        root_event_ids = infer_roots(events_by_id)

    if source == "scan":
        note = "Public Scan projection. Event ids may be Scan-indexed and are not the same as a participant projection."
    else:
        note = "Authorized participant projection. Private data outside these party rights is not available."

    projection = {
        "source": source,
        "participantScoped": source != "scan",
        "readAs": parties,
        "notGlobal": source != "scan",
        "note": note,
    }

    return NormalizedTrace(
        update_id=update_id,
        source=source,
        source_url=source_url,
        projection=projection,
        root_event_ids=root_event_ids,
        events_by_id=events_by_id,
        record_time=pick(tx, "record_time", "recordTime"),
        offset=str(pick(tx, "offset") or "") or None,
        synchronizer_id=pick(tx, "synchronizer_id", "synchronizerId"),
        raw=raw,
    )


def unwrap_transaction(raw: dict[str, Any]) -> dict[str, Any]:
    data = raw.get("data", raw)
    for key in ("transaction", "Transaction", "TransactionTree", "update", "Update"):
        if isinstance(data, dict) and isinstance(data.get(key), dict):
            return unwrap_transaction(data[key])
    if isinstance(data, dict) and isinstance(data.get("value"), dict):
        return unwrap_transaction(data["value"])
    if isinstance(data, dict) and ("events_by_id" in data or "eventsById" in data):
        return data
    if isinstance(data, dict) and "events" in data:
        return data
    return data if isinstance(data, dict) else raw


def normalize_events_map(events_raw: Any) -> dict[str, TraceEvent]:
    result: dict[str, TraceEvent] = {}
    if isinstance(events_raw, dict):
        iterable = events_raw.items()
    elif isinstance(events_raw, list):
        iterable = []
        for i, item in enumerate(events_raw):
            if isinstance(item, dict) and "key" in item and "value" in item:
                iterable.append((str(item["key"]), item["value"]))
            else:
                iterable.append((str(i), item))
    else:
        iterable = []

    for event_id, event_raw in iterable:
        if not isinstance(event_raw, dict):
            continue
        event = normalize_event(str(event_id), event_raw)
        result[event.event_id] = event
    link_range_children(result)
    return result


def normalize_event(event_id: str, event_raw: dict[str, Any]) -> TraceEvent:
    variant = event_raw
    kind = "event"
    for candidate, normalized in (
        ("created", "create"),
        ("CreatedEvent", "create"),
        ("createdEvent", "create"),
        ("exercised", "exercise"),
        ("ExercisedEvent", "exercise"),
        ("exercisedEvent", "exercise"),
        ("archived", "archive"),
        ("ArchivedEvent", "archive"),
        ("archivedEvent", "archive"),
    ):
        if isinstance(event_raw.get(candidate), dict):
            variant = event_raw[candidate]
            kind = normalized
            break
    if kind == "event":
        explicit = str(pick(event_raw, "eventType", "event_type", "kind") or "").lower()
        if "create" in explicit:
            kind = "create"
        elif "exercise" in explicit:
            kind = "exercise"
        elif "archive" in explicit:
            kind = "archive"

    resolved_event_id = str(pick(variant, "event_id", "eventId", "node_id", "nodeId") or event_id)
    return TraceEvent(
        event_id=resolved_event_id,
        kind=kind,
        template=template_name(pick(variant, "template_id", "templateId")),
        contract_id=pick(variant, "contract_id", "contractId"),
        choice=pick(variant, "choice"),
        consuming=pick(variant, "consuming"),
        acting_parties=list_str(pick(variant, "acting_parties", "actingParties") or []),
        witnesses=list_str(pick(variant, "witness_parties", "witnessParties", "witnesses") or []),
        signatories=list_str(pick(variant, "signatories") or []),
        observers=list_str(pick(variant, "observers") or []),
        child_event_ids=list_str(pick(variant, "child_event_ids", "childEventIds") or []),
        payload=pick(variant, "create_arguments", "createArguments", "create_argument", "createArgument", "payload"),
        argument=pick(variant, "choice_argument", "choiceArgument", "exercise_argument", "exerciseArgument", "argument"),
        result=pick(variant, "exercise_result", "exerciseResult", "result"),
        raw=event_raw,
    )


def infer_roots(events_by_id: dict[str, TraceEvent]) -> list[str]:
    children = {child for ev in events_by_id.values() for child in ev.child_event_ids}
    roots = [event_id for event_id in events_by_id if event_id not in children]
    return roots or list(events_by_id.keys())


def link_range_children(events_by_id: dict[str, TraceEvent]) -> None:
    numeric_ids = sorted(
        [event_id for event_id in events_by_id if event_id.isdigit()],
        key=lambda value: int(value),
    )
    if not numeric_ids:
        return

    def last_descendant(event_id: str) -> int | None:
        ev = events_by_id[event_id]
        variant = event_variant(ev.raw)
        value = pick(variant, "last_descendant_node_id", "lastDescendantNodeId")
        try:
            return int(value)
        except (TypeError, ValueError):
            return None

    for event_id in numeric_ids:
        ev = events_by_id[event_id]
        if ev.child_event_ids:
            continue
        last = last_descendant(event_id)
        if last is None:
            continue
        current = int(event_id)
        child_ids: list[str] = []
        index = numeric_ids.index(event_id) + 1
        while index < len(numeric_ids):
            child_id = numeric_ids[index]
            child_node_id = int(child_id)
            if child_node_id > last:
                break
            child_ids.append(child_id)
            child_last = last_descendant(child_id)
            if child_last is not None and child_last > child_node_id:
                while index + 1 < len(numeric_ids) and int(numeric_ids[index + 1]) <= child_last:
                    index += 1
            index += 1
        ev.child_event_ids = child_ids


def event_variant(event_raw: dict[str, Any]) -> dict[str, Any]:
    for key in (
        "created",
        "CreatedEvent",
        "createdEvent",
        "exercised",
        "ExercisedEvent",
        "exercisedEvent",
        "archived",
        "ArchivedEvent",
        "archivedEvent",
    ):
        value = event_raw.get(key)
        if isinstance(value, dict):
            return value
    return event_raw


class Color:
    codes = {
        "reset": "\033[0m",
        "bold": "\033[1m",
        "dim": "\033[2m",
        "red": "\033[31m",
        "green": "\033[32m",
        "yellow": "\033[33m",
        "blue": "\033[34m",
        "magenta": "\033[35m",
        "cyan": "\033[36m",
        "gray": "\033[90m",
    }

    def __init__(self, enabled: bool) -> None:
        self.enabled = enabled

    @classmethod
    def from_mode(cls, mode: str) -> "Color":
        if mode == "always":
            return cls(True)
        if mode == "never":
            return cls(False)
        return cls(sys.stdout.isatty() and "NO_COLOR" not in os.environ)

    def apply(self, text: str, *styles: str) -> str:
        if not self.enabled or not styles:
            return text
        prefix = "".join(self.codes[style] for style in styles if style in self.codes)
        return f"{prefix}{text}{self.codes['reset']}"


def print_pretty_trace(trace: NormalizedTrace, color: Color, source_index: "SourceIndex | None" = None) -> None:
    ctx = RenderContext(trace)
    source_index = source_index or SourceIndex()
    print(color.apply("Canton trace", "bold"))
    print(f"  update:       {short(trace.update_id, 80)}")
    print(f"  source:       {trace.source} ({trace.source_url or '-'})")
    print(f"  offset:       {trace.offset or '-'}")
    print(f"  record time:  {trace.record_time or '-'}")
    print(f"  synchronizer: {short(trace.synchronizer_id, 80)}")
    if trace.projection.get("readAs"):
        print(f"  read-as:      {', '.join(ctx.party_with_full(party) for party in trace.projection['readAs'])}")
    print(f"  visibility:   {trace.projection['note']}")
    print(f"  events:       {state_diff_summary(trace, color)}")
    if source_index.has_sources():
        print(f"  source roots: {', '.join(source_index.roots)}")
    if ctx.party_aliases:
        print("  parties:")
        for party, alias in sorted(ctx.party_aliases.items(), key=lambda item: item[1]):
            print(f"    {alias} = {party}")
    print()

    if not trace.root_event_ids:
        print(color.apply("No events found.", "yellow"))
        return

    print(color.apply("Trace", "bold"))
    for index, event_id in enumerate(trace.root_event_ids):
        is_last = index == len(trace.root_event_ids) - 1
        print_event_tree(trace, event_id, prefix="", is_last=is_last, color=color, ctx=ctx, source_index=source_index)


def state_diff_summary(trace: NormalizedTrace, color: Color) -> str:
    counts = {"create": 0, "exercise": 0, "archive": 0, "event": 0}
    for ev in trace.events_by_id.values():
        counts[ev.kind if ev.kind in counts else "event"] += 1
    parts = [
        color.apply(f"+{counts['create']} create", "green"),
        color.apply(f">{counts['exercise']} exercise", "yellow"),
        color.apply(f"x{counts['archive']} archive", "red"),
    ]
    if counts["event"]:
        parts.append(color.apply(f"{counts['event']} other", "blue"))
    return ", ".join(parts)


def event_color(kind: str) -> str:
    return {
        "create": "green",
        "exercise": "yellow",
        "archive": "red",
    }.get(kind, "blue")


def print_event_tree(
    trace: NormalizedTrace,
    event_id: str,
    prefix: str,
    is_last: bool,
    color: Color,
    ctx: "RenderContext",
    source_index: "SourceIndex",
) -> None:
    ev = trace.events_by_id.get(event_id)
    if ev is None:
        return

    connector = "`-- " if is_last else "|-- "
    child_prefix = "    " if is_last else "|   "
    print(prefix + connector + event_title(ev, color))

    detail_lines = event_detail_lines(ev, color, ctx, source_index)
    for index, line in enumerate(detail_lines):
        detail_last = index == len(detail_lines) - 1 and not ev.child_event_ids
        detail_connector = "`-- " if detail_last else "|-- "
        print(prefix + child_prefix + detail_connector + line)

    next_prefix = prefix + child_prefix
    for index, child_id in enumerate(ev.child_event_ids):
        print_event_tree(
            trace,
            child_id,
            prefix=next_prefix,
            is_last=index == len(ev.child_event_ids) - 1,
            color=color,
            ctx=ctx,
            source_index=source_index,
        )


def event_title(ev: TraceEvent, color: Color) -> str:
    kind = ev.kind.upper()
    kind_style = {
        "create": "green",
        "exercise": "yellow",
        "archive": "red",
    }.get(ev.kind, "blue")
    target = event_target(ev)
    marker = {"create": "CREATE", "exercise": "EXERCISE", "archive": "ARCHIVE"}.get(ev.kind, kind)
    return (
        color.apply(f"[{ev.event_id}]", "gray")
        + " "
        + color.apply(marker, kind_style, "bold")
        + " "
        + color.apply(target, "bold")
    )


def event_target(ev: TraceEvent) -> str:
    template = short_template(ev.template) or "<unknown>"
    if ev.choice:
        return f"{template}.{ev.choice}"
    return template


def event_detail_lines(ev: TraceEvent, color: Color, ctx: "RenderContext", source_index: "SourceIndex | None" = None) -> list[str]:
    lines: list[str] = []
    if source_index is not None:
        loc = source_index.location_for_event(ev)
        if loc is not None:
            lines.append(label_value("source", f"{Path(loc.path).name}:{loc.line} ({loc.label})", color))
    if ev.contract_id:
        lines.append(label_value("contract", short(ev.contract_id, 66), color))
    if ev.consuming is not None and ev.kind == "exercise":
        lines.append(label_value("consuming", str(ev.consuming).lower(), color))
    if ev.acting_parties:
        lines.append(label_value("actors", ", ".join(ctx.party(party) for party in ev.acting_parties), color))
    if ev.signatories:
        lines.append(label_value("signatories", ", ".join(ctx.party(party) for party in ev.signatories), color))
    if ev.observers:
        lines.append(label_value("observers", ", ".join(ctx.party(party) for party in ev.observers), color))
    if ev.witnesses:
        lines.append(label_value("witnesses", ", ".join(ctx.party(party) for party in ev.witnesses), color))
    if ev.argument is not None:
        lines.extend(block_lines("argument", ev.argument, color, ctx))
    if ev.payload is not None:
        lines.extend(block_lines("payload", ev.payload, color, ctx))
    if ev.result is not None:
        lines.extend(block_lines("result", ev.result, color, ctx))
    return lines


def label_value(label: str, value: str, color: Color) -> str:
    return f"{color.apply(label + ':', 'cyan')} {value}"


def block_lines(label: str, value: Any, color: Color, ctx: "RenderContext") -> list[str]:
    rendered = render_pretty_value(value, ctx)
    if "\n" not in rendered:
        return [label_value(label, short(rendered, 120), color)]
    lines = [color.apply(label + ":", "cyan")]
    lines.extend("  " + line for line in rendered.splitlines())
    return lines


def render_pretty_value(value: Any, ctx: "RenderContext | None" = None) -> str:
    simplified = simplify_lf_value(value)
    if ctx is not None:
        simplified = ctx.render_value(simplified)
    if isinstance(simplified, dict):
        if not simplified:
            return "{}"
        items = ", ".join(f"{key}: {format_scalar(val, ctx)}" for key, val in simplified.items())
        if len(items) <= 100 and all("\n" not in str(val) for val in simplified.values()):
            return "{ " + items + " }"
    if isinstance(simplified, list):
        items = ", ".join(format_scalar(item, ctx) for item in simplified)
        if len(items) <= 100:
            return "[" + items + "]"
    if not isinstance(simplified, (dict, list)):
        return format_scalar(simplified, ctx)
    return json.dumps(simplified, indent=2, sort_keys=True)


def simplify_lf_value(value: Any) -> Any:
    if isinstance(value, dict):
        if "sum" in value and len(value) == 1:
            return simplify_lf_value(value["sum"])
        if "fields" in value and isinstance(value["fields"], list):
            return {
                str(field.get("label", index)): simplify_lf_value(field.get("value"))
                for index, field in enumerate(value["fields"])
                if isinstance(field, dict)
            }
        if "record" in value and isinstance(value["record"], dict):
            return simplify_lf_value(value["record"])
        for scalar_key in ("party", "int64", "numeric", "text", "contract_id", "contractId", "timestamp", "date", "bool"):
            if scalar_key in value and len(value) == 1:
                return value[scalar_key]
        if "list" in value and isinstance(value["list"], dict):
            return simplify_lf_value(value["list"].get("elements", []))
        if "optional" in value and isinstance(value["optional"], dict):
            optional = value["optional"]
            if "value" not in optional:
                return None
            return simplify_lf_value(optional["value"])
        if "variant" in value and isinstance(value["variant"], dict):
            variant = value["variant"]
            constructor = pick(variant, "constructor", "variant") or "variant"
            return {str(constructor): simplify_lf_value(variant.get("value"))}
        if "enum" in value and isinstance(value["enum"], dict):
            return pick(value["enum"], "constructor", "value") or value["enum"]
        return {key: simplify_lf_value(val) for key, val in value.items() if key not in ("record_id", "recordId")}
    if isinstance(value, list):
        return [simplify_lf_value(item) for item in value]
    return value


class RenderContext:
    def __init__(self, trace: NormalizedTrace) -> None:
        self.party_aliases = build_party_aliases(trace)

    def party(self, value: str) -> str:
        return self.party_aliases.get(value, value)

    def party_with_full(self, value: str) -> str:
        alias = self.party_aliases.get(value)
        if not alias:
            return value
        return f"{alias} ({short_party(value)})"

    def render_value(self, value: Any) -> Any:
        if isinstance(value, str):
            return self.party(value)
        if isinstance(value, list):
            return [self.render_value(item) for item in value]
        if isinstance(value, dict):
            return {key: self.render_value(val) for key, val in value.items()}
        return value


def build_party_aliases(trace: NormalizedTrace) -> dict[str, str]:
    parties: set[str] = set()
    parties.update(trace.projection.get("readAs") or [])

    for ev in trace.events_by_id.values():
        parties.update(ev.acting_parties)
        parties.update(ev.witnesses)
        parties.update(ev.signatories)
        parties.update(ev.observers)
        collect_party_ids(ev.payload, parties)
        collect_party_ids(ev.argument, parties)
        collect_party_ids(ev.result, parties)

    names: dict[str, list[str]] = {}
    for party in parties:
        parsed = split_party_id(party)
        if parsed is None:
            continue
        name, _fingerprint = parsed
        names.setdefault(name, []).append(party)

    aliases: dict[str, str] = {}
    for name, party_ids in names.items():
        sorted_parties = sorted(set(party_ids))
        if len(sorted_parties) == 1:
            aliases[sorted_parties[0]] = name
        else:
            for party in sorted_parties:
                _name, fingerprint = split_party_id(party) or (name, "")
                aliases[party] = f"{name}@{fingerprint[:8]}"
    return aliases


def collect_party_ids(value: Any, parties: set[str]) -> None:
    if isinstance(value, str):
        if split_party_id(value) is not None:
            parties.add(value)
        return
    if isinstance(value, list):
        for item in value:
            collect_party_ids(item, parties)
        return
    if isinstance(value, dict):
        for item in value.values():
            collect_party_ids(item, parties)


def split_party_id(value: str) -> tuple[str, str] | None:
    if "::" not in value:
        return None
    name, fingerprint = value.split("::", 1)
    if not name or not fingerprint:
        return None
    if not re.fullmatch(r"[0-9a-fA-F]{16,}", fingerprint):
        return None
    return name, fingerprint


def short_party(value: str) -> str:
    parsed = split_party_id(value)
    if parsed is None:
        return short(value, 80)
    name, fingerprint = parsed
    return f"{name}::{fingerprint[:8]}...{fingerprint[-6:]}"


def format_scalar(value: Any, ctx: RenderContext | None = None) -> str:
    if isinstance(value, str):
        if ctx is not None:
            value = ctx.party(value)
        return value
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return str(value)
    return json.dumps(value, sort_keys=True)


def short_template(template: str | None) -> str | None:
    if not template:
        return None
    parts = template.split(":")
    if len(parts) >= 3:
        return ":".join(parts[1:])
    return template


class SourceIndex:
    def __init__(
        self,
        debug_info_paths: list[str] | None = None,
        source_roots: list[str] | None = None,
        daml_yaml_paths: list[str] | None = None,
        dar_paths: list[str] | None = None,
        damlc: str | None = None,
    ) -> None:
        self.roots: list[str] = []
        self.templates: dict[str, SourceLocation] = {}
        self.choices: dict[str, SourceLocation] = {}
        self.files: dict[str, list[str]] = {}
        self.file_modules: dict[str, str] = {}
        self.module_files: dict[str, list[str]] = {}
        self.inspect_modules: dict[str, list[str]] = {}
        for path in debug_info_paths or []:
            self._load_debug_info(Path(path).expanduser())
        for path in daml_yaml_paths or []:
            self._load_daml_yaml(Path(path).expanduser())
        for path in source_roots or []:
            self._load_source_root(Path(path).expanduser())
        for path in dar_paths or []:
            self._load_dar_inspect(Path(path).expanduser(), damlc or "daml")

    def _load_debug_info(self, path: Path) -> None:
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return
        if not isinstance(data, dict):
            return
        package_id = data.get("packageId")
        if not isinstance(package_id, str) or not package_id:
            return
        source_root = Path(str(data.get("sourceRoot") or path.parent)).expanduser()
        if not source_root.is_absolute():
            source_root = (path.parent / source_root).resolve()
        source_root_str = str(source_root)
        if source_root_str not in self.roots:
            self.roots.append(source_root_str)
        for file_info in data.get("files") or []:
            if not isinstance(file_info, dict):
                continue
            raw_file_path = file_info.get("path")
            if not raw_file_path:
                continue
            file_path = Path(str(raw_file_path)).expanduser()
            if not file_path.is_absolute():
                file_path = source_root / file_path
            if file_path.exists():
                try:
                    self.files[str(file_path)] = file_path.read_text(encoding="utf-8").splitlines()
                except UnicodeDecodeError:
                    self.files[str(file_path)] = file_path.read_text(errors="replace").splitlines()
            for entity in file_info.get("entities") or []:
                if not isinstance(entity, dict):
                    continue
                key = entity.get("qualifiedName")
                kind = entity.get("kind")
                line = entity.get("startLine")
                if not isinstance(key, str) or not isinstance(kind, str) or not isinstance(line, int):
                    continue
                package_key = f"{package_id}:{key}"
                loc = SourceLocation(str(file_path), line, key)
                if kind == "template":
                    self.templates[package_key] = loc
                elif kind == "choice":
                    self.choices[package_key] = loc

    def _load_daml_yaml(self, path: Path) -> None:
        if not path.exists():
            return
        try:
            lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError:
            return
        source_values: list[str] = []
        for line in lines:
            match = re.match(r"\s*source\s*:\s*(.+?)\s*$", line)
            if match:
                source_values.append(strip_yaml_scalar(match.group(1)))
        if not source_values:
            source_values.append(".")
        for value in source_values:
            root = Path(value).expanduser()
            if not root.is_absolute():
                root = path.parent / root
            self._load_source_root(root)

    def _load_source_root(self, root: Path) -> None:
        root = root.resolve()
        if root.is_file():
            candidates = [root]
            root_marker = root.parent
        else:
            candidates = sorted(root.rglob("*.daml")) if root.exists() else []
            root_marker = root
        root_str = str(root_marker)
        if root_str not in self.roots:
            self.roots.append(root_str)
        for candidate in candidates:
            self._load_source_file(candidate)

    def _load_source_file(self, path: Path) -> None:
        try:
            lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError:
            return
        path_str = str(path)
        self.files[path_str] = lines
        module = daml_module_name(lines)
        if module:
            self.file_modules[path_str] = module
            self.module_files.setdefault(module, [])
            if path_str not in self.module_files[module]:
                self.module_files[module].append(path_str)

    def _load_dar_inspect(self, path: Path, damlc: str) -> None:
        if not path.exists():
            return
        command = damlc_inspect_command(damlc, path)
        try:
            completed = subprocess.run(
                command,
                check=False,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                text=True,
                timeout=30,
                env=daml_child_env(),
            )
        except (OSError, subprocess.TimeoutExpired):
            return
        if completed.returncode != 0:
            return
        current_module: str | None = None
        for line in completed.stdout.splitlines():
            module_match = re.match(r"module\s+([A-Za-z0-9_.']+)\s+where\b", line)
            if module_match:
                current_module = module_match.group(1)
                self.inspect_modules.setdefault(current_module, [])
                continue
            if current_module:
                self.inspect_modules.setdefault(current_module, []).append(line)

    def location_for_event(self, ev: TraceEvent) -> SourceLocation | None:
        parsed = parse_template_ref(ev.template)
        if parsed is None:
            return None
        package_id, module, entity = parsed
        if ev.choice:
            return self.choices.get(f"{package_id}:{module}:{entity}.{ev.choice}")
        return self.templates.get(f"{package_id}:{module}:{entity}")

    def snippet(self, loc: SourceLocation, radius: int = 2) -> str:
        lines = self.files.get(loc.path)
        if not lines:
            return format_source_path(loc)
        start = max(loc.line - radius, 1)
        end = min(loc.line + radius, len(lines))
        rendered: list[str] = []
        width = len(str(end))
        for line_no in range(start, end + 1):
            marker = ">" if line_no == loc.line else " "
            prefix = f"{marker} {line_no:{width}d} | "
            rendered.append(prefix + lines[line_no - 1])
            if line_no == loc.line and loc.column > 1:
                rendered.append(" " * (len(prefix) + loc.column - 1) + "^")
        return f"{format_source_path(loc)}\n" + "\n".join(rendered)

    def body_lines(self, loc: SourceLocation) -> list[SourceLine]:
        lines = self.files.get(loc.path)
        if not lines:
            return []

        result: list[SourceLine] = []
        choice_indent = leading_spaces(lines[loc.line - 1]) if loc.line - 1 < len(lines) else 0
        for idx in range(loc.line + 1, len(lines) + 1):
            text = lines[idx - 1]
            stripped = text.strip()
            if not stripped:
                result.append(SourceLine(loc.path, idx, text))
                continue
            indent = leading_spaces(text)
            if indent <= choice_indent and re.match(r"(choice|template)\s+\S+", stripped):
                break
            if indent <= choice_indent and stripped.startswith("-- |"):
                break
            result.append(SourceLine(loc.path, idx, text))
        return result

    def has_sources(self) -> bool:
        return bool(self.templates or self.choices or self.files)

    def find_text(self, text: str, label: str, limit: int = 5) -> list[SourceLocation]:
        return self._find_text(text, label, None, limit)

    def find_failure_text(self, text: str, limit: int = 5) -> list[SourceLocation]:
        if not text:
            return []
        modules = self.inspect_modules_containing(text)
        if modules:
            paths = [
                path
                for module in modules
                for path in self.module_files.get(module, [])
            ]
            if paths:
                label = "damlc inspect: " + ", ".join(modules[:3])
                return self._find_text(text, label, paths, limit)
        return self._find_text(text, "local source", None, limit)

    def inspect_modules_containing(self, text: str) -> list[str]:
        if not text:
            return []
        result: list[str] = []
        for module, lines in sorted(self.inspect_modules.items()):
            if any(text in line for line in lines):
                result.append(module)
        return result

    def _find_text(self, text: str, label: str, paths: list[str] | None, limit: int) -> list[SourceLocation]:
        if not text:
            return []
        result: list[SourceLocation] = []
        items = [(path, self.files[path]) for path in paths or sorted(self.files) if path in self.files]
        for path, lines in items:
            for line_no, line in enumerate(lines, start=1):
                column = line.find(text)
                if column < 0:
                    continue
                result.append(SourceLocation(path, line_no, label, column + 1))
                if len(result) >= limit:
                    return result
        return result


def daml_module_name(lines: list[str]) -> str | None:
    for line in lines[:50]:
        match = re.match(r"\s*module\s+([A-Za-z0-9_.']+)\s+where\b", line)
        if match:
            return match.group(1)
    return None


def damlc_inspect_command(damlc: str, path: Path) -> list[str]:
    executable = str(Path(damlc).expanduser())
    if Path(executable).name == "damlc":
        return [executable, "inspect", str(path), "--detail", "2"]
    return [executable, "damlc", "inspect", str(path), "--detail", "2"]


def strip_yaml_scalar(value: str) -> str:
    value = value.strip()
    if value.startswith(('"', "'")) and value.endswith(('"', "'")) and len(value) >= 2:
        return value[1:-1]
    return value


def format_source_path(loc: SourceLocation) -> str:
    suffix = f":{loc.line}"
    if loc.column and loc.column > 1:
        suffix += f":{loc.column}"
    return f"{loc.path}{suffix}"


def leading_spaces(value: str) -> int:
    return len(value) - len(value.lstrip(" "))


def source_index_from_args(args: argparse.Namespace, bundle: dict[str, Any] | None = None) -> SourceIndex:
    debug_info_paths: list[str] = []
    debug_info_paths.extend(getattr(args, "debug_info", []) or [])
    packages = (bundle or {}).get("packages") or {}
    debug_info_paths.extend(list_str(packages.get("debugInfoPaths") or []))
    source_roots = list_str(getattr(args, "source_root", []) or [])
    daml_yaml_paths = list_str(getattr(args, "daml_yaml", []) or [])
    dar_paths = list_str(getattr(args, "dar", []) or [])
    return SourceIndex(
        unique([path for path in debug_info_paths if path]),
        source_roots=unique([path for path in source_roots if path]),
        daml_yaml_paths=unique([path for path in daml_yaml_paths if path]),
        dar_paths=unique([path for path in dar_paths if path]),
        damlc=getattr(args, "damlc", None) or "daml",
    )


def parse_template_ref(template: str | None) -> tuple[str, str, str] | None:
    if not template:
        return None
    parts = template.split(":")
    if len(parts) < 3:
        return None
    return parts[0], parts[1], parts[2]


def input_contract_payload(bundle: dict[str, Any] | None, contract_id: str | None) -> Any:
    if bundle is None or not contract_id:
        return None
    acs = bundle.get("acsSnapshot") or {}
    return find_contract_payload(acs.get("response"), contract_id)


def find_contract_payload(value: Any, contract_id: str) -> Any:
    if isinstance(value, list):
        for item in value:
            found = find_contract_payload(item, contract_id)
            if found is not None:
                return found
        return None
    if not isinstance(value, dict):
        return None

    created = pick(value, "createdEvent", "created_event", "created", "CreatedEvent")
    if isinstance(created, dict):
        cid = pick(created, "contractId", "contract_id")
        if cid == contract_id:
            payload = pick(created, "createArgument", "create_arguments", "createArguments", "payload")
            return simplify_lf_value(payload)

    cid = pick(value, "contractId", "contract_id")
    if cid == contract_id:
        payload = pick(value, "createArgument", "create_arguments", "createArguments", "payload")
        if payload is not None:
            return simplify_lf_value(payload)

    for child in value.values():
        if isinstance(child, (dict, list)):
            found = find_contract_payload(child, contract_id)
            if found is not None:
                return found
    return None


def child_create_payload(trace: NormalizedTrace, ev: TraceEvent) -> Any:
    for child_id in ev.child_event_ids:
        child = trace.events_by_id.get(child_id)
        if child is not None and child.kind == "create" and child.payload is not None:
            return simplify_lf_value(child.payload)
    return None


def expression_environment(ev: TraceEvent, bundle: dict[str, Any] | None) -> dict[str, Any]:
    env: dict[str, Any] = {}
    input_contract = input_contract_payload(bundle, ev.contract_id)
    if isinstance(input_contract, dict):
        env.update(input_contract)
        env["this"] = input_contract
    if ev.argument is not None:
        argument = simplify_lf_value(ev.argument)
        env["choiceArgument"] = argument
        if isinstance(argument, dict):
            env.update(argument)
    return env


def expression_steps_for_event(
    trace: NormalizedTrace,
    bundle: dict[str, Any] | None,
    source_index: SourceIndex,
    ev: TraceEvent,
) -> list[ExpressionStep]:
    loc = source_index.location_for_event(ev)
    if loc is None:
        return []
    source_lines = source_index.body_lines(loc)
    if not source_lines:
        return []

    env = expression_environment(ev, bundle)
    output_payload = child_create_payload(trace, ev)
    steps: list[ExpressionStep] = [
        ExpressionStep(
            line=SourceLine(loc.path, loc.line, source_index.files.get(loc.path, [""])[loc.line - 1]),
            label=f"enter {ev.choice or event_target(ev)}",
            expression=ev.choice or event_target(ev),
            variables=env.copy(),
            result=None,
            note="source-linked replay step",
        )
    ]

    for line in source_lines:
        stripped = line.text.strip()
        if not stripped or stripped.startswith("--"):
            continue
        if stripped.startswith("controller "):
            expr = stripped.removeprefix("controller ").strip()
            result = eval_daml_expression(expr, env)
            steps.append(ExpressionStep(line, "authorize controller", expr, env.copy(), result))
            continue
        if stripped == "do":
            steps.append(ExpressionStep(line, "enter do block", stripped, env.copy(), None))
            continue
        if stripped.startswith("create this with "):
            assignment_text = stripped.removeprefix("create this with ").strip()
            assignments = parse_record_update_assignments(assignment_text)
            if not assignments:
                steps.append(ExpressionStep(line, "create", stripped, env.copy(), output_payload))
                continue
            for field, expr in assignments:
                result = eval_daml_expression(expr, env)
                steps.append(
                    ExpressionStep(
                        line=line,
                        label=f"evaluate {field}",
                        expression=expr,
                        variables=env.copy(),
                        result=result,
                    )
                )
                env[field] = result
            steps.append(
                ExpressionStep(
                    line=line,
                    label="create contract",
                    expression=stripped,
                    variables=env.copy(),
                    result=output_payload if output_payload is not None else env.get("this"),
                )
            )
            continue
        if stripped.startswith("create "):
            steps.append(ExpressionStep(line, "create", stripped, env.copy(), output_payload))
            continue
        if "<-" in stripped:
            name, expr = [part.strip() for part in stripped.split("<-", 1)]
            result = eval_daml_expression(expr, env)
            steps.append(ExpressionStep(line, f"bind {name}", expr, env.copy(), result))
            if result is not None:
                env[name] = result
            continue
        steps.append(ExpressionStep(line, "evaluate", stripped, env.copy(), eval_daml_expression(stripped, env)))

    return steps


def parse_record_update_assignments(value: str) -> list[tuple[str, str]]:
    assignments: list[tuple[str, str]] = []
    for part in split_top_level(value, ";"):
        if "=" not in part:
            continue
        field, expr = part.split("=", 1)
        field = field.strip()
        expr = expr.strip()
        if field and expr:
            assignments.append((field, expr))
    return assignments


def split_top_level(value: str, delimiter: str) -> list[str]:
    parts: list[str] = []
    depth = 0
    start = 0
    for idx, char in enumerate(value):
        if char in "([{":
            depth += 1
        elif char in ")]}":
            depth = max(depth - 1, 0)
        elif char == delimiter and depth == 0:
            parts.append(value[start:idx].strip())
            start = idx + 1
    parts.append(value[start:].strip())
    return [part for part in parts if part]


def eval_daml_expression(expr: str, env: dict[str, Any]) -> Any:
    expr = expr.strip()
    if not expr:
        return None
    if expr in env:
        return env[expr]
    if re.fullmatch(r"-?[0-9]+", expr):
        return int(expr)
    if expr.startswith('"') and expr.endswith('"'):
        return expr[1:-1]
    if expr.startswith("(") and expr.endswith(")"):
        return eval_daml_expression(expr[1:-1], env)
    for op in ("+", "-"):
        left, right = split_binary(expr, op)
        if left is not None and right is not None:
            left_value = eval_daml_expression(left, env)
            right_value = eval_daml_expression(right, env)
            if op == "+":
                return coerce_int(left_value) + coerce_int(right_value)
            return coerce_int(left_value) - coerce_int(right_value)
    return None


def split_binary(expr: str, operator: str) -> tuple[str | None, str | None]:
    depth = 0
    for idx in range(len(expr) - 1, -1, -1):
        char = expr[idx]
        if char in ")]}":
            depth += 1
        elif char in "([{":
            depth = max(depth - 1, 0)
        elif char == operator and depth == 0 and idx > 0:
            return expr[:idx].strip(), expr[idx + 1 :].strip()
    return None, None


def coerce_int(value: Any) -> int:
    if isinstance(value, bool):
        return int(value)
    if isinstance(value, int):
        return value
    if isinstance(value, str) and re.fullmatch(r"-?[0-9]+", value):
        return int(value)
    raise ValueError(f"cannot evaluate integer expression from {value!r}")


@dataclass
class Breakpoint:
    spec: str

    def matches(self, step: int, event_id: str, ev: TraceEvent, loc: SourceLocation | None) -> bool:
        spec = self.spec.strip()
        if not spec:
            return False
        lowered = spec.lower()
        if spec == event_id or lowered == f"#{event_id}".lower() or spec == str(step + 1):
            return True
        target = event_target(ev).lower()
        if lowered == target or lowered in target:
            return True
        if loc is None:
            return False
        if lowered == loc.label.lower() or lowered in loc.label.lower():
            return True
        file_part, sep, line_part = spec.rpartition(":")
        if sep and line_part.isdigit():
            try:
                line = int(line_part)
            except ValueError:
                return False
            if line != loc.line:
                return False
            file_part = file_part.strip()
            return not file_part or loc.path.endswith(file_part)
        return loc.path.endswith(spec)


class Stepper:
    def __init__(
        self,
        trace: NormalizedTrace,
        bundle: dict[str, Any] | None = None,
        source_index: SourceIndex | None = None,
        color: Color | None = None,
    ) -> None:
        self.trace = trace
        self.bundle = bundle
        self.source_index = source_index or SourceIndex()
        self.color = color or Color(False)
        self.breakpoints: list[Breakpoint] = []
        self.order = self._preorder()
        self.index = 0
        self.expression_event_id: str | None = None
        self.expression_index = 0

    def run(self) -> None:
        print_summary(self.trace)
        print("\n" + self.color.apply("Visualizer commands:", "bold") + " n/next, p/prev, j <n>, s/source, vars, b <spec>, c/continue, tree, context, json, q")
        if self.source_index.has_sources():
            print(self.color.apply("source roots:", "cyan"), ", ".join(self.source_index.roots))
        if not self.order:
            print("No events found.")
            return
        self.show_current()
        while True:
            try:
                cmd = input("dpm-trace> ").strip()
            except (EOFError, KeyboardInterrupt):
                print()
                return
            if cmd in ("q", "quit", "exit"):
                return
            if cmd in ("", "n", "next"):
                self.index = min(self.index + 1, len(self.order) - 1)
                self.show_current()
            elif cmd in ("p", "prev"):
                self.index = max(self.index - 1, 0)
                self.show_current()
            elif cmd.startswith("j "):
                self.jump(cmd)
            elif cmd in ("s", "src", "source"):
                self.show_source()
            elif cmd in ("vars", "locals"):
                self.show_variables()
            elif cmd.startswith("b "):
                self.add_breakpoint(cmd)
            elif cmd in ("bp", "breakpoints"):
                self.list_breakpoints()
            elif cmd.startswith("clear"):
                self.clear_breakpoints(cmd)
            elif cmd in ("c", "continue"):
                self.continue_to_breakpoint()
            elif cmd == "tree":
                self.show_tree()
            elif cmd == "context":
                print(debug_context_report(self.trace))
            elif cmd == "json":
                event = self.trace.events_by_id[self.order[self.index]]
                print(json.dumps(event_to_json(event), indent=2, sort_keys=True))
            elif cmd == "help":
                print("n/next, p/prev, j <index>, s/source, vars, b <spec>, bp, clear [n], c/continue, tree, context, json, q")
            else:
                print("unknown command; try `help`")

    def _preorder(self) -> list[str]:
        seen: set[str] = set()
        order: list[str] = []

        def visit(event_id: str) -> None:
            if event_id in seen or event_id not in self.trace.events_by_id:
                return
            seen.add(event_id)
            order.append(event_id)
            for child in self.trace.events_by_id[event_id].child_event_ids:
                visit(child)

        for root in self.trace.root_event_ids:
            visit(root)
        for event_id in self.trace.events_by_id:
            visit(event_id)
        return order

    def show_current(self) -> None:
        ctx = RenderContext(self.trace)
        event_id = self.order[self.index]
        if self.expression_event_id != event_id:
            self.expression_event_id = event_id
            self.expression_index = 0
        ev = self.trace.events_by_id[event_id]
        color = self.color
        print("\n" + color.apply("-" * 72, "gray"))
        print(color.apply(f"Step {self.index + 1}/{len(self.order)}", "bold"), color.apply(ev.kind.upper(), event_color(ev.kind), "bold"), color.apply(ev.event_id, "gray"))
        print(label_value("template", ev.template or "-", color))
        loc = self.source_index.location_for_event(ev)
        if loc is not None:
            print(label_value("source", f"{format_source_path(loc)}  ({loc.label})", color))
        print(label_value("contract", short(ev.contract_id), color))
        if ev.choice:
            print(label_value("choice", f"{ev.choice}  consuming={ev.consuming}", color))
        if ev.acting_parties:
            print(label_value("actors", ", ".join(ctx.party(party) for party in ev.acting_parties), color))
        if ev.witnesses:
            print(label_value("witness", ", ".join(ctx.party(party) for party in ev.witnesses), color))
        if ev.signatories or ev.observers:
            signatories = [ctx.party(party) for party in ev.signatories]
            observers = [ctx.party(party) for party in ev.observers]
            print(label_value("stakeholders", f"signatories={signatories or []} observers={observers or []}", color))
        if ev.payload is not None:
            print(color.apply("payload:", "cyan"))
            print(indent_text(render_pretty_value(ev.payload, ctx)))
        if ev.argument is not None:
            print(color.apply("choice argument:", "cyan"))
            print(indent_text(render_pretty_value(ev.argument, ctx)))
        if ev.result is not None:
            print(color.apply("choice result:", "cyan"))
            print(indent_text(render_pretty_value(ev.result, ctx)))
        if ev.child_event_ids:
            print(label_value("children", ", ".join(ev.child_event_ids), color))

    def show_source(self) -> None:
        ev = self.trace.events_by_id[self.order[self.index]]
        loc = self.source_index.location_for_event(ev)
        if loc is None:
            print(self.color.apply("no source location available for this event; provide matching source metadata", "yellow"))
            return
        print(self.render_source_snippet(loc))

    def render_source_snippet(self, loc: SourceLocation, radius: int = 2) -> str:
        lines = self.source_index.files.get(loc.path)
        if not lines:
            return f"{loc.path}:{loc.line}"
        start = max(loc.line - radius, 1)
        end = min(loc.line + radius, len(lines))
        width = len(str(end))
        rendered = [self.color.apply(format_source_path(loc), "cyan")]
        for line_no in range(start, end + 1):
            marker = ">" if line_no == loc.line else " "
            prefix = f"{marker} {line_no:{width}d} | "
            line = lines[line_no - 1]
            if line_no == loc.line:
                rendered.append(self.color.apply(prefix + line, "yellow", "bold"))
            else:
                rendered.append(self.color.apply(prefix, "gray") + line)
        return "\n".join(rendered)

    def show_variables(self) -> None:
        event_id = self.order[self.index]
        ev = self.trace.events_by_id[event_id]
        ctx = RenderContext(self.trace)
        variables = self.step_variables(ev, ctx)
        print(self.color.apply("variables", "bold"))
        if not variables:
            print("  -")
            return
        for key, value in variables.items():
            rendered = render_pretty_value(value, ctx)
            if "\n" in rendered:
                print(f"  {self.color.apply(key + ':', 'cyan')}")
                print(indent_text(rendered))
            else:
                print(f"  {self.color.apply(key + ':', 'cyan')} {rendered}")

    def show_expression_steps(self) -> None:
        steps = self.current_expression_steps()
        if not steps:
            print(self.color.apply("no expression steps available; provide source metadata and visible inputs", "yellow"))
            return
        for idx, step in enumerate(steps, start=1):
            print(f"{self.color.apply(str(idx) + '.', 'gray')} {self.color.apply(step.label, 'bold')}  {self.color.apply(Path(step.line.path).name + ':' + str(step.line.line), 'cyan')}")
            print(f"   {self.color.apply('source:', 'cyan')} {step.line.text.strip()}")
            if step.expression:
                print(f"   {self.color.apply('expr:', 'cyan')}   {step.expression}")
            if step.result is not None:
                print(f"   {self.color.apply('result:', 'green')} {render_pretty_value(step.result, RenderContext(self.trace))}")
            if step.note:
                print(f"   {self.color.apply('note:', 'gray')}   {step.note}")

    def step_expression(self) -> None:
        steps = self.current_expression_steps()
        if not steps:
            print(self.color.apply("no expression steps available", "yellow"))
            return
        if self.expression_index >= len(steps):
            self.expression_index = len(steps) - 1
        step = steps[self.expression_index]
        self.print_expression_step(step)
        if self.expression_index < len(steps) - 1:
            self.expression_index += 1

    def current_expression_steps(self) -> list[ExpressionStep]:
        event_id = self.order[self.index]
        ev = self.trace.events_by_id[event_id]
        return expression_steps_for_event(self.trace, self.bundle, self.source_index, ev)

    def print_expression_step(self, step: ExpressionStep) -> None:
        ctx = RenderContext(self.trace)
        color = self.color
        print("\n" + color.apply("-" * 72, "gray"))
        print(color.apply(f"Expression {self.expression_index + 1}", "bold"), color.apply(step.label, "magenta", "bold"))
        print(label_value("source", f"{step.line.path}:{step.line.line}", color))
        print(f"  {color.apply(step.line.text.strip(), 'bold')}")
        if step.expression:
            print(label_value("expr", step.expression, color))
        if step.variables:
            compact_vars = {
                key: value
                for key, value in step.variables.items()
                if key in ("this", "owner", "count", "amount", "choiceArgument")
            }
            if compact_vars:
                print(color.apply("vars:", "cyan"))
                for key, value in compact_vars.items():
                    print(f"  {color.apply(key + ':', 'cyan')} {render_pretty_value(value, ctx)}")
        if step.result is not None:
            print(f"{color.apply('result:', 'green')} {render_pretty_value(step.result, ctx)}")
        if step.note:
            print(f"{color.apply('note:', 'gray')}   {step.note}")

    def step_variables(self, ev: TraceEvent, ctx: RenderContext) -> dict[str, Any]:
        variables: dict[str, Any] = {
            "eventId": ev.event_id,
            "kind": ev.kind,
        }
        if ev.template:
            variables["template"] = ev.template
        if ev.contract_id:
            variables["contractId"] = ev.contract_id
        if ev.choice:
            variables["choice"] = ev.choice
        if ev.acting_parties:
            variables["actors"] = ev.acting_parties
        if ev.witnesses:
            variables["witnesses"] = ev.witnesses
        if ev.signatories:
            variables["signatories"] = ev.signatories
        if ev.payload is not None:
            variables["createPayload"] = simplify_lf_value(ev.payload)
        if ev.argument is not None:
            variables["choiceArgument"] = simplify_lf_value(ev.argument)
        if ev.result is not None:
            variables["choiceResult"] = simplify_lf_value(ev.result)
        input_contract = input_contract_payload(self.bundle, ev.contract_id)
        if input_contract is not None:
            variables["inputContract"] = input_contract
        return {key: ctx.render_value(value) for key, value in variables.items()}

    def add_breakpoint(self, cmd: str) -> None:
        _head, _sep, spec = cmd.partition(" ")
        spec = spec.strip()
        if not spec:
            print(self.color.apply("usage: b <event-id|template.choice|file:line>", "yellow"))
            return
        self.breakpoints.append(Breakpoint(spec))
        print(f"{self.color.apply('breakpoint', 'magenta', 'bold')} {len(self.breakpoints)} set: {spec}")

    def list_breakpoints(self) -> None:
        if not self.breakpoints:
            print(self.color.apply("no breakpoints", "yellow"))
            return
        for index, breakpoint in enumerate(self.breakpoints, start=1):
            print(f"{self.color.apply(str(index) + ':', 'gray')} {breakpoint.spec}")

    def clear_breakpoints(self, cmd: str) -> None:
        _head, _sep, value = cmd.partition(" ")
        value = value.strip()
        if not value:
            self.breakpoints.clear()
            print(self.color.apply("cleared all breakpoints", "yellow"))
            return
        try:
            index = int(value) - 1
        except ValueError:
            print(self.color.apply("usage: clear [breakpoint-number]", "yellow"))
            return
        if index < 0 or index >= len(self.breakpoints):
            print(self.color.apply(f"breakpoint must be between 1 and {len(self.breakpoints)}", "yellow"))
            return
        removed = self.breakpoints.pop(index)
        print(self.color.apply("cleared breakpoint:", "yellow"), removed.spec)

    def continue_to_breakpoint(self) -> None:
        if not self.breakpoints:
            print(self.color.apply("no breakpoints set", "yellow"))
            return
        if not self.order:
            print(self.color.apply("no events", "yellow"))
            return
        start = self.index + 1
        for idx in range(start, len(self.order)):
            event_id = self.order[idx]
            ev = self.trace.events_by_id[event_id]
            loc = self.source_index.location_for_event(ev)
            if any(bp.matches(idx, event_id, ev, loc) for bp in self.breakpoints):
                self.index = idx
                self.show_current()
                return
        print(self.color.apply("no later breakpoint hit", "yellow"))

    def show_tree(self) -> None:
        current_event_id = self.order[self.index] if self.order else None

        def visit(event_id: str, depth: int) -> None:
            ev = self.trace.events_by_id.get(event_id)
            if not ev:
                return
            is_current = event_id == current_event_id
            cursor = self.color.apply("=>", "magenta", "bold") if is_current else "  "
            indent = "  " * depth
            target = short_template(ev.template) or ev.template or ""
            label = f"{target}.{ev.choice}" if ev.choice and target else (ev.choice or target)
            kind = f"{ev.kind.upper():<8}"
            if is_current:
                kind = self.color.apply(kind, event_color(ev.kind), "bold")
                label = self.color.apply(label, "bold")
            print(f"{indent}{cursor} {kind} {ev.event_id} {label}")
            for child in ev.child_event_ids:
                visit(child, depth + 1)

        for root in self.trace.root_event_ids:
            visit(root, 0)

    def jump(self, cmd: str) -> None:
        _, _, value = cmd.partition(" ")
        try:
            idx = int(value) - 1
        except ValueError:
            print("usage: j <step-number>")
            return
        if idx < 0 or idx >= len(self.order):
            print(f"step must be between 1 and {len(self.order)}")
            return
        self.index = idx
        self.show_current()


def print_summary(trace: NormalizedTrace) -> None:
    print(f"update:      {trace.update_id}")
    print(f"source:      {trace.source} ({trace.source_url or '-'})")
    print(f"record time: {trace.record_time or '-'}")
    print(f"offset:      {trace.offset or '-'}")
    print(f"synchronizer:{trace.synchronizer_id or '-'}")
    print(f"projection:  {trace.projection['note']}")
    if trace.projection.get("readAs"):
        print(f"read-as:     {', '.join(trace.projection['readAs'])}")
    print(f"events:      {len(trace.events_by_id)}")


def debug_context_report(trace: NormalizedTrace) -> str:
    package_ids = sorted({
        package
        for ev in trace.events_by_id.values()
        for package in [package_from_template(ev.template)]
        if package
    })
    present = [
        "participant-visible transaction tree",
        "event order and parent/child links" if trace.root_event_ids else "flat event list",
        "choice arguments and create payloads where exposed",
        "party/witness labels where exposed",
    ]
    if package_ids:
        present.append(f"package ids referenced by events: {', '.join(package_ids[:5])}")

    missing = [
        "source metadata unless provided by the local project or registry",
        "full original command envelope unless captured separately",
        "private subtransactions outside this projection",
        "operator logs unless attached separately",
    ]
    return (
        "\nTrace context assessment\n"
        "------------------------\n"
        "Present in this trace:\n"
        + "\n".join(f"- {item}" for item in present)
        + "\n\nNot present in this trace artifact:\n"
        + "\n".join(f"- {item}" for item in missing)
    )


def explain_apis() -> str:
    return textwrap.dedent(
        f"""
        Scan API vs Ledger API
        ----------------------
        Scan API:
        - Public/indexed network data from Super Validator Scan services.
        - Useful for CantonScan-like flows and public update lookup.
        - Endpoint used by this POC: GET {SCAN_UPDATE_PATH}
        - Does not prove access to a bank/private participant projection.

        Ledger JSON API:
        - Authenticated participant/validator API.
        - Requires participant URL, bearer token, and read-as/party context.
        - Endpoint used by this POC: POST {LEDGER_UPDATE_BY_ID_PATH}
        - Returns the participant-visible projection; it is not a global trace.

        In proposal terms:
        - Scan is the public entry point.
        - Ledger API is the authorized participant inspection entry point.
        """
    ).strip()


def trace_to_json(trace: NormalizedTrace) -> dict[str, Any]:
    return {
        "updateId": trace.update_id,
        "source": trace.source,
        "sourceUrl": trace.source_url,
        "projection": trace.projection,
        "recordTime": trace.record_time,
        "offset": trace.offset,
        "synchronizerId": trace.synchronizer_id,
        "rootEventIds": trace.root_event_ids,
        "eventsById": {key: event_to_json(ev) for key, ev in trace.events_by_id.items()},
    }


def trace_from_json(data: dict[str, Any]) -> NormalizedTrace:
    events_json = data.get("eventsById") or {}
    if not isinstance(events_json, dict):
        raise ValueError("trace.eventsById must be an object")
    events_by_id = {
        str(event_id): event_from_json(event)
        for event_id, event in events_json.items()
        if isinstance(event, dict)
    }
    return NormalizedTrace(
        update_id=str(data.get("updateId") or ""),
        source=str(data.get("source") or "artifact"),
        source_url=data.get("sourceUrl"),
        projection=data.get("projection") if isinstance(data.get("projection"), dict) else {},
        root_event_ids=list_str(data.get("rootEventIds") or []),
        events_by_id=events_by_id,
        record_time=data.get("recordTime"),
        offset=str(data.get("offset") or "") or None,
        synchronizer_id=data.get("synchronizerId"),
        raw={},
    )


def event_to_json(ev: TraceEvent) -> dict[str, Any]:
    return {
        "eventId": ev.event_id,
        "kind": ev.kind,
        "template": ev.template,
        "contractId": ev.contract_id,
        "choice": ev.choice,
        "consuming": ev.consuming,
        "actingParties": ev.acting_parties,
        "witnesses": ev.witnesses,
        "signatories": ev.signatories,
        "observers": ev.observers,
        "childEventIds": ev.child_event_ids,
        "payload": ev.payload,
        "argument": ev.argument,
        "result": ev.result,
    }


def event_from_json(data: dict[str, Any]) -> TraceEvent:
    return TraceEvent(
        event_id=str(data.get("eventId") or ""),
        kind=str(data.get("kind") or "event"),
        template=data.get("template"),
        contract_id=data.get("contractId"),
        choice=data.get("choice"),
        consuming=data.get("consuming"),
        acting_parties=list_str(data.get("actingParties") or []),
        witnesses=list_str(data.get("witnesses") or []),
        signatories=list_str(data.get("signatories") or []),
        observers=list_str(data.get("observers") or []),
        child_event_ids=list_str(data.get("childEventIds") or []),
        payload=data.get("payload"),
        argument=data.get("argument"),
        result=data.get("result"),
        raw={},
    )


def extract_update_id(target: str | None) -> str | None:
    if not target:
        return None
    match = re.search(r"/update/([^/?#]+)", target)
    if match:
        return match.group(1)
    return target


def parse_parties(values: list[str]) -> list[str]:
    parties: list[str] = []
    for value in values:
        for part in value.split(","):
            stripped = part.strip()
            if stripped:
                parties.append(stripped)
    return parties


def read_token_file(path: str | None) -> str | None:
    if not path:
        return None
    return Path(path).read_text(encoding="utf-8").strip()


def join_url(base: str, path: str) -> str:
    return base.rstrip("/") + "/" + path.lstrip("/")


def pick(obj: dict[str, Any], *keys: str) -> Any:
    for key in keys:
        if key in obj:
            return obj[key]
    return None


def list_str(value: Any) -> list[str]:
    if isinstance(value, list):
        return [str(item) for item in value]
    if value is None:
        return []
    return [str(value)]


def template_name(value: Any) -> str | None:
    if value is None:
        return None
    if isinstance(value, str):
        return value
    if isinstance(value, dict):
        package = pick(value, "package_id", "packageId", "packageName")
        module = pick(value, "module_name", "moduleName")
        entity = pick(value, "entity_name", "entityName")
        parts = [str(part) for part in (package, module, entity) if part]
        return ":".join(parts) if parts else json.dumps(value, sort_keys=True)
    return str(value)


def package_from_template(template: str | None) -> str | None:
    if not template or ":" not in template:
        return None
    return template.split(":", 1)[0]


def indent_json(value: Any) -> str:
    return textwrap.indent(json.dumps(value, indent=2, sort_keys=True), "  ")


def indent_text(value: str) -> str:
    return textwrap.indent(value, "  ")


def short(value: str | None, max_len: int = 32) -> str:
    if not value:
        return "-"
    if len(value) <= max_len:
        return value
    return value[: max_len - 3] + "..."


if __name__ == "__main__":
    raise SystemExit(main())
