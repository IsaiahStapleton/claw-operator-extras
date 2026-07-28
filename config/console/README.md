# Agent Console manifests

Deploys the Agent Console: one console you log into that shows the agents of
every Claw in namespaces you have access to.

## Install

The console runs in its own namespace, not in a Claw's.

```sh
oc new-project agent-console

# The oauth-proxy signs its browser session cookie with this key, and will not
# start without it. It is a credential, so it is created here rather than
# checked in. oauth-proxy base64-decodes the value and uses it as an AES key,
# so it must decode to 16, 24, or 32 bytes — 24 here, matching the deployer's
# own openclaw-deployer-cookie.
oc create secret generic agent-console-cookie \
  --from-literal=session_secret="$(head -c 24 /dev/urandom | base64)"

oc apply -k config/console
oc get route agent-console -o jsonpath='{.spec.host}{"\n"}'
```

The console claims a 1 GiB `ReadWriteOnce` volume for what it has observed about
each Claw's memory notes. Because that volume attaches to one node at a time,
the Deployment uses the `Recreate` strategy — a rolling update would deadlock
waiting for the old pod to release it.

If the `oauth-proxy` container crash-loops on startup, this secret is the first
thing to check: a value that does not decode to a valid AES key length fails
there rather than at apply time.

## How access works

The console holds **no standing permission to read any Claw's data**. It has
one privilege: impersonating the logged-in user. Every Kubernetes call — both
listing Claws and reading session files — is made as that user, so the API
server decides what they see.

That matters because agent transcripts contain whatever the agent saw,
including tool output. Enforcing in the API server rather than filtering in
application code means a bug in this console cannot leak one tenant's
transcripts to another.

In practice a user sees a Claw's agents when they can:

- `list` Claws (populates the picker), and
- `create pods/exec` in that namespace (reads the session files).

Exec is the effective bar. A user with view-only access to a namespace will
see the Claw in the picker but get a permission error opening it. If that is
the wrong bar for your users, the data path is behind an interface — see the
gateway-API note in the repo README.

## Why exec rather than mounting the volumes

Claw home PVCs are ReadWriteOnce. A volume attaches to one node at a time, so
a single console pod cannot mount the volumes of the many Claws it reports on
— it would have to be scheduled onto every node at once. Reading through the
Kubernetes exec API removes the storage coupling entirely: any number of
Claws, in any namespace, on any node.

The console never writes. Only `GET` routes exist, and the commands it runs in
a Claw pod are `find`, `tar`, and `cat`.

## Performance

A refresh is one `find` to index the sessions, then one `tar` carrying every
file that changed since the last refresh. Batching matters: reading files one
at a time turned a cold scan of ~250 files into a 60-second round-trip storm,
while a single streamed tar does it in about 7 seconds. Parsed sessions are
cached by size and mtime, so once warm a refresh is well under a second —
session files are append-only, and most never change again.

`CONSOLE_CACHE_MS` bounds how often a refresh may re-index. Sessions past
`maxSessionsPerAgent` (200, newest first) are skipped and counted rather than
silently dropped.

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | Bind address |
| `CLAW_AGENTS_DIR` | `/home/node/.openclaw/agents` | Where agent state lives inside a Claw pod |
| `CLAW_CONTAINER` | `gateway` | Container in the Claw pod holding that state |
| `AGENT_DATA_DIR` | unset | Setting it switches to local single-directory mode (development); leave unset in the cluster |
| `GATEWAY_URL` | unset | Claw gateway to health-check. Unset reports "disabled" rather than guessing. |
| `EXCLUDED_AGENTS` | unset | Comma-separated agent directories to hide |
| `AGENT_META` | unset | JSON map of `{agent: {emoji, title, desc}}` for display |
| `MEMORY_PATH_PATTERN` | `(?:^\|/)(?:memory\|wiki)/[\w\-./]*\.md` | Regexp matching vault note paths. The default covers all three of OpenClaw's stores; override it for a vault kept elsewhere. |
| `CONSOLE_CACHE_MS` | `2000` | Minimum interval between re-indexes |
| `CONSOLE_STATE_DIR` | unset | Where observed memory writes are persisted. Unset (or unwritable) degrades to an in-memory record that the memory page reports as such. |

## What counts as a memory write

OpenClaw keeps durable notes in three places, and the console watches all of
them:

- `workspace/memory/*.md` — the shared daily and named notes
- `workspace/wiki/main/**` — the memory wiki (concepts, entities, syntheses)
- `<agent>/memory/dreaming/{deep,light}/*.md` — per-agent consolidation

The **All notes** tab lists these as they exist on disk, newest first. It is not
derived from tool calls, and that distinction matters: OpenClaw's consolidation
and wiki synthesis write notes directly, with no agent tool call to observe. On
a real Claw a tool-derived feed found 22 notes where the stores held 422 — it
missed every consolidation run and the entire wiki.

Tool calls are still read, but only for attribution: where one recorded a
write, it supplies the agent and session behind a note. Notes with no tool call
are attributed to the agent whose directory holds them, and notes under
`workspace/` belong to no single agent.

### Observed writes

The **Observed writes** tab answers a narrower question — what changed, and
which lines appeared — and it is the console's own observation rather than
anything OpenClaw recorded. Each note's content is remembered and diffed on
change.

Every entry carries a diff. A note seen for the first time establishes a
baseline silently: there is nothing to diff against, and announcing several
hundred pre-existing notes as writes would be false at the exact moment the feed
needs to be trusted.

That record is persisted to `CONSOLE_STATE_DIR`, which is what makes it useful.
Without it the baseline was re-taken on every redeploy and the page reported
nothing had happened when the console had merely forgotten. With it, a write
made while the console was down still shows its real lines on the next read.

Two limits remain, and the UI states them:

- Detection advances only when someone loads the memory page. The console holds
  no standing permission to read any Claw, so it cannot poll on its own.
- Several writes to one note between two reads are reported as the one diff that
  spans them.

Only notes whose size or mtime moved are actually read, so a refresh of a settled
vault transfers nothing; the full read happens once, when the baseline is set.

## A note on agent backends

Claws run different agent backends, and not all store sessions the way this
console parses. A Codex-backed Claw, for instance, writes
`<agent>/agent/codex-home/sessions/YYYY/MM/DD/rollout-*.jsonl` rather than
`<agent>/sessions/<id>.trajectory.jsonl`. Those agents are still listed, with
zero runs — the agent exists, and saying so is more honest than omitting it.
Adding a reader for other backends is a contained change behind the same
source interface.
