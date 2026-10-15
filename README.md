# dpm trace

A DPM component for inspecting Canton transactions.

`dpm trace <update-id>` reads a committed update from a participant's JSON
Ledger API and renders it as an event tree: creates, exercises, archives and
reassignments, with the contract ids, parties, choice arguments, return values
and payloads that participant can see.

![dpm trace](docs/demo.gif)

## Install

Download the archive for your platform from
[Releases](https://github.com/walnuthq/dpm-trace/releases) and unpack it. The
binary is self-contained — no runtime, no dependencies:

```bash
tar xzf dpm-trace_<version>_<os>_<arch>.tar.gz
./dpm-trace --version
```

Building instead needs Go 1.25 or newer:

```bash
go build -o dpm-trace ./cmd/trace
```

**That is enough to use it.** Everything below works with `./dpm-trace`.

Optionally, register it as a DPM component so it runs as `dpm trace`:

```bash
./dpm-trace install-plugin
dpm trace --help
```

Registration requires DPM with a Daml SDK installed. If you have neither:

```bash
curl https://get.digitalasset.com/install/install.sh | sh   # installs dpm
dpm install 3.5.1                                           # installs an SDK
```

Otherwise keep using `./dpm-trace`; it is the same binary.

### Requirements

| To | You need |
| --- | --- |
| Open committed artifacts, read and compare captured files | the binary only |
| Trace, prepare and submit against a participant | a reachable JSON Ledger API (Canton 3.x) |
| Build the example DAR and run a local Canton | DPM with SDK 3.5.1 (`dpm install 3.5.1`) and Java 17 or newer |

## Usage

An update id identifies a committed transaction on a participant, in the format
`1220e77482b473bfff30d376bd853f0a71df7ab6d41cc3f060dc5456603493acd06c`. Canton
returns one from each successful submission, so if you have no ledger yet, start
with the [examples](#examples) below — they need no update id and no
participant.

Against a local participant:

```bash
dpm trace <update-id> \
  --submitter http://localhost:<json-ledger-api-port> \
  --read-as '<party-id>'
```

Against an authorized remote participant, which additionally needs a bearer
token:

```bash
dpm trace <update-id> \
  --submitter https://participant.example.com \
  --read-as '<party-id>' \
  --token-file ./token.txt
```

Write the trace as a portable JSON artifact for downstream tools, or print it to
stdout:

```bash
dpm trace <update-id> --export trace.json
dpm trace <update-id> --print-json
```

`dpm trace open <artifact>` renders a saved artifact again, with no ledger
connection.

Flags and usage in full: **[docs/commands.md](docs/commands.md)**.

## Interactive visualizer

![dpm trace --visualize](docs/visualizer.gif)

`--visualize` opens a session over the transaction instead of printing it:

```bash
dpm trace <update-id> --submitter <url> --read-as '<party-id>' --visualize
dpm trace open trace.json --visualize
```

Step through events, fold deep trees, filter by template, choice, party or
contract id, search inside large payloads, and list what the transaction
created and archived. The prompt names the parties you are reading as, because
the session shows one participant's projection and not a global record of the
transaction.

**[docs/visualizer.md](docs/visualizer.md)** covers the session in full.

## Prepared transactions, failures and diffs

![dpm trace compare](docs/m3.gif)

Not every submission becomes a transaction, and not every question is about one
transaction:

```bash
dpm trace submit --submitter <url> --act-as '<party-id>' ...    # commit one, get its id
dpm trace prepare --submitter <url> --act-as '<party-id>' ...   # compute it without committing
dpm trace --completion-file failure.json                        # a rejection captured with submit --export
dpm trace --command-id <command-id>                             # a rejection that reached the ledger
dpm trace compare a.trace.json b.trace.json                     # what differs
```

A failed submission has no update id, so `dpm trace <update-id>` cannot find
it — completion data is where its outcome lives, and a captured completion file
reads back with no ledger at all.

Which of the two to use depends on where the submission failed:

| Failure | Example | Completion on the ledger? | Read it with |
| --- | --- | --- | --- |
| Before anything is sequenced | a failed `assert`, a contract archived before you submitted (`CONTRACT_NOT_FOUND`), missing authorization, any other interpretation error, a reused command id (`DUPLICATE_COMMAND`) | no | `--completion-file` |
| After sequencing, during confirmation | contention over the same contract (`LOCAL_VERDICT_LOCKED_CONTRACTS`), a contract archived by a concurrent transaction, a mediator timeout, any other rejection after sequencing | yes | `--command-id` or `--completion-file` |

Most failures you meet while developing are the first kind, so capture the
rejection when you submit:

```bash
dpm trace submit --submitter <url> --act-as '<party-id>' ... --export failure.json
dpm trace --completion-file failure.json
dpm trace compare --prepared prepared.json --completion-file failure.json
```

`--command-id` asks the participant's completion stream, so it only finds the
second kind, but it needs nothing captured: it works for a command your
application submitted. Two submissions racing for the same contract, for
example:

```bash
dpm trace --command-id <command-id> --submitter <url> --act-as '<party-id>'
#   result:     completion failed
#   status:     10
#   message:    LOCAL_VERDICT_LOCKED_CONTRACTS(2,...): Rejected transaction is referring to locked contracts
dpm trace compare --prepared prepared.json --command-id <command-id> --submitter <url> --act-as '<party-id>'
```

A command rejected before sequencing has no completion to find. For a reused
command id, the lookup finds the original submission, not the rejection.

`compare` answers what changed: between what was prepared and what committed,
between a prepared command and the completion it produced, or between two
committed transactions. It names every field that moved, in child events as
well as in the command itself: the prepared side is the full transaction the
participant computed, not only the command you sent.

**[docs/commands.md](docs/commands.md)** covers all of it, including
[what can and cannot be compared](docs/commands.md#what-cannot-be-compared).

## Examples

[`examples/`](examples) covers the four event kinds — a create, an exercise
with a child create, an archive, and a reassignment — each with a trace
artifact captured against a real Canton, so the output can be seen without a
ledger:

```bash
./dpm-trace open examples/create.trace.json
./dpm-trace open examples/exercise-child-create.trace.json
./dpm-trace open examples/archive.trace.json
./dpm-trace open examples/unassign.trace.json
./dpm-trace open examples/assign.trace.json
```

These need nothing but the binary — the artifacts are committed, so there is no
ledger to reach and no Daml toolchain to install.

[`examples/README.md`](examples/README.md) shows how to reproduce the first
three against a local Canton, and how the same commands work against a remote
participant. That needs DPM with SDK 3.5.1 and Java 17 or newer (see
[Requirements](#requirements)). The reassignment pair needs two
synchronizers, which the shipped config does not set up.

## Notes

- Output is one participant's projection, not a global view of the transaction.
  It does not imply access to private data outside that participant's rights.

## Development

```bash
go build -o /tmp/dpm-trace ./cmd/trace
DPM_TRACE_BIN=/tmp/dpm-trace lit tests
go test ./...
```

`lit` and `FileCheck` come from pip: `pip install lit filecheck`.

[AGENTS.md](AGENTS.md) describes the code layout and contribution rules;
[docs/](docs) holds the rest of the documentation.

## License

Apache License 2.0. See [LICENSE](LICENSE).
