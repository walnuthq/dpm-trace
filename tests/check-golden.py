"""Golden-output harness: records complete stdout/stderr/exit for CLI cases.

The lit suite asserts selected CHECK lines; this asserts every byte, so
unreviewed rendering changes (tree connectors, party aliases, JSON key order,
number formatting) cannot pass silently. That makes it the oracle for a
reimplementation: point DPM_TRACE_BIN at another binary and the goldens must
reproduce exactly.

    python tests/check-golden.py <repo-root>            # verify
    python tests/check-golden.py <repo-root> --update   # re-record

Cases must be offline and deterministic. Non-deterministic output (timestamps,
absolute paths, temp dirs) is scrubbed by SCRUBBERS below; anything that cannot
be scrubbed does not belong in a case.
"""

import difflib
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

GOLDEN_DIR = Path("tests/golden")

# name -> argv after the CLI entry point. Paths are repo-relative and resolved
# against the repo root at run time.
CASES: dict[str, list[str]] = {
    # ── trace tree rendering (print_pretty_trace, event_detail_lines) ────────
    # trace-b has an exercise with a child create, so this covers nesting,
    # connectors, party aliasing and payload/argument blocks.
    # The examples are a Milestone 1 deliverable, so their rendering is pinned
    # here: a change to the trace tree must be reviewed, not discovered by a
    # reader of examples/README.md.
    "example-failed-withdraw": [
        "--completion-file", "examples/failed-withdraw.completion.json",
        "--daml-yaml", "examples/asset/daml.yaml", "--color", "never",
    ],
    "example-compare-prepared": [
        "compare", "--prepared", "examples/transfer.prepared.json",
        "--update", "examples/transfer.trace.json", "--full", "--color", "never",
    ],
    "open-example-prepared": ["open", "examples/transfer.prepared.json", "--color", "never"],
    "open-example-create": ["open", "examples/create.trace.json", "--color", "never"],
    "open-example-exercise-child-create": ["open", "examples/exercise-child-create.trace.json", "--color", "never"],
    "open-example-archive": ["open", "examples/archive.trace.json", "--color", "never"],
    "open-trace-a": ["open", "tests/fixtures/compare/trace-a.json", "--color", "never"],
    "open-trace-b": ["open", "tests/fixtures/compare/trace-b.json", "--color", "never"],
    "open-trace-b-json": ["open", "tests/fixtures/compare/trace-b.json", "--print-json"],

    # ── trace: reassignment rendering ────────────────────────────────────────
    # Captured from a real two-synchronizer Canton, not hand-written.
    "open-reassign-unassign": [
        "open", "examples/unassign.trace.json", "--color", "never",
    ],
    "open-reassign-assign": [
        "open", "examples/assign.trace.json", "--color", "never",
    ],
    "open-reassign-assign-json": [
        "open", "examples/assign.trace.json", "--print-json",
    ],

    # ── compare: prepared vs completion ──────────────────────────────────────
    "compare-prepared-vs-completion-fail": [
        "compare",
        "--prepared", "tests/fixtures/compare/prepared.json",
        "--completion-file", "tests/fixtures/compare/completion-fail.json",
        "--color", "never",
    ],
    "compare-prepared-vs-completion-fail-full": [
        "compare",
        "--prepared", "tests/fixtures/compare/prepared.json",
        "--completion-file", "tests/fixtures/compare/completion-fail.json",
        "--color", "never", "--full",
    ],
    "compare-prepared-vs-completion-ok": [
        "compare",
        "--prepared", "tests/fixtures/compare/prepared.json",
        "--completion-file", "tests/fixtures/compare/completion-ok.json",
        "--color", "never",
    ],

    # ── compare: prepared vs committed update ────────────────────────────────
    "compare-prepared-vs-update": [
        "compare",
        "--prepared", "tests/fixtures/compare/prepared.json",
        "--update", "tests/fixtures/compare/trace-a.json",
        "--color", "never",
    ],
    "compare-prepared-vs-update-full": [
        "compare",
        "--prepared", "tests/fixtures/compare/prepared.json",
        "--update", "tests/fixtures/compare/trace-a.json",
        "--color", "never", "--full",
    ],

    # trace-b has an exercise with a child create, so this pins whether the
    # committed side carries child events into the JSON. It does not today;
    # see #42, which tracks that and the fields the diff ignores.
    "compare-prepared-vs-update-json": [
        "compare",
        "--prepared", "tests/fixtures/compare/prepared.json",
        "--update", "tests/fixtures/compare/trace-b.json",
        "--print-json",
    ],

    # The prepared side decodes into a tree, so a committed child that differs
    # from the one the participant prepared is reported. update-tampered
    # changes only the child create's quantity.
    "compare-prepared-child-match": [
        "compare",
        "--prepared", "tests/fixtures/child-diff/prepared.json",
        "--update", "tests/fixtures/child-diff/update-expected.json",
        "--color", "never",
    ],
    "compare-prepared-child-differs": [
        "compare",
        "--prepared", "tests/fixtures/child-diff/prepared.json",
        "--update", "tests/fixtures/child-diff/update-tampered.json",
        "--color", "never",
    ],
    "compare-prepared-child-differs-full": [
        "compare",
        "--prepared", "tests/fixtures/child-diff/prepared.json",
        "--update", "tests/fixtures/child-diff/update-tampered.json",
        "--color", "never", "--full",
    ],

    # ── compare: update vs update ────────────────────────────────────────────
    "compare-update-vs-update": [
        "compare",
        "tests/fixtures/compare/trace-a.json", "tests/fixtures/compare/trace-b.json",
        "--color", "never",
    ],
    "compare-update-vs-update-full": [
        "compare",
        "tests/fixtures/compare/trace-a.json", "tests/fixtures/compare/trace-b.json",
        "--color", "never", "--full",
    ],
    "compare-update-vs-update-json": [
        "compare",
        "tests/fixtures/compare/trace-a.json", "tests/fixtures/compare/trace-b.json",
        "--print-json",
    ],

    # ── failed submissions / source diagnostics ──────────────────────────────
    "completion-plain": [
        "--completion-file", "tests/fixtures/failed-with-source.completion.json",
        "--color", "never",
    ],
    "completion-with-source": [
        "--completion-file", "tests/fixtures/failed-with-source.completion.json",
        "--daml-yaml", "tests/fixtures/source-pkg/daml.yaml",
        "--color", "never",
    ],

    # ── usage / error surfaces ───────────────────────────────────────────────
    "open-missing-file": ["open", "tests/fixtures/does-not-exist.json"],
    "open-not-an-artifact": ["open", "tests/fixtures/reassignment/real-assign-update.json"],
}

