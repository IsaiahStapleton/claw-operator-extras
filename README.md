# OpenClaw Operator Extras

This repository contains two standalone apps that sit alongside the
[`claw-operator`](https://github.com/redhat-et/claw-operator). Neither imports
the operator code.

| App | Path | What it does |
|---|---|---|
| Deployer | `cmd/deployer` | Web UI and backend that creates and updates `Claw` resources through the Kubernetes API. Depends only on the `claw.sandbox.redhat.com/v1alpha1` API shape. |
| Agent Console | `cmd/console` | Read-only observability UI for the agents inside a running Claw. Reads trajectory and transcript files from the Claw home volume. |

## Test

```sh
make test
```

## Deployer

```sh
make deployer-run-local     # local preview
make deployer-build         # build the image; override DEPLOYER_IMG
```

## Agent Console

A Red Hat styled console for watching what a Claw's agents are actually doing:
run timeline with filters, per-agent detail with charts, session replay with
live tailing, the handoff topology between agents, and the memory-vault writes
they commit.

```sh
make console-run-local CONSOLE_LOCAL_DATA_DIR=/path/to/agents   # local preview
make console-build                                              # build; override CONSOLE_IMG
```

It expects a directory laid out the way OpenClaw writes one:

```
<AGENT_DATA_DIR>/<agent>/sessions/<sessionId>.trajectory.jsonl   # structured events
<AGENT_DATA_DIR>/<agent>/sessions/<sessionId>.jsonl              # plain transcript
```

Inside a Claw pod that is `~/.openclaw/agents`, which is why the deployment
mounts the Claw home PVC at `/data/agents` with `subPath: .openclaw/agents`.

To deploy on OpenShift, see **[config/console/README.md](config/console/README.md)** —
in particular the ReadWriteOnce constraint, which requires the console to be
co-scheduled onto the same node as the Claw pod.

### Design principles

The console is read-only by construction: the server defines only `GET`
handlers and the data volume is mounted read-only.

It also refuses to paper over gaps in its input. An unreadable data directory
produces a danger banner and no figures at all, rather than an empty dashboard
that looks like a healthy fleet. Unparseable JSONL lines, unreadable files, and
events that OpenClaw truncated for exceeding its size limit are each counted
and surfaced in the masthead integrity badge. A run whose prompt was dropped by
that size limit says so, and reports the original event size, instead of
rendering a bare "unavailable".

### API

All routes are `GET`. `/metrics` is Prometheus text format.

| Route | Returns |
|---|---|
| `/api/agents` | Per-agent status, current step, last-seen, run count |
| `/api/runs` | Run list; filters: `agent`, `outcome`, `q`, `since`, `limit` |
| `/api/runs/{agent}/{sessionId}` | Session replay; `offset`, `limit` |
| `/api/runs/{agent}/{sessionId}/tail` | Events past `after=N`, for live tailing |
| `/api/handoffs` | Inferred and declared agent-to-agent edges |
| `/api/memory` | Writes to the shared memory vault |
| `/api/health` | Gateway health, data integrity, stale threshold |
| `/metrics` | `agent_console_*` gauges |
| `/healthz` | Liveness/readiness (204) |
