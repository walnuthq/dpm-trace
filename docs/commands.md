# Command reference

Full flags and workflows for `dpm trace`. The [README](../README.md) covers
installation and a quick tour; this is the detail behind it.

## Configuration

An optional per-project config saves repeating connection flags:

```bash
cp .dpm-trace.example.json .dpm-trace.json
```

```json
{
  "ledgerUrl": "http://localhost:<json-ledger-api-port>",
  "readAs": "<party-id>",
  "darPaths": ["./path/to/app.dar"]
}
```

`--config PATH` names the file explicitly. Otherwise `DPM_TRACE_CONFIG` does, then
the nearest `.dpm-trace.json` up to the project root. Every command's config
lookup honours `DPM_TRACE_CONFIG`, but only the integration runner sets it, and
only in attach mode (`test --integration --ledger-url`), so it is not a setting
for users. There it points at a copy of the project config
without `token`, `tokenFile`, `ledgerUrl` and `scanUrl`, so those reach test
commands only through the flags a suite passes them. The other keys, such as
`readAs`, `party`, `damlYaml` and `sourceRoots`, still apply. Like `--config`, a
`DPM_TRACE_CONFIG` that cannot be read is an error.

## trace

Inspect a committed transaction:

```bash
dpm trace <update-id>
```

With explicit participant context:

```bash
dpm trace <update-id> \
  --submitter http://localhost:<json-ledger-api-port> \
  --read-as '<party-id>' \
  --access-token-file ./token.txt
```

The bearer token can also come from `--token`, `DPM_TRACE_TOKEN` or
`DPM_TRACE_TOKEN_FILE`.

`--visualize` opens the interactive session over the trace instead of printing
it; see [visualizer.md](visualizer.md).

Write a portable JSON artifact for downstream tools, or print it to stdout:

```bash
dpm trace <update-id> --export trace.json
dpm trace <update-id> --print-json
```

Because a committed update reaches other participants asynchronously, `--wait
<seconds>` retries until it becomes visible.

### Event kinds

The tree renders create, exercise, archive and reassignment events. A
reassignment shows as `UNASSIGN` on the source synchronizer and `ASSIGN` on the
target, each with the source/target synchronizer ids, the reassignment id and
counter, and the submitter:

```
└── [0] UNASSIGN Asset:Asset
    ├── contract: 00aabbcc0011...
    ├── reassignment: sync-a::... -> sync-b::...
    ├── reassignment id: reassign-0001
    ├── counter: 1
    ├── submitter: Alice
    └── witnesses: Alice
```

An `ASSIGN` event also carries the reassigned contract's payload and
stakeholders, which the Ledger API nests inside the assigned event's created
event.

Both halves of one real reassignment are committed as
[`examples/unassign.trace.json`](../examples/unassign.trace.json) and
[`examples/assign.trace.json`](../examples/assign.trace.json).

## open

Reopen an exported artifact, with no ledger connection:

```bash
dpm trace open trace.json
```

## prepare

Build a command and have the participant compute its transaction *without*
committing it — the interactive-submission prepare step:

```bash
dpm trace prepare --submitter <url> --act-as '<party-id>' \
  --template '#pkg:Asset:Asset' --choice Transfer \
  --contract-id '<contract-id>' --arg newOwner='<party-id>' \
  --export prepared.json
```

Commands can also come from a file (`--commands commands.json`), a JSON string
(`--command-json`), or `--arg key=value` pairs.

The result is labelled for what it is (header shortened):

```
Prepared command
  committed:    no
  command id:   dpm-trace-prepare-04427c1b4565
  prepared hash:FtItr2nW1XjyV+FNOyU16Wib3D1ccZrWewE42KtyBwE=
  hashing:      HASHING_SCHEME_VERSION_V2

Transaction (as the participant would commit it)
  └── [0] EXERCISE Asset:Asset.Transfer
      ├── contract: 0059f0a55ba182af89abbf3213b2df6bbdeaba0409a9d41080573864051d7e5...
      ├── consuming: true
      ├── actors: Alice
      ├── argument: { newOwner: Bob }
      ├── result: 006735a2d9b35ffbaf26328c528d4e30574c6a27788b8d492ef3ae2ac1b4acce6e (local; assigned at commit)
      └── [1] CREATE Asset:Asset
          ├── contract: 006735a2d9b35ffbaf26328c528d4e30574c6a27788b8d492ef3ae2ac1b4acce6e (local; assigned at commit)
          ├── signatories: Issuer
          ├── observers: Bob
          └── payload: { issuer: Issuer, owner: Bob, name: SILVER, quantity: 100 }
```

The transaction is decoded from the participant's `preparedTransaction`, so it
shows every event the command would produce, children included, not only the
command you sent.

