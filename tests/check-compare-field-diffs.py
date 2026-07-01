"""Driver for the prepared-vs-update comparison test.

Loads fixture files, runs compare_prepared_to_trace, and prints the result.
No ledger connection required.
"""
import json
import sys
from pathlib import Path

root = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(root / "src"))

from dpm_trace.cli import (  # noqa: E402
    Color,
    compare_prepared_to_trace,
    load_prepared_artifact,
    print_comparison,
    trace_from_json,
)

fixtures = root / "tests" / "fixtures" / "compare"
prepared = load_prepared_artifact(fixtures / "prepared.json")
trace_data = json.loads((fixtures / "trace-a.json").read_text(encoding="utf-8"))
trace = trace_from_json(trace_data["trace"])
comparison = compare_prepared_to_trace(prepared, trace)
print_comparison(comparison, Color(enabled=False))
