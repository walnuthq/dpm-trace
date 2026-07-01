"""Pin the %ledger2-before-%ledger substitution ordering invariant.

`integration_lit_cfg_text()` registers `%ledger2` before `%ledger` because
lit applies substitutions as in-order literal replacements, so a bare
`%ledger` would otherwise rewrite the `2` inside `%ledger2`. The ordering
was enforced only by a comment; a refactor that alphabetizes or reorders
the registrations would silently break every cross-participant integration
test.

This check execs the actual generated lit.cfg.py under a stubbed lit/
lit_config/config namespace with sentinel ledger URLs, then asserts both
that `%ledger2` is registered before `%ledger` and that an in-order
literal-replacement pass (lit's documented behavior) leaves a `%ledger2`
token intact and uncorrupted. Renaming the substitutions to non-colliding
names (e.g. %ledger_p1/%ledger_p2) was deferred because it would break the
sibling daml-tests/itests/*.test files (a separate repo) and the
scaffolder-sync check.
"""
from __future__ import annotations

import os
import sys
import types
from pathlib import Path


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-substitution-order.py <repo-root>", file=sys.stderr)
        return 2
    repo_root = Path(sys.argv[1]).resolve()
    sys.path.insert(0, str(repo_root / "src"))
    from dpm_trace.cli import integration_lit_cfg_text  # noqa: E402

    saved = {k: os.environ.get(k) for k in ("DPM_TRACE_IT_LEDGER", "DPM_TRACE_IT_LEDGER2", "DPM_TRACE_IT_DAML")}
    try:
        os.environ["DPM_TRACE_IT_LEDGER"] = "http://ledger1.example"
        os.environ["DPM_TRACE_IT_LEDGER2"] = "http://ledger2.example"
        os.environ["DPM_TRACE_IT_DAML"] = "daml"

        config = types.SimpleNamespace(substitutions=[], environment={})
        lit_config = types.SimpleNamespace(fatal=lambda msg: (_ for _ in ()).throw(SystemExit(msg)))
        lit_formats = types.SimpleNamespace(ShTest=lambda **kwargs: object())
        lit_mod = types.ModuleType("lit")
        lit_mod.formats = lit_formats
        formats_mod = types.ModuleType("lit.formats")
        formats_mod.ShTest = lit_formats.ShTest
        sys.modules["lit"] = lit_mod
        sys.modules["lit.formats"] = formats_mod
        namespace = {
            "os": os,
            "sys": sys,
            "lit_config": lit_config,
            "config": config,
            "__file__": str(repo_root / "itests" / "lit.cfg.py"),
        }
        try:
            exec(compile(integration_lit_cfg_text(), "lit.cfg.py", "exec"), namespace)
        finally:
            sys.modules.pop("lit", None)
            sys.modules.pop("lit.formats", None)
    finally:
        for key, value in saved.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value

    keys = [key for key, _ in config.substitutions]
    errors: list[str] = []
    if "%ledger2" not in keys or "%ledger" not in keys:
        errors.append(f"expected %ledger and %ledger2 substitutions; got {keys}")
    elif keys.index("%ledger2") >= keys.index("%ledger"):
        errors.append(
            f"%ledger2 must be registered before %ledger (prefix collision); got order {keys}"
        )

    # Simulate lit's in-order literal replacement on a representative line.
    sample = "%dpm trace --submitter %ledger2 --read-as %bob && %dpm trace --submitter %ledger"
    result = sample
    for key, value in config.substitutions:
        result = result.replace(key, str(value))
    if "http://ledger2.example" not in result:
        errors.append(f"%ledger2 did not resolve to the ledger2 URL: {result!r}")
    if "http://ledger1.example2" in result:
        errors.append(f"%ledger2 was corrupted by the %ledger substitution: {result!r}")

    if errors:
        print("substitution-order checks FAILED:")
        for err in errors:
            print(f"  - {err}")
        return 1
    print("substitution-order checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