**Nothing has happened on the ledger.** A prepared transaction is what the
participant *would* commit if the submission were signed and sent. No contract
exists, no party has been notified, and the prepared hash is not an update id.
A created contract's id is local: the ledger assigns the real one at commit, so
it cannot be looked up. `dpm trace open prepared.json` reopens it later, and
`--visualize` steps through it.

## submit

Submit a command and print the update id it produced, so there is something to
trace:

```bash
UPDATE_ID=$(dpm trace submit --submitter <url> --act-as '<party-id>' \
  --template '#pkg:Asset:Asset' --arg owner='<party-id>' --arg quantity=100)

dpm trace "$UPDATE_ID" --submitter <url> --read-as '<party-id>'
```

It takes the same command flags as `prepare` — `--template`, `--choice`,
`--contract-id`, `--arg`, `--args-json`, `--args-file`, `--commands`,
`--command-json` — and differs only in committing rather than stopping at the
prepared transaction. A submission carries one or more commands and produces
one transaction, so one update id comes back however many commands went in.

When a submission is rejected there is no update id, and `submit` prints the
rejection instead: the status, the error and the choice it failed in. `-v`
renders the whole completion rather than the summary, and `--allow-fail` exits
0 so a script expecting a failure does not abort. Both apply only to that
rejection output — a submission that commits prints its update id and nothing
else. `--export` writes the raw response, the submission's own JSON rather than
a trace artifact, which is how a failure gets captured for `--completion-file`
later.

`prepare` and `submit` take the same command flags but not the same options.
The flags that render a rejection — `-v`/`--verbose` (also spelled `--full`),
`--allow-fail`, `--log-file`, `--color` and the source diagnostics — belong to
`submit`, since `prepare` never produces a completion. Each command rejects
what the other owns rather than accepting and ignoring it.

Both take `--synchronizer-id` to pick the synchronizer to submit or prepare
against. Given one the participant cannot reach, it says so:

```
✗ submission failed  INVALID_PRESCRIBED_SYNCHRONIZER_ID
```

## --command-id and --completion-file

A submission that fails never becomes a transaction, so it has no update id and
`dpm trace <update-id>` cannot find it. Its outcome lives in completion data
instead:

```bash
dpm trace --command-id <command-id> --submitter <url> --act-as '<party-id>'
dpm trace --completion-file completion.json
```

The first searches the participant's completion stream; widen it with
`--begin-exclusive` and `--completion-limit` if the submission is older than
the default window. The second reads a captured file, which needs no ledger
connection and is how a failure gets attached to a bug report.

Either way the view states the absence rather than inventing an id:

```
  update id:  -
  trace:      no committed transaction tree is available for this completion
```

**A command rejected during interpretation never reaches the ledger**, so it
produces no completion at all and `--command-id` will not find it because there
is nothing to find. Capture the submission response and use `--completion-file`.

| Failure | Example | `--command-id` | `--completion-file` |
| --- | --- | --- | --- |
| Before sequencing | a failed `assert` (`DAML_FAILURE`), `CONTRACT_NOT_FOUND`, missing authorization, a reused command id (`DUPLICATE_COMMAND`) | no completion | yes |
| After sequencing | contention over the same contract (`LOCAL_VERDICT_LOCKED_CONTRACTS`), a contract archived by a concurrent transaction, a mediator timeout | yes | yes |

`--completion-file` therefore works for every rejection, which is why the
failure summary `submit` prints suggests it:

```
  Next
    dpm trace compare --prepared <prepared.json> --completion-file failure.json
```

That is the line when `--export failure.json` was given. Without `--export`
there is no file yet, so the summary says to rerun with it first.

`--log-file` correlates a completion with operator logs, matching on command
id, submission id, trace id and correlation id. It is optional and local; the
tool never needs log access to explain a failure.

## compare

Five shapes, all of which answer "why do these two differ":

```bash
# two committed transactions
dpm trace compare a.trace.json b.trace.json
dpm trace compare <update-id-a> <update-id-b> --submitter <url> --read-as '<party>'

# a prepared command against what actually committed
dpm trace compare --prepared prepared.json --update trace.json

# a prepared command against a failure
dpm trace compare --prepared prepared.json --command-id <id> --submitter <url> --act-as '<party>'
dpm trace compare --prepared prepared.json --completion-file completion.json
```

`-v` prints the verbose form; the default is compact. `--print-json` emits
the comparison for scripting.

### What can be compared

**Between two committed transactions:** the event tree, contracts created and
archived, parties, choice arguments, payloads and return values. Differences
are reported per event:

```
  Events
    ~ exercise Asset:Asset.Transfer    newOwner: Bob::1220bc0a... → Mallory::1220ffff...
```

