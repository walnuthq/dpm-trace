import os
import sys

import lit.formats


config.name = "dpm-trace"
config.test_format = lit.formats.ShTest()
config.suffixes = [".test"]
config.test_source_root = os.path.dirname(__file__)
config.test_exec_root = os.path.join(config.test_source_root, ".lit")

repo_root = os.path.dirname(config.test_source_root)

config.substitutions.append(("%python", sys.executable))
config.substitutions.append(("%root", repo_root))
config.substitutions.append(("%damlc", os.environ.get("DPM_TRACE_DAMLC", "daml")))
config.substitutions.append(("%daml", os.environ.get("DPM_TRACE_DAML", "daml")))

for key in (
    "HOME",
    "DPM_TRACE_DAML",
    "DPM_TRACE_DAMLC",
    "DPM_TRACE_CANTON_JAR",
    "DPM_TRACE_DAML_HELPER",
    "DPM_TRACE_PYTHON",
):
    if key in os.environ:
        config.environment[key] = os.environ[key]

if os.environ.get("DPM_TRACE_RUN_DAMLC_INSPECT") == "1":
    config.available_features.add("damlc-inspect")

if os.environ.get("DPM_TRACE_RUN_REAL_CANTON") == "1":
    config.available_features.add("real-canton")

if os.environ.get("DPM_TRACE_RUN_DAML_TEST") == "1":
    config.available_features.add("daml-test")
