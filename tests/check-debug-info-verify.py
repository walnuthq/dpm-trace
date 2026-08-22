"""Checks for `dpm debug-info verify`.

Builds daml-debug-info documents in a tempdir (no binary fixtures), then
confirms the verifier accepts a valid one and rejects each corruption the
specification calls out. Exits non-zero printing what failed, so lit can
drive it directly.
"""
from __future__ import annotations

import hashlib
import json
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from dpm_trace.cli import verify_debug_info  # noqa: E402

PKG = "ab" * 32


def base(digest: str) -> dict:
    return {
        "schema": "daml-debug-info/v1",
        "version": "1.0",
        "producer": {
            "tool": "damlc",
            "version": "3.4.0",
            "buildMode": "experimental",
            "features": ["source-spans", "symbols"],
        },
        "package": {
            "packageId": PKG,
            "name": "asset-demo",
            "version": "1.0.0",
            "lfVersion": "2.1",
            "sdkVersion": "3.4.0",
        },
        "sources": [
            {"id": "src:Asset", "module": "Asset", "path": "Asset.daml", "sha256": digest}
        ],
        "spans": [
            {
                "id": "span:t",
                "source": "src:Asset",
                "kind": "template-definition",
                "start": {"line": 5, "column": 1},
                "end": {"line": 10, "column": 20},
            }
        ],
        "symbols": [
            {
                "id": "sym:t",
                "kind": "template",
                "module": "Asset",
                "name": "Asset",
                "qualifiedName": "Asset:Asset",
                "span": "span:t",
                "source": "src:Asset",
            }
        ],
        "valueSlots": [
            {"id": "slot:1", "symbol": "sym:t", "kind": "signatories",
             "availability": "transaction-visible"}
        ],
        "steps": [],
    }


def codes(findings, level: str | None = None) -> set[str]:
    return {f.code for f in findings if level is None or f.level == level}


def main() -> int:
    failures: list[str] = []

    def check(name: str, ok: bool, detail: str = "") -> None:
        if not ok:
            failures.append(f"{name}: {detail}")

    root = Path(tempfile.mkdtemp())
    text = "module Asset where\n" + "".join(f"-- line {i}\n" for i in range(2, 40))
    (root / "Asset.daml").write_text(text, encoding="utf-8")
    digest = hashlib.sha256(text.encode("utf-8")).hexdigest()

    valid = base(digest)
    check("valid document has no findings", verify_debug_info(valid) == [],
          str([str(f) for f in verify_debug_info(valid)]))
    check("valid document verifies against its sources",
          verify_debug_info(valid, source_root=root) == [],
          str([str(f) for f in verify_debug_info(valid, source_root=root)]))
    check("package id agreement is checked",
          "package-mismatch" in codes(verify_debug_info(valid, package_id="cd" * 32)))

    doc = base(digest)
    del doc["version"]
    check("missing version is an error", "missing-field" in codes(verify_debug_info(doc)))

    doc = base(digest)
    doc["version"] = "2.0"
    check("major version must agree with schema", "version" in codes(verify_debug_info(doc)))

    doc = base(digest)
    doc["schema"] = "daml-debug-info/v2"
    check("unsupported schema is rejected", "schema" in codes(verify_debug_info(doc)))

    doc = base(digest)
    doc["spans"][0]["source"] = "src:nope"
    check("unresolved source reference", "unresolved-ref" in codes(verify_debug_info(doc)))

    doc = base(digest)
    doc["symbols"].append(dict(doc["symbols"][0], name="Other"))
    check("duplicate symbol id", "duplicate-id" in codes(verify_debug_info(doc)))

    doc = base(digest)
    doc["valueSlots"][0] = {"id": "s", "symbol": "sym:t", "kind": "precondition",
                            "availability": "transaction-visible"}
    check("over-permissive availability is rejected",
          "over-permissive-availability" in codes(verify_debug_info(doc)))

    doc = base(digest)
    doc["sources"][0]["path"] = "/absolute/path/Asset.daml"
    check("absolute source path is rejected", "absolute-path" in codes(verify_debug_info(doc)))

    doc = base(digest)
    doc["sources"][0]["sha256"] = "0" * 64
    check("stale source is rejected",
          "stale-source" in codes(verify_debug_info(doc, source_root=root)))

    doc = base(digest)
    doc["spans"][0]["end"] = {"line": 9999, "column": 1}
    check("span past end of file is rejected",
          "span-out-of-range" in codes(verify_debug_info(doc, source_root=root)))

    doc = base(digest)
    doc["spans"][0]["end"] = {"line": 1, "column": 1}
    doc["spans"][0]["start"] = {"line": 4, "column": 1}
    check("reversed span is rejected", "span" in codes(verify_debug_info(doc)))

    doc = base(digest)
    doc["unmappedModules"] = ["Asset"]
    check("module cannot be both mapped and unmapped",
          "unmapped-module" in codes(verify_debug_info(doc)))

    # A checkout with translated line endings warns rather than failing, and
    # says so specifically, because the file is the right one.
    crlf_root = Path(tempfile.mkdtemp())
    (crlf_root / "Asset.daml").write_bytes(text.replace("\n", "\r\n").encode("utf-8"))
    findings = verify_debug_info(base(digest), source_root=crlf_root)
    check("line-ending mismatch warns specifically",
          "line-endings" in codes(findings, "warning") and not codes(findings, "error"),
          str([str(f) for f in findings]))

    for failure in failures:
        print(f"FAIL: {failure}")
    print(f"{'FAILED' if failures else 'ok'}: check-debug-info-verify")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
