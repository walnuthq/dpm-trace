"""Daml-independent checks for the daml-debug-info/v1 consumer.

Exercises SourceIndex against the committed v1 fixture, DAR-embedded and
sidecar discovery (zips are built in a tempdir, no binary fixtures),
forward/backward compatibility (unknown fields, v2 skip, legacy v0), and the
runtime debug-trace loader/normalizer. Exits non-zero (printing what failed)
so it can be driven directly by lit.
"""
from __future__ import annotations

import json
import shutil
import sys
import tempfile
import zipfile
from pathlib import Path


PKG = "deadbeef" * 8
# A damlc that never resolves, so SourceIndex(dar_paths=...) cannot fall back
# to spawning a real `daml damlc inspect` during the test.
NO_DAMLC = "./no-such-damlc-for-tests"


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-debug-info.py <repo-root>", file=sys.stderr)
        return 2
    repo_root = Path(sys.argv[1]).resolve()
    sys.path.insert(0, str(repo_root / "src"))
    from dpm_trace.cli import (  # noqa: E402
        SourceIndex,
        debug_location,
        debug_trace_failed,
        load_debug_trace_events,
        normalize_debug_steps,
        slot_availability_for_event,
        TraceEvent,
    )

    fixtures = repo_root / "tests" / "fixtures" / "debug-info-v1"
    debug_info = fixtures / "asset.debug-info.json"
    errors: list[str] = []

    def check(condition: bool, message: str) -> None:
        if not condition:
            errors.append(message)

    # 1. v1 loading: templates/choices with exact spans, resolved sources.
    index = SourceIndex(debug_info_paths=[str(debug_info)])
    template_key = f"{PKG}:Asset:Asset"
    choice_key = f"{PKG}:Asset:Asset.Transfer"
    tpl = index.templates.get(template_key)
    check(tpl is not None, f"v1 template not indexed: {sorted(index.templates)}")
    if tpl is not None:
        check(tpl.line == 9 and tpl.column == 1, f"template span wrong: {tpl}")
        check(tpl.end_line == 23, f"template end line wrong: {tpl}")
        check(Path(tpl.path).name == "Asset.daml", f"template path wrong: {tpl.path}")
        check(Path(tpl.path).is_file(), f"template source did not resolve to a file: {tpl.path}")
        check(tpl.path in index.files, "resolved source was not loaded into files")
        check(index.file_modules.get(tpl.path) == "Asset", "file_modules missing v1 source")
    choice = index.choices.get(choice_key)
    check(choice is not None, f"v1 choice not indexed: {sorted(index.choices)}")
    if choice is not None:
        check(choice.line == 18 and choice.column == 5, f"choice span wrong: {choice}")
    check(bool(index.module_files.get("Asset")), "module_files missing module from v1 sources")

    # location_for_event works off the v1 keys (Module:Entity[.Choice]).
    create_ev = TraceEvent(event_id="e1", kind="create", template=f"{PKG}:Asset:Asset")
    exercise_ev = TraceEvent(event_id="e2", kind="exercise", template=f"{PKG}:Asset:Asset", choice="Transfer")
    loc = index.location_for_event(create_ev)
    check(loc is not None and loc.line == 9, f"location_for_event create wrong: {loc}")
    loc = index.location_for_event(exercise_ev)
    check(loc is not None and loc.line == 18, f"location_for_event exercise wrong: {loc}")

    # 2. Value-slot availability + symbols + steps.
    slots = index.slot_availability.get(choice_key) or []
    check(
        any(s.get("name") == "newOwner" and s.get("availability") == "transaction-visible" for s in slots),
        f"newOwner slot missing/mislabeled: {slots}",
    )
    check(
        any(s.get("kind") == "choice-controllers" and s.get("availability") == "interpreter-only" for s in slots),
        f"controllers slot missing/mislabeled: {slots}",
    )
    availability = slot_availability_for_event(index, exercise_ev)
    check(availability.get("newOwner") == "transaction-visible", f"availability map wrong: {availability}")
    check(availability.get("controllers") == "interpreter-only", f"availability map wrong: {availability}")
    # Stepper wrapper variables are aliased onto slot kinds.
    check(availability.get("choiceArgument") == "transaction-visible", f"choiceArgument alias missing: {availability}")
    check(availability.get("choiceResult") == "transaction-visible", f"choiceResult alias missing: {availability}")
    symbol = index.debug_symbols.get(choice_key)
    check(symbol is not None and symbol.get("kind") == "choice", f"debug symbol missing: {symbol}")
    check(symbol is not None and symbol.get("span") is not None, "choice symbol lost its span")
    module_symbol = index.debug_symbols.get(f"{PKG}:Asset")
    check(module_symbol is not None and module_symbol.get("span") is None, "span-less symbol should still be recorded")
    steps = index.debug_steps.get(f"{PKG}:Asset:test_transfer") or []
    check(len(steps) == 5, f"expected 5 debug steps, got {len(steps)}")
    check(steps and steps[0].line == 27 and steps[0].label == "step:Asset:test_transfer:0", f"first step wrong: {steps[:1]}")
    check(all(Path(s.path).name == "Asset.daml" for s in steps), "step source ids did not resolve to paths")

    data = json.loads(debug_info.read_text(encoding="utf-8"))

    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        shutil.copy(fixtures / "Asset.daml", tmp_path / "Asset.daml")

        # 3. DAR member discovery: META-INF/daml-debug-info/<package-id>.json.
        dar = tmp_path / "asset.dar"
        with zipfile.ZipFile(dar, "w") as archive:
            archive.writestr(f"META-INF/daml-debug-info/{PKG}.json", json.dumps(data))
            archive.writestr("asset-demo/Asset.daml", "-- packaged source placeholder\n")
        dar_index = SourceIndex(dar_paths=[str(dar)], damlc=NO_DAMLC)
        check(template_key in dar_index.templates, "DAR member debug info not loaded")
        dar_tpl = dar_index.templates.get(template_key)
        check(
            dar_tpl is not None and Path(dar_tpl.path) == tmp_path / "Asset.daml",
            f"DAR member source not resolved against the DAR parent: {dar_tpl}",
        )
        check(choice_key in dar_index.slot_availability, "DAR member slots not loaded")

        # 3b. Older prototype member name META-INF/daml-debug-info.json.
        legacy_dar = tmp_path / "legacy.dar"
        with zipfile.ZipFile(legacy_dar, "w") as archive:
            archive.writestr("META-INF/daml-debug-info.json", json.dumps(data))
        legacy_index = SourceIndex(dar_paths=[str(legacy_dar)], damlc=NO_DAMLC)
        check(template_key in legacy_index.templates, "legacy DAR member name not loaded")

        # 3c. Sidecar discovery: <name>.debug-info.json next to the DAR
        # (the DAR itself may not even be a readable zip).
        side_dar = tmp_path / "side.dar"
        side_dar.write_bytes(b"not a zip archive")
        (tmp_path / "side.debug-info.json").write_text(json.dumps(data), encoding="utf-8")
        side_index = SourceIndex(dar_paths=[str(side_dar)], damlc=NO_DAMLC)
        check(template_key in side_index.templates, "sidecar <name>.debug-info.json not discovered")

        # 3d. Sidecar variant <name>.dar.debug-info.json.
        full_dar = tmp_path / "full.dar"
        full_dar.write_bytes(b"not a zip archive")
        (tmp_path / "full.dar.debug-info.json").write_text(json.dumps(data), encoding="utf-8")
        full_index = SourceIndex(dar_paths=[str(full_dar)], damlc=NO_DAMLC)
        check(template_key in full_index.templates, "sidecar <name>.dar.debug-info.json not discovered")

        # 4. Forward compat: an unsupported major schema is skipped, not fatal.
        v2 = dict(data)
        v2["schema"] = "daml-debug-info/v2"
        v2_file = tmp_path / "v2.debug-info.json"
        v2_file.write_text(json.dumps(v2), encoding="utf-8")
        v2_index = SourceIndex(debug_info_paths=[str(v2_file)])
        check(not v2_index.templates and not v2_index.debug_symbols, "v2 schema should be skipped")

        # 5. Backward compat: a schema-less v0 debug-info file still loads.
        v0 = {
            "packageId": PKG,
            "files": [
                {
                    "path": "Asset.daml",
                    "entities": [
                        {"qualifiedName": "Asset:Asset", "kind": "template", "startLine": 9},
                        {"qualifiedName": "Asset:Asset.Transfer", "kind": "choice", "startLine": 18},
                    ],
                }
            ],
        }
        v0_file = tmp_path / "v0.debug-info.json"
        v0_file.write_text(json.dumps(v0), encoding="utf-8")
        v0_index = SourceIndex(debug_info_paths=[str(v0_file)])
        v0_tpl = v0_index.templates.get(template_key)
        check(v0_tpl is not None and v0_tpl.line == 9, f"v0 debug info regressed: {v0_tpl}")
        check(v0_index.choices.get(choice_key) is not None, "v0 choice entity regressed")

        # 6. Runtime trace loader: blank lines skipped, junk lines counted.
        noisy = tmp_path / "noisy.jsonl"
        noisy.write_text(
            '\n{"event":"script-start","script":"Asset:test_transfer"}\n'
            "definitely not json\n"
            '[1, 2, 3]\n'
            '{"event":"script-end","status":"success"}\n',
            encoding="utf-8",
        )
        events, junk = load_debug_trace_events(noisy)
        check(len(events) == 2, f"noisy trace should keep 2 events, got {len(events)}")
        check(junk == 2, f"noisy trace should count 2 junk lines, got {junk}")

    # 7. Normalization of the committed runtime trace.
    events, junk = load_debug_trace_events(fixtures / "run.debug-trace.jsonl")
    check(junk == 0, f"committed success trace should have no junk lines: {junk}")
    steps = normalize_debug_steps(events, index)
    kinds = [s.kind for s in steps]
    check("future-event-kind" not in kinds, "unknown event kinds must be ignored")
    check(
        kinds
        == [
            "script-start",
            "question",
            "submission",
            "created",
            "trace",
            "submission",
            "exercised",
            "created",
            "script-end",
        ],
        f"unexpected normalized kinds: {kinds}",
    )
    check(not debug_trace_failed(steps), "success trace flagged as failed")
    created = next(s for s in steps if s.kind == "created")
    check(created.location is not None and created.location.line == 9, f"created location wrong: {created.location}")
    check(created.values.get("owner") == "Alice::1220ab", f"created values not simplified: {created.values}")
    check(created.availability.get("owner") == "transaction-visible", f"created availability wrong: {created.availability}")
    exercised = next(s for s in steps if s.kind == "exercised")
    check(exercised.location is not None and exercised.location.line == 18, f"exercised location wrong: {exercised.location}")
    check(exercised.availability.get("newOwner") == "transaction-visible", f"exercised availability wrong: {exercised.availability}")
    check(exercised.target == "Asset:Asset.Transfer", f"exercised target wrong: {exercised.target}")
    traced = next(s for s in steps if s.kind == "trace")
    check(
        traced.location is not None and traced.location.line == 31 and Path(traced.location.path).is_file(),
        f"trace LOC did not resolve through module_files: {traced.location}",
    )
    question = next(s for s in steps if s.kind == "question")
    check(question.location is not None and question.location.line == 27, f"question stackTrace LOC wrong: {question.location}")
    check(not traced.values and not question.values, "trace/question events must not invent values")

    # 7b. Error trace: exit-status source of truth.
    events, junk = load_debug_trace_events(fixtures / "run-error.debug-trace.jsonl")
    check(junk == 1, f"error trace fixture should count 1 junk line, got {junk}")
    error_steps = normalize_debug_steps(events, index)
    check(debug_trace_failed(error_steps), "error trace not flagged as failed")
    end = error_steps[-1]
    check(end.error == "Attempt to exercise a consumed contract", f"script-end error lost: {end.error}")
    check(end.location is not None and end.location.line == 33, f"script-end location wrong: {end.location}")

    # 7c. LOC module fallback when no source is indexed for the module.
    loc = debug_location(
        {"module": "Nowhere", "definition": "run", "startLine": 5, "startCol": 2},
        SourceIndex(),
    )
    check(loc is not None and loc.path == "Nowhere.daml" and loc.line == 5 and loc.column == 2,
          f"module fallback pseudo-path wrong: {loc}")

    if errors:
        print("debug-info v1 checks FAILED:")
        for err in errors:
            print(f"  - {err}")
        return 1
    print("debug-info v1 checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
