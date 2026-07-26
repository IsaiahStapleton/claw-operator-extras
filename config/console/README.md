# Agent Console manifests

Deploys the Agent Console (`cmd/console`) behind an OpenShift OAuth proxy, with
the Claw home PVC mounted read-only.

## Prerequisites

A Claw instance already running in the target namespace, since the console
reads its home volume. The defaults here target a Claw named `podling` with the
PVC `podling-home-pvc`.

## Install

```sh
oc new-project isaiah-claw    # or your existing Claw namespace

# The oauth-proxy needs a session secret.
oc create secret generic agent-console-cookie \
  --from-literal=session_secret="$(head -c 32 /dev/urandom | base64)"

oc apply -k config/console
```

Then open the route:

```sh
oc get route agent-console -o jsonpath='{.spec.host}'
```

## The ReadWriteOnce constraint

**This is the one thing that will bite you.** Claw home PVCs are provisioned
`ReadWriteOnce` on EBS (`gp3`). An EBS volume attaches to exactly one node at a
time, so a console pod scheduled onto a different node than the Claw pod will
sit `Pending` forever with a multi-attach error.

Multiple pods *may* share an RWO volume when they are on the **same node**, so
`deployment.yaml` pins the console to the Claw pod with a required
`podAffinity` on `topologyKey: kubernetes.io/hostname`. Consequences:

- If the Claw pod moves to another node, the console must be rescheduled to
  follow it. `kubectl rollout restart deploy/agent-console` is enough.
- If the Claw pod is scaled to zero, the affinity has nothing to match and the
  console stays `Pending`. That is expected — there is no data to read.

If the cluster later offers an RWX storage class (EFS, ODF), drop the affinity
block and mount the volume directly.

## The UID constraint

The second thing that will bite you. OpenClaw writes session files as mode
`0600` inside a `0700` `sessions/` directory, owned by the UID the Claw runs
as. That restriction is deliberate and audited, so **do not loosen it** to make
the console work. Group permissions are not enough either — the mode bits give
the group nothing.

The console therefore has to run as the *same UID* as the Claw pod. Both
Deployments omit `runAsUser`, so `restricted-v2` assigns each the first UID in
the namespace's `openshift.io/sa.scc.uid-range` annotation and they match
automatically. Check the range with:

```sh
oc get ns <namespace> -o jsonpath='{.metadata.annotations.openshift\.io/sa\.scc\.uid-range}{"\n"}'
```

If the Claw is pinned to an explicit `runAsUser`, pin the console to the same
value. Confirm they agree:

```sh
oc exec deploy/agent-console -c app -- id
oc exec deploy/podling -c gateway -- id
```

A mismatch does not crash anything — it shows up as a large **unreadable files**
count in the console's masthead integrity badge, with few or no agents listed.
That is the honesty machinery working as intended, but it means you are seeing
nothing rather than everything.

## Pointing at a different Claw

The affinity selector, PVC name, and agent metadata all name `podling`. For a
different instance, patch the three of them:

```sh
oc patch deploy agent-console --type=json -p='[
  {"op":"replace","path":"/spec/template/spec/affinity/podAffinity/requiredDuringSchedulingIgnoredDuringExecution/0/labelSelector/matchLabels/claw.sandbox.redhat.com~1instance","value":"my-claw"},
  {"op":"replace","path":"/spec/template/spec/volumes/0/persistentVolumeClaim/claimName","value":"my-claw-home-pvc"}
]'
```

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `AGENT_DATA_DIR` | `/data/agents` | Root scanned for `<agent>/sessions/*.jsonl` |
| `LISTEN_ADDR` | `:8080` | Bind address |
| `GATEWAY_URL` | unset | Claw gateway to health-check. Unset reports "disabled" rather than guessing. |
| `EXCLUDED_AGENTS` | unset | Comma-separated agent directories to hide |
| `AGENT_META` | unset | JSON map of `{agent: {emoji, title, desc}}` for display |
| `CONSOLE_CACHE_MS` | `2000` | Directory-scan cache window |

## Security posture

- The server defines only `GET` routes; there is no mutating handler.
- The data volume is mounted `readOnly: true` and the container runs with a
  read-only root filesystem as an arbitrary non-root UID (restricted-v2).
- All traffic goes through the oauth-proxy sidecar; only `/healthz` skips auth
  so the kubelet can probe it. `/metrics` sits behind auth — scrape it via the
  in-cluster service on port 8080 rather than the route.
