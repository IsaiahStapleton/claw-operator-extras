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

One Red Hat styled console you log into that shows the agents of every Claw in
namespaces you have access to: run timeline with filters, per-agent detail with
charts, session replay with live tailing, the handoff topology between agents,
and the memory-vault writes they commit.

It reads a Claw's session files through the Kubernetes exec API rather than by
mounting its volume, because Claw home PVCs are ReadWriteOnce and one pod
cannot mount many of them. Access is enforced by impersonation: the console has
no standing permission to read any Claw, only to act as the logged-in user, so
the API server decides what each person sees.

```sh
make console-run-local CONSOLE_LOCAL_DATA_DIR=/path/to/agents   # local preview
make console-build                                              # build; override CONSOLE_IMG
```

`console-run-local` is a development mode that reads one directory off disk
instead of talking to a cluster. It expects the layout OpenClaw writes:

```
<AGENT_DATA_DIR>/<agent>/sessions/<sessionId>.trajectory.jsonl   # structured events
<AGENT_DATA_DIR>/<agent>/sessions/<sessionId>.jsonl              # plain transcript
```

Inside a Claw pod that is `~/.openclaw/agents`.

To deploy on OpenShift, see **[config/console/README.md](config/console/README.md)**.

A natural next step is for the OpenClaw gateway to serve its own trajectories
over HTTP. It already runs in every Claw pod with the files on local disk, so
an authenticated read API would remove the exec dependency and let the console
talk plain HTTP. The data path sits behind an interface for exactly that
reason — swapping it is a contained change, not a rewrite.

### Design principles

The console is read-only by construction: the server defines only `GET`
handlers, and the only commands it runs inside a Claw pod are `find`, `tar`,
and `cat`.

It also refuses to paper over gaps in its input. An unreadable data directory
produces a danger banner and no figures at all, rather than an empty dashboard
that looks like a healthy fleet. Unparseable JSONL lines, unreadable files, and
events that OpenClaw truncated for exceeding its size limit are each counted
and surfaced in the masthead integrity badge. A run whose prompt was dropped by
that size limit says so, and reports the original event size, instead of
rendering a bare "unavailable".

### API

All routes are `GET`. `/metrics` is Prometheus text format.

All data routes take `namespace` and `claw` query parameters naming the Claw to
report on.

| Route | Returns |
|---|---|
| `/api/scope` | Claws this user may read |
| `/api/agents` | Per-agent status, current step, last-seen, run count |
| `/api/runs` | Run list; filters: `agent`, `outcome`, `q`, `since`, `limit` |
| `/api/runs/{agent}/{sessionId}` | Session replay; `offset`, `limit` |
| `/api/runs/{agent}/{sessionId}/tail` | Events past `after=N`, for live tailing |
| `/api/handoffs` | Inferred and declared agent-to-agent edges |
| `/api/memory` | Writes to the shared memory vault |
| `/api/health` | Gateway health, data integrity, stale threshold |
| `/metrics` | `agent_console_*` gauges |
| `/healthz` | Liveness/readiness (204) |
