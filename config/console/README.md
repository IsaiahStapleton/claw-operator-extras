# Agent Console manifests

Deploys the Agent Console: one console you log into that shows the agents of
every Claw in namespaces you have access to.

## Install

The console runs in its own namespace, not in a Claw's.

```sh
oc new-project agent-console

# The oauth-proxy needs a session secret.
oc create secret generic agent-console-cookie \
  --from-literal=session_secret="$(head -c 32 /dev/urandom | base64)"

oc apply -k config/console
oc get route agent-console -o jsonpath='{.spec.host}{"\n"}'
```

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
| `CONSOLE_CACHE_MS` | `2000` | Minimum interval between re-indexes |

## A note on agent backends

Claws run different agent backends, and not all store sessions the way this
console parses. A Codex-backed Claw, for instance, writes
`<agent>/agent/codex-home/sessions/YYYY/MM/DD/rollout-*.jsonl` rather than
`<agent>/sessions/<id>.trajectory.jsonl`. Those agents are still listed, with
zero runs — the agent exists, and saying so is more honest than omitting it.
Adding a reader for other backends is a contained change behind the same
source interface.
