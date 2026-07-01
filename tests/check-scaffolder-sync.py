"""Enforce that the scaffolder's embedded `lit.cfg.py` text stays in sync with
the canonical sibling `daml-tests/itests/lit.cfg.py`.

`integration_lit_cfg_text()` in `dpm_trace.cli` embeds a copy of
`itests/lit.cfg.py`. AGENTS.md requires it stays in sync with
`daml-tests/itests/lit.cfg.py`, but that file lives in a separate repo, so
drift is otherwise silent.

This check diffs the embedded text against the sibling canonical file when it
is available. If the sibling package is not present (e.g. CI that only checks
out `dpm-trace`), the check exits 0 with a SKIP notice so it never blocks
unrelated pipelines; where the sibling is checked out (the canonical Walnut
workspace), drift fails the suite.
"""
from __future__ import annotations

import os
import sys
from pathlib import Path


def _norm(text: str) -> str:
    return text.replace("\r\n", "\n").rstrip() + "\n"


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-scaffolder-sync.py <repo-root>", file=sys.stderr)
        return 2
    repo_root = Path(sys.argv[1]).resolve()

    env_sibling = os.environ.get("DPM_TRACE_DAML_TESTS_DIR", "").strip()
    sibling = Path(env_sibling).resolve() if env_sibling else repo_root.parent / "daml-tests"
    cfg = sibling / "itests" / "lit.cfg.py"
    if not cfg.is_file():
        print(
            f"scaffolder-sync checks skipped: sibling daml-tests/itests/lit.cfg.py "
            f"not found at {cfg} (set DPM_TRACE_DAML_TESTS_DIR to enable)."
        )
        return 0

    sys.path.insert(0, str(repo_root / "src"))
    from dpm_trace.cli import integration_lit_cfg_text  # noqa: E402

    embedded = _norm(integration_lit_cfg_text())
    canonical = _norm(cfg.read_text(encoding="utf-8"))
    if embedded != canonical:
        print(
            "scaffolder-sync checks FAILED:\n"
            f"  - integration_lit_cfg_text() drifts from {cfg} (canonical). "
            "Update cli.py to match the sibling file."
        )
        return 1
    print("scaffolder-sync checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