**Between a prepared command and a committed update:** the whole transaction,
event by event. The prepared side is the transaction the participant computed,
decoded from the artifact's `preparedTransaction`, so a difference in a child
event is reported as well as one in the command itself:

```
  Events
    = exercise Asset:Asset.Transfer                      (1 field matches)
      ~ create Asset:Asset                                 quantity: 100 → 1
```

The prepared transaction is projected the way the committed one was: only the
events the committed trace's `--read-as` parties witness, without fetches or
rolled-back subtrees, which a committed transaction does not report. Contracts
the transaction creates get a new id each time a command is interpreted, so
they are compared by position — the first contract created on one side against
the first on the other.

An artifact without a decodable `preparedTransaction` falls back to comparing
the commands against the root events, and the output says that child events
were not compared.

**Between a prepared command and a completion:** the command id (reported as
`match` when they are the same submission), completion status, error details,
submission id, offset, trace context and synchronizer time.

### What cannot be compared

**A failed submission has no transaction tree.** The Ledger API reports a
rejection, not an update, so there are no events, no contracts and no payloads
to diff. A prepared-versus-completion comparison tells you the submission
failed and why; it cannot tell you which event failed, because no events were
recorded.

**Archived contracts carry no payload.** A transaction tree reports an archive
as `consuming: true` on the exercise, which names the contract that ceased to
exist but not its fields. A diff can say a contract was archived, not what it
contained — except for a contract this same transaction created, whose fields
are already in the trace and which the state diff marks `~ transient`.

**Two participants' projections are not directly comparable.** Each participant
sees only what its parties are entitled to, and numbers only the events it
witnessed, so the same transaction read on two participants legitimately
differs in both event count and event ids. A diff across projections reports
those as differences, which is accurate about the artifacts and misleading
about the transaction. Compare like with like: same participant, same
`--read-as`.

**Absence is not evidence.** A contract or event missing from one side may be
outside that projection rather than absent from the ledger.

**Prepared hashes are not comparable across hashing schemes.** The prepared
hash is only meaningful alongside its `HASHING_SCHEME_VERSION`.

**Created contracts are numbered only where the projection sees them.** A
visible event can hold the id of a contract created in a subtree the `--read-as`
parties do not witness. Neither side numbers that contract, so its local
prepared id against the committed one is reported as a difference.

**A top-level `None` of a nested Optional reads as `null` on the prepared
side.** The JSON Ledger API writes it as `[]`, but the prepared transaction does
not carry the type that says it is nested, so a field of type
`Optional (Optional a)` set to `None` is reported as `null → []`.

## test --integration against an existing participant

By default `dpm trace test --integration <dir>` boots its own Canton from
`--canton-jar`. Pass `--ledger-url` to attach to participants that are already
running instead. The DAR is uploaded and the parties are allocated over the JSON
Ledger API, so the token needs participant admin rights. The token's user (read
from `GET /v2/authenticated-user`) is granted `CanActAs` on the parties placed on
its participant and `CanReadAs` on the others. Those rights are revoked when the
run ends; the parties and the DAR stay on the network.

```bash
dpm trace test --integration itests --parties Alice@1,Bob@2 \
  --ledger-url http://localhost:<participant1-json-port> --token-file ./token1.txt \
  --ledger-url http://localhost:<participant2-json-port> --token-file ./token2.txt
```

- Attach mode is enabled only by `--ledger-url` on the command line, never by
  `DPM_TRACE_LEDGER_URL` or `.dpm-trace.json`. `--ledger-url` repeats, with one
  value per participant: the highest `@N` in `--parties`. It must be an http or
  https URL with a hostname and no `user:password@`; credentials in a URL are
  rejected, and never echoed, in favour of `--token-file`. A query (`?`) or
  fragment (`#`) is rejected too, and not echoed, because the API paths are
  appended to the URL as text. Combining it with
  `--canton-jar`, even an empty one, is an error; an ambient `DPM_TRACE_CANTON_JAR`
  is ignored.
- Tokens come only from `--token-file`, which repeats and pairs with
  `--ledger-url` by position. Give none for participants that need no token,
  otherwise exactly one per `--ledger-url`. Without a token file a participant is
  treated as unauthenticated: it has no user, so no rights are granted or
  revoked. `--token-file` without `--ledger-url` is an error, and so are an empty
  or malformed `--ledger-url` and a token file that is unreadable or empty, all
  reported before the DAR is built.
- Each party id gets a per-run suffix, so re-runs on a long-lived network do not
  collide. `DPM_TRACE_IT_<NAME>` still carries the full id.
- `--canton-timeout` is the time each party has to become visible on every
  participant, as one deadline that also bounds each request.
- Token files are re-read on every request, so a refresh loop keeps a suite
  longer than the token lifetime working. A suite command that outlives its
  token, such as `trace --wait`, reads the file once when it starts, not again
  while it waits. Replace a token file by renaming a new file over it, so a
  request never reads a half-written token.
