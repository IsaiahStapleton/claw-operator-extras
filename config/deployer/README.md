# OpenClaw Deployer

This is a small OpenShift webapp for provisioning `Claw` resources in
OpenShift namespaces the logged-in user can manage.

OpenShift OAuth protects the UI and forwards the username/groups to the
backend. The backend uses the deployer service account token with Kubernetes
impersonation headers, so OpenShift authorizes each request as the logged-in
user. The deployer can only operate where that user already has normal project
RBAC.

For each Claw, the deployer creates or deletes in the selected namespace:

- a provider API key Secret named `openclaw-<name>-<provider>-api-key`
- a `claw.sandbox.redhat.com/v1alpha1` `Claw`
- when a folder is uploaded to seed the filesystem, a ConfigMap named
  `openclaw-<name>-agentfiles` holding the packaged `agentfiles.tgz`

## Seeding the OpenClaw workspace

The form's **Starting files** section maps to `spec.agentFiles` on the Claw.
There is no Config owner toggle in the form; choosing a starting-files source
switches the instance to user-managed config (the operator only honors
`spec.agentFiles` for user-managed Claws):

- **From a Git repository** — a repository URL with an optional ref and
  subpath. The operator clones it in the init container.
- **Upload a folder** — pick a folder in the browser. The backend packages it
  into `agentfiles.tgz`, stores it in the `openclaw-<name>-agentfiles`
  ConfigMap as the impersonated user, and points `spec.agentFiles.configMapRef`
  at it — no manual `tar`/ConfigMap step. Uploads are capped at 1 MiB.

Either way, the source is a directory tree that seeds the Claw's OpenClaw home
(`~/.openclaw`, on its persistent volume), applied once on first boot; edits
made later inside the running Claw are preserved. Layout:

- `workspace-main/` → the agent workspace (`~/.openclaw/workspace/`), e.g.
  `AGENTS.md`, `SOUL.md`, `memory/`, `skills/`
- `openclaw.json` → merged into the Claw's effective config (not copied as a
  workspace file)
- any other top-level folder → copied under `~/.openclaw/` (e.g. `skills/` →
  `~/.openclaw/skills/`)

