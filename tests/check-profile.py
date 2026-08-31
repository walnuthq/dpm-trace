"""Checks for `dpm profile`.

Covers the parts a FileCheck run cannot assert: exit codes (CI depends on
telling a budget breach from a crash), the invariants the sizing model must
hold, and the measured-versus-modeled distinction.

Exits non-zero printing what failed, so lit can drive it directly.
"""
from __future__ import annotations

import base64
import contextlib
import io
import json
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from dpm_trace.cli import (  # noqa: E402
    PROFILE_EXIT_BUDGET,
    PROFILE_EXIT_ERROR,
    PROFILE_EXIT_OK,
    canonical_bytes,
    check_profile_budgets,
    diff_profiles,
    field_sizes,
    main,
    measured_wire_bytes,
    walk_profile,
    rebuild_profile_tree,
)

FIXTURES = ROOT / "tests" / "fixtures" / "profile"
BEFORE = FIXTURES / "transfer-before.trace-artifact.json"
AFTER = FIXTURES / "transfer-after.trace-artifact.json"
BUDGETS = FIXTURES / "budgets.json"

failures: list[str] = []


def check(label: str, condition: bool, detail: str = "") -> None:
    if not condition:
        failures.append(f"{label}{': ' + detail if detail else ''}")


def profile_of(artifact: Path, tmp: Path) -> dict:
    code = main(["profile", "tx", "--trace", str(artifact), "--export", str(tmp)])
    check(f"export {artifact.name}", code == PROFILE_EXIT_OK, f"exit {code}")
    return json.loads(tmp.read_text(encoding="utf-8"))


def main_check() -> int:
    with tempfile.TemporaryDirectory(prefix="dpm-profile-check-") as tmpdir:
        return run_checks(Path(tmpdir))


def run_checks(tmp: Path) -> int:
    before = profile_of(BEFORE, tmp / "before.json")
    after = profile_of(AFTER, tmp / "after.json")

    # --- sizing invariants -------------------------------------------------
    roots = rebuild_profile_tree(before["tree"])
    nodes = list(walk_profile(roots))
    check("node count matches totals", len(nodes) == before["totals"]["nodes"])
    check(
        "self bytes split into envelope and payload",
        all(n.self_bytes == n.envelope_bytes + n.payload_bytes for n in nodes),
    )
    for node in nodes:
        subtree = node.self_bytes + sum(c.total_bytes for c in node.children)
        check(f"subtree total for {node.event_id}", node.total_bytes == subtree,
              f"{node.total_bytes} != {subtree}")
    check(
        "modeled total is the sum of self bytes",
        before["totals"]["modeledBytes"] == sum(n.self_bytes for n in nodes),
    )
    check(
        "per-template rollup sums to the modeled total",
        sum(row["selfBytes"] for row in before["byTemplate"]) == before["totals"]["modeledBytes"],
    )
    # Field sizes are attribution, so they must not exceed what they attribute.
    for node in nodes:
        check(f"fields fit payload for {node.event_id}",
              sum(node.fields.values()) <= node.payload_bytes + len(node.fields) * 8)

    # --- encoding ----------------------------------------------------------
    check("None encodes to zero bytes", canonical_bytes(None) == 0)
    check("encoding is stable across key order",
          canonical_bytes({"a": 1, "b": 2}) == canonical_bytes({"b": 2, "a": 1}))
    check("nested fields flatten to dotted paths",
          set(field_sizes({"a": {"b": 1}, "c": [2]})) == {"a.b", "c[0]"})

    # --- measured vs modeled ----------------------------------------------
    check("no measured size for a JSON-fetched update", "measuredWireBytes" not in before["totals"])
    blob = base64.b64encode(b"\x01\x02\x03\x04").decode()
    check("prepared bytes are measured, not modeled",
          (measured_wire_bytes({"response": {"preparedTransaction": blob}}) or {}).get("bytes") == 4)
    check("a missing prepared transaction yields no measurement",
          measured_wire_bytes({"response": {}}) is None)

    # --- diff --------------------------------------------------------------
    diff = diff_profiles(before, after)
    delta = diff["totals"]["delta"]
    check("regression is payload only", delta["envelopeBytes"] == 0, str(delta))
    check("regression adds bytes", delta["modeledBytes"] > 0, str(delta))
    check("node count is unchanged", delta["nodes"] == 0, str(delta))
    check("diff of a profile with itself is empty",
          diff_profiles(before, before)["totals"]["delta"]["modeledBytes"] == 0)

    # --- budgets and exit codes -------------------------------------------
    budgets = json.loads(BUDGETS.read_text(encoding="utf-8"))
    check("before fits its budgets", check_profile_budgets(before, budgets) == [])
    breaches = check_profile_budgets(after, budgets)
    check("after breaches its budgets", len(breaches) == 2, f"{len(breaches)} breach(es)")
    check("every breach names a limit and an actual",
          all({"budget", "limit", "actual", "message"} <= set(b) for b in breaches))
    check("no budgets means no findings", check_profile_budgets(after, {}) == [])

    code = main(["profile", "check", str(tmp / "before.json"), "--budget", str(BUDGETS)])
    check("passing budget check exits 0", code == PROFILE_EXIT_OK, f"exit {code}")
    code = main(["profile", "check", str(tmp / "after.json"), "--budget", str(BUDGETS)])
    check("breaching budget check exits 2", code == PROFILE_EXIT_BUDGET, f"exit {code}")
    code = main(["profile", "check", str(tmp / "missing.json"), "--budget", str(BUDGETS)])
    check("a tool error exits 1, not 2", code == PROFILE_EXIT_ERROR, f"exit {code}")
    code = main(["profile", "tx"])
    check("no input exits 1", code == PROFILE_EXIT_ERROR, f"exit {code}")

    # --- rendered output ---------------------------------------------------
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        main(["profile", "diff", str(tmp / "before.json"), str(tmp / "after.json")])
    rendered = out.getvalue()
    for needle in ("DPM cost diff", "nodes:        0", "modeled:      +456 B",
                   "envelope:     0 B", "payload:      +456 B", "Changed templates"):
        check(f"diff renders {needle!r}", needle in rendered, rendered)

    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        main(["profile", "check", str(tmp / "after.json"), "--budget", str(BUDGETS)])
    rendered = out.getvalue()
    for needle in ("budget check failed: 2 breach(es)", "maxTotalBytes", "maxTemplateBytes"):
        check(f"breach renders {needle!r}", needle in rendered, rendered)

    if failures:
        print(f"check-profile: {len(failures)} failure(s)")
        for line in failures:
            print(f"  - {line}")
        return 1
    print("check-profile: all checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main_check())