# Driver scripts that exercise code paths the CLI cannot reach offline --
# comparison model construction, driven through the CLI.
#
# What remains here drives the binary like CASES does; the entries that called
# into the Python implementation went with it. Their behaviour now lives in the
# Go package tests.
SCRIPT_CASES: dict[str, list[str]] = {
    "script-compare": ["tests/check-compare.py", "<root>"],
}

def scrubbers(root: Path) -> list[tuple[re.Pattern[str], str]]:
    home = str(Path.home())
    return [
        (re.compile(re.escape(str(root))), "<root>"),
        (re.compile(re.escape(home)), "<home>"),
        (re.compile(r"/(?:private/)?(?:tmp|var/folders)/[^\s\"']+"), "<tmp>"),
        # Only the artifact's generated createdAt. Deliberately narrow: fixture
        # timestamps like recordTime are deterministic and must stay in the
        # golden, or the harness scrubs away the signal it exists to protect.
        (re.compile(r'("createdAt":\s*)"[^"]*"'), r'\1"<timestamp>"'),
        (re.compile(r"dpm-trace-(?:submit|prepare)-[0-9a-f]{12}"), "dpm-trace-<id>"),
    ]


def scrub(text: str, root: Path) -> str:
    for pattern, replacement in scrubbers(root):
        text = pattern.sub(replacement, text)
    return text


def resolve_args(args: list[str], root: Path) -> list[str]:
    """Make repo-relative path arguments absolute; cases run in a temp cwd."""
    return [
        str(root / arg) if "/" in arg and not arg.startswith("-") else arg
        for arg in args
    ]


def case_inputs_present(args: list[str], root: Path) -> bool:
    """True unless a case references a .json input that is not in the tree.

    Lets generated or optional fixtures be absent without failing the suite;
    such cases report SKIP instead."""
    for arg in args:
        if arg.endswith(".json") and "/" in arg and "does-not-exist" not in arg:
            if not (root / arg).exists():
                return False
    return True