A correct bundle also wires up sub-agent workspaces, shared skills dirs, plugin
paths, and cron in its `openclaw.json`. See the full layout reference and
examples in [redhat-et/claw-collections](https://github.com/redhat-et/claw-collections).

## Build

The Makefile defaults image builds to `PLATFORM=linux/amd64`.

```sh
make deployer-build
make deployer-push
```

To be explicit:

```sh
make deployer-build PLATFORM=linux/amd64
make deployer-push
```

## Local preview

To preview the deployer UI without building an image or deploying to
OpenShift:

```sh
make deployer-run-local
```

Then open <http://127.0.0.1:18080/>. The app runs with a local preview user
and a dummy Kubernetes API server, so the page renders but status and
provisioning calls will show connection errors.

Useful overrides:

```sh
make deployer-run-local DEPLOYER_LOCAL_ADDR=:18081 DEPLOYER_LOCAL_USER=sallyom
```

## Deploy

```sh
oc new-project openclaw-deployer
oc create secret generic openclaw-deployer-cookie \
  --from-literal=session_secret="$(openssl rand -base64 32 | head -c 32)"

oc apply -k config/deployer
oc rollout status deployment/openclaw-deployer -n openclaw-deployer
oc get route openclaw-deployer -n openclaw-deployer
```

The manifests grant the deployer service account `impersonate` on users and
groups. They do not grant direct permission to create Secrets or Claws. Users
still need normal RBAC in the target namespace. If they can create Secrets and
`Claw` resources there, the card can provision their OpenClaw. If they cannot,
the app shows the Kubernetes authorization error.

By default, the deployer suggests namespace `sallyom-claw` for user `sallyom`.
Users whose login already ends with `-claw` get that exact namespace as the
suggestion. Set `CLAW_NAMESPACE_SUFFIX` on the deployer container to use a
different suffix. The UI keeps the field editable and suggests namespaces from
Claws the user can see.

The deployer binary defaults new Claws to `spec.config.management=user` when a
provision request omits management; `CLAW_CONFIG_MANAGEMENT_DEFAULT` overrides
that fallback. The UI exposes a Config ownership option and defaults it to
user-managed. Proxy-backed providers and add-ons stay managed through this UI
and the Claw CR, while ordinary OpenClaw runtime config edits made through the
UI, CLI, plugins, skills, MCPs, or agents persist on the PVC. Operator-managed
remains available for Claws that should keep reconciling runtime config from the
form and the Claw CR.

Set `DISABLE_USER_CONFIG_MANAGEMENT=true` on the deployer when the operator has
user-managed config disabled. The deployer hides user-managed-only controls,
including the custom `spec.image` OpenClaw image override, and rejects requests
that try to set them.

## Automated deploys (merge to main)

Merging a PR that touches deployer source rolls the running deployer out
automatically.

How it works:

1. The `.github/workflows/deploy.yml` workflow runs on pushes to `main` that
   touch `cmd/deployer/**`, `Containerfile`, `go.mod`, or `go.sum`. It builds
   the image and pushes it to `quay.io/redhat-et/claw-deployer-ui` as both
   `:latest` and `:<git-sha>`. It needs two repo secrets in GitHub Actions:
   `QUAY_USERNAME` and `QUAY_PASSWORD`.
2. The `openclaw-deployer` `ImageStream` (`imagestream.yaml`) tracks
   `quay.io/redhat-et/claw-deployer-ui:latest` with `importPolicy.scheduled:
   true`, so OpenShift re-imports the tag on its own timer (cluster default
   ~15 min). When the digest changes, the ImageStreamTag updates.
3. The Deployment carries an `image.openshift.io/triggers` annotation watching
   that tag. When the tag's digest changes, the OpenShift image-trigger
   controller overwrites the `app` container's `image` field with the resolved
   quay digest (the ImageStream uses `referencePolicy: Source`), which produces
   a new rollout.

The Deployment's `image:` stays a plain pullspec — in `deployment.yaml` it is
`openclaw-deployer:latest`, which kustomize's `images:` transformer rewrites to
`quay.io/redhat-et/claw-deployer-ui:latest` in the applied output. A plain
Deployment cannot reference an ImageStreamTag directly, so the annotation is the
only link to the ImageStream, and the trigger controller rewrites the field to a
`…@sha256:…` digest on each import.

One-time setup so the loop exists in the cluster (run once after the manifests
are applied):

```sh
oc apply -k config/deployer
oc import-image openclaw-deployer:latest -n openclaw-deployer
```

There is up to ~15 min of latency between a merge and the rollout, because the
import is scheduled (polling), not a webhook. To deploy immediately instead of
waiting for the next scheduled import, force the import:

```sh
oc import-image openclaw-deployer:latest -n openclaw-deployer
```

## Manually updating the running image

Useful for testing a one-off image (for example your own
`quay.io/<you>/claw-deployer-ui:latest`) without going through a PR.

Important: while the `image.openshift.io/triggers` annotation is present, the
image-trigger controller keeps forcing the `app` container back to whatever
`openclaw-deployer:latest` resolves to. A plain `oc set image` will be reverted
within seconds unless you first neutralize the trigger.

Option A — disable the trigger, then set your image (true ad-hoc image):

```sh
oc set triggers deployment/openclaw-deployer -n openclaw-deployer --remove-all
oc set image deployment/openclaw-deployer app=quay.io/<you>/claw-deployer-ui:latest -n openclaw-deployer
```

Restore the automated flow when you are done by re-applying the manifests,
which puts the annotation back and resumes tracking the redhat-et image:

```sh
oc apply -k config/deployer
```

Option B — repoint the ImageStream instead (rolls out exactly the way the
automated flow does, through the trigger):

```sh
oc tag quay.io/<you>/claw-deployer-ui:latest openclaw-deployer:latest -n openclaw-deployer
```

Revert to the redhat-et image:

```sh
oc tag quay.io/redhat-et/claw-deployer-ui:latest openclaw-deployer:latest -n openclaw-deployer
```

Either way the committed manifests are unchanged, so the next `oc apply -k
config/deployer` (or the next merge) brings the running deployer back to the
real image.
