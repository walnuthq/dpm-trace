# Examples

Representative Daml examples and the traces they produce: a **create**, an
**exercise with a child create**, an **archive (consuming exercise)**, and a
**reassignment** seen from both synchronizers.

Each one ships as a committed trace artifact, so you can see the output without
a Canton node, a Daml toolchain, or a network:

```bash
dpm trace open examples/create.trace.json
dpm trace open examples/exercise-child-create.trace.json
dpm trace open examples/archive.trace.json
dpm trace open examples/unassign.trace.json
dpm trace open examples/assign.trace.json
```

The Daml package behind the first three is in [`asset/`](asset), and the steps
below rebuild them from it. Reproducing them needs DPM 1.1 or newer with SDK
3.7.1, Java 17 or newer, and access to github.com: `Asset` implements the Splice
Token Standard `Holding` interface, and its `daml.yaml` pulls the Splice DARs
from Git, pinned to a commit (see [Git DAR dependencies](#git-dar-dependencies)). Opening the
committed artifacts above needs neither.

## The shapes

| Artifact | Daml | What the trace shows |
| --- | --- | --- |
| `create.trace.json` | `createCmd Asset` | One create event: payload, signatory, observer, witnesses |
| `exercise-child-create.trace.json` | `Split` | A consuming exercise with two child creates nested under it, and the tuple of new contract ids as its result |
| `archive.trace.json` | `Burn` | A consuming exercise that archives without creating anything |
| `unassign.trace.json` | `Iou` | The outgoing half of a reassignment: source and target synchronizer, reassignment id, counter, submitter |
| `assign.trace.json` | `Iou` | The incoming half of the same reassignment, which additionally carries the contract's payload and stakeholders |

`Asset` is signed by `issuer` and observed by `owner`, so the projections differ
per party — which is the point of the participant-scoped labelling in the output.

## The reassignment pair

`unassign.trace.json` and `assign.trace.json` are two updates describing one
contract moving between synchronizers, so they share a contract id, a
reassignment id and a counter:

```
└── [0] UNASSIGN Iou:Iou
    ├── contract: 00e89575764e68ce627d51fa4f136402cc3320fce88707c58a20c08ddab8768...
    ├── reassignment: sync-a::1220e19b470d09961acb2d17952d2... -> sync-b::1220b13f96ccbe9db7875e1d6f72a...
    ├── reassignment id: 0012200ce3d189576eed2c5bfeb7e4ee84162e6bd9b7a406e2341d110a89230...
    ├── counter: 1
    ├── submitter: Alice
    └── witnesses: Alice
```

The synchronizer on the header line differs between the two — `sync-a` for the
unassign, `sync-b` for the assign — because each update is committed on the
synchronizer that saw that half.

These were captured against a ledger with two synchronizers, which
[`devnet-trace.conf`](devnet-trace.conf) does not set up: it runs one sequencer
and one mediator, so the steps below reproduce the first three artifacts only.

## Reproducing them against a local Canton

[`up.sh`](up.sh) boots [`devnet-trace.conf`](devnet-trace.conf) — two
participants with fixed ports — in the background, uploads the DAR, allocates
the parties and exports their ids:

```bash
source examples/up.sh   # sets LEDGER, ISSUER, ALICE, BOB
```

The same exports are kept in `examples/.env` for other shells.

It uses the shipped DAR (building it with `dpm install package` and `dpm build` if missing) and the Canton
3.7.1 jar from the DPM cache, or `$CANTON_JAR`. Canton logs to
`examples/.up/canton.log`; stop it with `./examples/up.sh down`.

Beware `.dpm-trace.json`: config discovery walks up from the current directory,
so a file with placeholder values will silently supply them — a placeholder
party reaches the ledger as `non expected character 0x2e in Daml-LF Party`. Run
these commands outside a directory that has one, or pass `--config`.

**create** — the issuer is the signatory, so it submits:

```bash
CREATE_ID=$(dpm trace submit --submitter "$LEDGER" --act-as "$ISSUER" \
  --template '#asset:Asset:Asset' \
  --arg issuer="$ISSUER" --arg owner="$ALICE" --arg name=GOLD --arg quantity=100)

dpm trace "$CREATE_ID" --submitter "$LEDGER" --read-as "$ISSUER" \
  --export examples/create.trace.json
```

**exercise with child create** — `Split` creates two assets from one. The owner
controls the choice; the issuer's authority comes from the parent contract:

```bash
CID=$(dpm trace "$CREATE_ID" --submitter "$LEDGER" --read-as "$ISSUER" --print-json \
  | python3 -c 'import sys,json; d=json.load(sys.stdin); print(next(e["contractId"] for e in d["eventsById"].values() if e.get("contractId")))')

SPLIT_ID=$(dpm trace submit --submitter "$LEDGER" --act-as "$ALICE" \
  --template '#asset:Asset:Asset' --choice Split \
  --contract-id "$CID" --arg splitQuantity=40)

dpm trace "$SPLIT_ID" --submitter "$LEDGER" --read-as "$ISSUER" \
  --export examples/exercise-child-create.trace.json
```

**archive / consuming exercise** — `Burn` archives with no child create. Note it
burns one of the contracts `Split` produced: `Split` is consuming, so the
original `$CID` no longer exists.

```bash
BURN_CID=$(dpm trace "$SPLIT_ID" --submitter "$LEDGER" --read-as "$ISSUER" --print-json \
  | python3 -c 'import sys,json; d=json.load(sys.stdin); print(next(e["contractId"] for e in d["eventsById"].values() if e.get("kind")=="create"))')

BURN_ID=$(dpm trace submit --submitter "$LEDGER" --act-as "$ALICE" \
  --template '#asset:Asset:Asset' --choice Burn --contract-id "$BURN_CID")

dpm trace "$BURN_ID" --submitter "$LEDGER" --read-as "$ISSUER" \
  --export examples/archive.trace.json
```

A consuming choice is how an archive appears in a participant projection: the
event is the `EXERCISE ... consuming: true`, not a separate archive event.

## Running the tests

`asset/` also carries Script unit tests in `unittests/` and lit integration
tests in `itests/`. The unit tests sit in their own package so the asset DAR
does not pull in `daml-script`; they depend on that DAR, so build both first:

```bash
cd examples/asset
dpm install package                    # fetches the git-pinned Splice DARs
(cd unittests && dpm install package)
dpm build --all
dpm trace test unittests --files daml/Test.daml \
  --daml-yaml unittests/daml.yaml --daml-yaml daml.yaml \
  --dar .daml/dist/asset-1.0.0.dar
dpm trace test . --integration itests --canton-jar <path-to-canton.jar>
```

`unittests/daml/FailureDemo.daml` fails on purpose to show a source-mapped
failure, so without `--files daml/Test.daml` the run exits 1. The asset
`daml.yaml` and DAR let failures map into `Asset.daml`.

## Against an authorized remote participant

The command shape is identical; a remote endpoint additionally needs a bearer
token. Pass it from a file rather than an argument so it stays out of your shell
history and process list:

```bash
dpm trace "$UPDATE_ID" \
  --submitter https://participant.example.com \
  --read-as "$ALICE" \
  --token-file ~/.config/dpm-trace/token
```

`DPM_TRACE_TOKEN_FILE` sets the same thing in the environment, and
`.dpm-trace.json` can hold the participant URL and party so they need not be
repeated on every invocation — see
[Configuration](../docs/commands.md#configuration) for its shape.

Output is always **one participant's projection** — what that participant is
authorized to see, not a global view of the transaction. A remote participant
therefore shows you its own projection, which may legitimately contain fewer
events than your local one.

## Git DAR dependencies

`asset/daml.yaml` and `asset/unittests/daml.yaml` do not vendor the Splice
Token Standard DARs. They name them as `git:` lines pinned to a commit of
`canton-network/splice` (DPM 1.1+, SDK 3.6+):

```yaml
data-dependencies:
  - "git:github.com/canton-network/splice#<sha>?path=daml/dars/splice-api-token-holding-v1-1.0.0.dar"
```

`dpm install package` fetches them into `~/.dpm/cache/git`. `dpm build` does not
fetch them, so run the install first on a fresh machine or CI runner. The first
fetch clones the Splice repository (a few hundred MB). To move to another
Splice version, pass the DAR's GitHub URL to `dpm add dar <url>
--data-dependencies`; it rewrites the line with the new pin.