def run_case(args: list[str], root: Path, binary: list[str], label: str) -> str:
    env = dict(os.environ)
    env["PYTHONPATH"] = str(root / "src") + os.pathsep + env.get("PYTHONPATH", "")
    env["NO_COLOR"] = "1"
    env["COLUMNS"] = "100"
    # Isolate from a developer's .dpm-trace.json, which would inject ledger
    # defaults and change output.
    workdir = tempfile.mkdtemp(prefix="dpm-golden-")
    try:
        completed = subprocess.run(
            binary + resolve_args(args, root),
            cwd=workdir,
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=60,
        )
        stdout = scrub(completed.stdout, root)
        stderr = scrub(completed.stderr, root)
        exit_code = completed.returncode
    finally:
        shutil.rmtree(workdir, ignore_errors=True)

    parts = [f"$ {label}", f"--- exit: {exit_code}", "--- stdout:", stdout.rstrip("\n")]
    if stderr.strip():
        parts += ["--- stderr:", stderr.rstrip("\n")]
    return "\n".join(parts) + "\n"


def main() -> int:
    argv = sys.argv[1:]
    update = "--update" in argv
    positional = [a for a in argv if not a.startswith("--")]
    if len(positional) != 1:
        print("usage: check-golden.py <repo-root> [--update]", file=sys.stderr)
        return 2

    root = Path(positional[0]).resolve()
    golden_dir = root / GOLDEN_DIR
    golden_dir.mkdir(parents=True, exist_ok=True)

    # The implementation under test. There is only one now, so the binary is
    # required rather than defaulted: silently testing something else is worse
    # than refusing to run.
    binary_env = os.environ.get("DPM_TRACE_BIN", "").strip()
    if not binary_env:
        print("error: DPM_TRACE_BIN must point at the dpm-trace binary "
              "(go build -o /tmp/dpm-trace ./cmd/trace)", file=sys.stderr)
        return 2
    binary = binary_env.split()

    # (name, argv, command prefix, display label)
    plan: list[tuple[str, list[str], list[str], str]] = [
        (name, CASES[name], binary, "dpm trace " + " ".join(CASES[name]))
        for name in sorted(CASES)
    ]
    plan += [
        (
            name,
            [arg for arg in SCRIPT_CASES[name] if arg != "<root>"]
            + ([str(root)] if "<root>" in SCRIPT_CASES[name] else []),
            [sys.executable],
            "python " + " ".join(SCRIPT_CASES[name]),
        )
        for name in sorted(SCRIPT_CASES)
    ]
    total = len(plan)

    failures: list[str] = []
    skipped: list[str] = []
    updated = 0

    for name, args, prefix, label in plan:
        if not case_inputs_present(args, root):
            skipped.append(name)
            print(f"  SKIP    {name}  (input fixture not present)")
            continue

        actual = run_case(args, root, prefix, label)
        golden_path = golden_dir / f"{name}.txt"

        if update:
            existing = golden_path.read_text(encoding="utf-8") if golden_path.exists() else None
            golden_path.write_text(actual, encoding="utf-8")
            state = "unchanged" if existing == actual else ("recorded" if existing is None else "UPDATED")
            if existing != actual:
                updated += 1
            print(f"  {state:<9} {name}")
            continue

        if not golden_path.exists():
            failures.append(name)
            print(f"  MISSING {name}  (run with --update to record)")
            continue

        expected = golden_path.read_text(encoding="utf-8")
        if expected == actual:
            print(f"  PASS    {name}")
        else:
            failures.append(name)
            print(f"  FAIL    {name}")
            diff = difflib.unified_diff(
                expected.splitlines(keepends=True),
                actual.splitlines(keepends=True),
                fromfile=f"golden/{name}.txt",
                tofile="actual",
            )
            sys.stdout.writelines("      " + line for line in diff)

    print()
    if update:
        print(f"golden: {total - len(skipped)} cases recorded, {updated} changed, {len(skipped)} skipped")
        return 0
    if failures:
        print(f"golden output checks FAILED: {len(failures)} of {total - len(skipped)}")
        for name in failures:
            print(f"  - {name}")
        print("\nIf a change is intentional, re-record with --update and review the diff.")
        return 1
    print(f"golden output checks passed ({total - len(skipped)} cases, {len(skipped)} skipped)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
