# Unit testing with `dpm trace`
`dpm trace test` runs your existing `Daml Script` test suite — no new test framework, no Canton node required. Point it at the directory containing your `daml.yaml` and the scripts file you want to exercise:

```bash
dpm trace test . --files daml/Test.daml
```
         
`dpm trace test` wraps `daml test` under the hood, collects each script's transaction tree, and renders a structured trace for every `submit` — showing creates, exercises, archives, acting parties, and payloads — so failures are immediately readable without digging through raw ledger logs.

* **The Happy-path:** Successful scripts produce a full, naturally readable event tree;
* **Expected Failures:** `submitMustFail` guards properly verify rejection and confirm no state leaked.
* **The Failure Path:** If a transaction unexpectedly aborts or an assertion fails, dpm trace intercepts the crash and outputs a source-mapped error. Instead of dumping a generic raw ledger trace, it points you directly to the exact file and line number in your Daml code where the execution failed.
  
The `--no-trees` flag suppresses per-test trees for a compact pass/fail summary, and `--color always` adds ANSI highlights useful in CI.

