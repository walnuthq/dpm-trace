"""Pin the %ledger2-before-%ledger substitution ordering invariant.

`integration_lit_cfg_text()` registers `%ledger2` before `%ledger` because
lit applies substitutions as in-order literal replacements, so a bare
`%ledger` would otherwise rewrite the `2` inside `%ledger2`. The ordering
was enforced only by a comment; a refactor that alphabetizes or reorders
the registrations would silently break every cross-participant integration
test.

The text comes from `dpm trace test --init`, so this checks whichever
implementation DPM_TRACE_BIN selects rather than the Python source alone.

This check execs the actual generated lit.cfg.py under a stubbed lit/
lit_config/config namespace with sentinel ledger URLs, then asserts both
that `%ledger2` is registered before `%ledger` and that an in-order
literal-replacement pass (lit's documented behavior) leaves a `%ledger2`
token intact and uncorrupted. Renaming the substitutions to non-colliding
names (e.g. %ledger_p1/%ledger_p2) was deferred because it would break the
sibling daml-contracts/itests/*.test files (a separate repo) and the
scaffolder-sync check.
"""
from __future__ import annotations

import os
import sys
import types
from pathlib import Path


def subst(text: str, config) -> str:
    for key, value in config.substitutions:
        text = text.replace(key, str(value))
    return text


def load_config(lit_cfg_text: str, repo_root: Path, token_files: dict[str, str], ledger: str = "http://ledger1.example"):
    names = ["DPM_TRACE_IT_LEDGER", "DPM_TRACE_IT_LEDGER2", "DPM_TRACE_IT_DAML", "DPM_TRACE_IT_DPM",
             "DPM_TRACE_IT_TOKEN_FILE", "DPM_TRACE_IT_TOKEN_FILE2", "DPM_TRACE_CONFIG"]
    saved = {name: os.environ.get(name) for name in names}
    try:
        os.environ["DPM_TRACE_IT_LEDGER"] = ledger
        os.environ["DPM_TRACE_IT_LEDGER2"] = "http://ledger2.example"
        os.environ["DPM_TRACE_IT_DAML"] = "daml"
        os.environ["DPM_TRACE_CONFIG"] = "/stub/empty-config.json"
        os.environ.pop("DPM_TRACE_IT_TOKEN_FILE", None)
        os.environ.pop("DPM_TRACE_IT_TOKEN_FILE2", None)
        for suffix, path in token_files.items():
            os.environ["DPM_TRACE_IT_TOKEN_FILE" + suffix] = path
        # The generated config now refuses to load without a binary.
        os.environ["DPM_TRACE_IT_DPM"] = "/stub/dpm-trace"

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
            exec(compile(lit_cfg_text, "lit.cfg.py", "exec"), namespace)
        finally:
            sys.modules.pop("lit", None)
            sys.modules.pop("lit.formats", None)
    finally:
        for key, value in saved.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value
    return config


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-substitution-order.py <repo-root>", file=sys.stderr)
        return 2
    repo_root = Path(sys.argv[1]).resolve()
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    from scaffold_output import generated_lit_cfg  # noqa: E402

    lit_cfg_text = generated_lit_cfg(repo_root)

    config = load_config(lit_cfg_text, repo_root, token_files={})
    attached = load_config(lit_cfg_text, repo_root, token_files={"": "/tokens/one file", "2": "/tokens/two"})

    unsafe = load_config(lit_cfg_text, repo_root, token_files={}, ledger="http://host/a b;touch x")

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

    if "%auth2" not in keys or "%auth" not in keys:
        errors.append(f"expected %auth and %auth2 substitutions; got {keys}")
    elif keys.index("%auth2") >= keys.index("%auth"):
        errors.append(f"%auth2 must be registered before %auth (prefix collision); got order {keys}")

    auth_sample = "%dpm trace --submitter %ledger2 %auth2 && %dpm trace --submitter %ledger %auth"
    boot_result = subst(auth_sample, config)
    expected_boot = (
        "/stub/dpm-trace trace --submitter http://ledger2.example "
        " && /stub/dpm-trace trace --submitter http://ledger1.example "
    )
    if boot_result != expected_boot:
        errors.append(f"boot mode must expand %auth and %auth2 to nothing: {boot_result!r}")
    attached_result = subst(auth_sample, attached)
    expected_attached = (
        '/stub/dpm-trace trace --submitter http://ledger2.example --token-file "$DPM_TRACE_IT_TOKEN_FILE2"'
        ' && /stub/dpm-trace trace --submitter http://ledger1.example --token-file "$DPM_TRACE_IT_TOKEN_FILE"'
    )
    if attached_result != expected_attached:
        errors.append(f"attach mode must expand %auth[2] to --token-file flags that read the environment: {attached_result!r}")
    for name, path in (("DPM_TRACE_IT_TOKEN_FILE", "/tokens/one file"), ("DPM_TRACE_IT_TOKEN_FILE2", "/tokens/two")):
        if attached.environment.get(name) != path:
            errors.append(f"{name} must reach the test commands unchanged: {attached.environment}")
    if any(name.startswith("DPM_TRACE_IT_TOKEN_FILE") for name in config.environment):
        errors.append(f"boot mode must not export a token file: {config.environment}")

    hostile_path = "/tmp/test-%alice.token"
    hostile = load_config(lit_cfg_text, repo_root, token_files={"": hostile_path})
    hostile_result = subst("%auth", hostile)
    if "%alice" in hostile_result or hostile_path in hostile_result or hostile.environment.get("DPM_TRACE_IT_TOKEN_FILE") != hostile_path:
        errors.append(f"a token path must travel in the environment, never in the RUN line: {hostile_result!r}, {hostile.environment}")

    quoted_ledger = subst("%ledger", unsafe)
    if quoted_ledger != "'http://host/a b;touch x'":
        errors.append(f"%ledger must be shell-quoted when it holds shell metacharacters: {quoted_ledger!r}")

    if config.environment.get("DPM_TRACE_CONFIG") != "/stub/empty-config.json":
        errors.append(f"DPM_TRACE_CONFIG must reach the test commands: {config.environment}")

    if errors:
        print("substitution-order checks FAILED:")
        for err in errors:
            print(f"  - {err}")
        return 1
    print("substitution-order checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