- The suite's commands run with the project `.dpm-trace.json` minus its
  connection keys (`DPM_TRACE_CONFIG`, see Configuration), so a project config
  can neither supply a token nor an endpoint. Only what `%auth` passes reaches
  them. The runner also drops every `DPM_TRACE_IT_*`, `DPM_TRACE_TOKEN*`,
  `DPM_TRACE_LEDGER_URL` and `DPM_TRACE_SCAN_URL` variable it did not set itself
  from the environment lit receives. In both modes it drops any inherited
  `DPM_TRACE_IT_TOKEN_FILE*`, so a stale token path can never become a
  `--token-file` flag. On Windows, where environment names ignore case, these
  comparisons ignore case and the runner's own value wins over a differently
  cased duplicate. This needs the suite's `lit.cfg.py` to forward
  `DPM_TRACE_CONFIG`, as the generated one does. The check is a best-effort
  guard: it reads the one lit configuration lit would execute, the first that
  exists of `lit.site.cfg.py`, `lit.site.cfg`, `lit.cfg.py` and `lit.cfg` in the
  nearest directory that has any, walking up from the test directory, and
  looks for an uncommented mention of `DPM_TRACE_CONFIG`. It does not run the
  configuration, so a mention that is never executed, or a forward done by a
  file the configuration loads, is not understood. A suite whose configuration
  does not mention it, such as one scaffolded before attach mode, is refused with
  exit code 2 and the lines to add, before anything is uploaded. So is a
  test directory with no lit configuration in it or above it, which cannot be
  isolated. The guard judges only the suite's own configuration, the one it
  finds from the test directory. lit also loads the configuration of any nested
  suite below it, and the guard does not read those, so every nested suite with
  its own lit configuration must forward `DPM_TRACE_CONFIG` too.
- A party whose upper-cased name would make `DPM_TRACE_IT_<NAME>` one of the
  variables the runner sets for something else (`LEDGER` or `LEDGER<N>`,
  `TOKEN_FILE` or `TOKEN_FILE<N>`, `DAR`, `DAML`, `DAML_YAML`, `DPM`, `PYTHON`,
  `SRC`) is refused with exit code 2 before anything is provisioned, in both modes.
- The rights granted during setup are revoked when the run ends, including when
  lit fails or the run receives SIGINT, SIGTERM or SIGHUP (exit code 128 plus the
  signal number), or when the runner's own output closes, for example under
  `| head`: when the runner's own write gets EPIPE it cleans up and exits with
  code 141. If the output closes while lit is running, lit 23.1.6 can hang
  and the rights stay in place until the run receives a signal; do not pipe a
  long run into a command that exits early. A grant request already sent is allowed to finish, for up to 30
  seconds, before the run moves on. If it times out or ends without a usable
  answer, the participant may still apply it after the revoke has run: the run
  revokes anyway and prints a warning naming the user, the parties and the
  participant as possibly still granted, so check them. A bare SIGPIPE, such as one raised
  by a participant that closes a connection early, is ignored. Further signals
  during cleanup are ignored, and each revoke request times out after 10
  seconds. A revoke that gets no 2xx answer prints a warning to stderr naming
  the user and parties to clean up. SIGKILL cannot be caught, so a killed run
  leaves its rights behind: revoke them by hand.
- If a participant already has a package with the same name and version but
  different content, the run stops and tells you to bump `version` in
  `daml.yaml` and rebuild, instead of printing Canton's `KNOWN_PACKAGE_VERSION`
  error.
- Every token file is exported as `DPM_TRACE_IT_TOKEN_FILE`, then
  `DPM_TRACE_IT_TOKEN_FILE2`, `DPM_TRACE_IT_TOKEN_FILE3` and so on, beside
  `DPM_TRACE_IT_LEDGER`, `DPM_TRACE_IT_LEDGER2`, ... The generated `lit.cfg.py`
  turns the first two into `%auth` and `%auth2`; a suite with more participants
  adds its own substitutions for the rest. `%ledger` and `%ledger2` are
  shell-quoted.
- In the tests, `%auth` expands to `--token-file "$DPM_TRACE_IT_TOKEN_FILE"` for
  participant 1 and `%auth2` to `--token-file "$DPM_TRACE_IT_TOKEN_FILE2"` for
  participant 2, or to nothing when no token file was given or the suite runs in
  boot mode. The path travels in the environment lit gives the test commands,
  never in the RUN line, so a path that contains text lit substitutes, such as
  `%alice`, arrives unchanged. Pass it to every command that talks to a
  participant, next to `--submitter`:
  `%dpm submit --submitter %ledger2 %auth2 ...`.
