const initialNamespace = localStorage.getItem("openclaw-deployer.namespace") || "";
const initialSelectedName = localStorage.getItem("openclaw-deployer.name") || "instance";

function integrationStorageKey(namespace, name) {
  if (!namespace || !name) {
    return "";
  }
  return `openclaw-deployer.integrations.${namespace}.${name}`;
}

function storedIntegrations(namespace, name) {
  const key = integrationStorageKey(namespace, name);
  if (!key) {
    return [];
  }
  try {
    const parsed = JSON.parse(localStorage.getItem(key) || "[]");
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function modelProviderStorageKey(namespace, name) {
  if (!namespace || !name) {
    return "";
  }
  return `openclaw-deployer.modelProviders.${namespace}.${name}`;
}

function storedModelProviders(namespace, name) {
  const key = modelProviderStorageKey(namespace, name);
  if (!key) {
    return [];
  }
  try {
    const parsed = JSON.parse(localStorage.getItem(key) || "[]");
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

const state = {
  namespace: initialNamespace,
  provider: localStorage.getItem("openclaw-deployer.provider") || "openrouter",
  selectedName: initialSelectedName,
  model: localStorage.getItem("openclaw-deployer.model") || "",
  modelProviders: storedModelProviders(initialNamespace, initialSelectedName),
  removedModelProviders: [],
  openClawImage: "",
  version: "",
  secretName: "",
  secretKey: "",
  gcpProject: localStorage.getItem("openclaw-deployer.gcpProject") || "",
  gcpLocation: localStorage.getItem("openclaw-deployer.gcpLocation") || "",
  management: localStorage.getItem("openclaw-deployer.management") || "user",
  filesystemSource: localStorage.getItem("openclaw-deployer.filesystemSource") || "",
  gitURL: localStorage.getItem("openclaw-deployer.gitURL") || "",
  gitRef: localStorage.getItem("openclaw-deployer.gitRef") || "",
  gitPath: localStorage.getItem("openclaw-deployer.gitPath") || "",
  gitSecretName: "",
  integrations: storedIntegrations(initialNamespace, initialSelectedName),
  integrationScope: integrationStorageKey(initialNamespace, initialSelectedName),
  integrationsDirty: false,
  removedIntegrations: [],
  theme: localStorage.getItem("openclaw-deployer.theme") === "dark" ? "dark" : "light",
  claws: [],
  namespaceSuggestions: [],
  currentSecretNames: [],
  currentCredentialRefs: [],
  exists: false,
  ready: false,
  userManagedEnabled: false,
  submitted: false,
  copied: "",
};

const providerLabels = {
  openrouter: "OpenRouter",
  openai: "OpenAI",
  google: "Google Gemini",
  "google-vertex": "Google Vertex AI (Gemini)",
  anthropic: "Anthropic",
  "anthropic-vertex": "Google Vertex AI (Claude)",
  xai: "xAI",
};

const modelDefaults = {
  anthropic: "anthropic/claude-sonnet-4-6",
  "anthropic-vertex": "anthropic-vertex/claude-sonnet-4-6",
  google: "google/gemini-3.1-pro-preview",
  "google-vertex": "google/gemini-3.1-pro-preview",
  openai: "openai/gpt-5.5",
  openrouter: "openrouter/anthropic/claude-sonnet-4-6",
  xai: "xai/grok-4.3",
};

if (Object.values(modelDefaults).includes(state.model)) {
  state.model = "";
  localStorage.removeItem("openclaw-deployer.model");
}

const modelOptions = {
  anthropic: ["anthropic/claude-sonnet-4-6", "anthropic/claude-haiku-4-5"],
  "anthropic-vertex": ["anthropic-vertex/claude-sonnet-4-6", "anthropic-vertex/claude-opus-4-8", "anthropic-vertex/claude-opus-4-7"],
  google: ["google/gemini-3.1-pro-preview", "google/gemini-3.5-flash", "google/gemini-3.1-flash-lite"],
  "google-vertex": ["google/gemini-3.1-pro-preview", "google/gemini-3.5-flash", "google/gemini-3.1-flash-lite"],
  openai: ["openai/gpt-5.5", "openai/gpt-5.4", "openai/gpt-5.4-mini"],
  openrouter: ["openrouter/anthropic/claude-sonnet-4-6", "openrouter/openai/gpt-5.5", "openrouter/google/gemini-3.5-flash", "openrouter/auto"],
  xai: ["xai/grok-4.3", "xai/grok-4.20"],
};

const googleVertexProviders = new Set(["anthropic-vertex", "google-vertex"]);

const integrationLabels = {
  "channel-telegram": "Telegram channel",
  "channel-discord": "Discord channel",
  "channel-slack": "Slack channel",
  "channel-whatsapp": "WhatsApp channel",
  "github-pat": "GitHub PAT",
  "websearch-brave": "Brave web search",
  "websearch-tavily": "Tavily web search",
  "websearch-duckduckgo": "DuckDuckGo web search",
  "websearch-gemini": "Gemini web search",
  "auth-password": "Gateway password auth",
  "custom-credential": "Custom credential",
};

const defaultGCPLocations = {
  "anthropic-vertex": "us-east5",
  "google-vertex": "us-central1",
};

const els = {
  themeToggle: document.getElementById("theme-toggle"),
  avatar: document.getElementById("avatar"),
  user: document.getElementById("user"),
  namespace: document.getElementById("namespace"),
  namespaceOptions: document.getElementById("namespace-options"),
  clawName: document.getElementById("clawName"),
  clawNameOptions: document.getElementById("claw-name-options"),
  provider: document.getElementById("provider"),
  model: document.getElementById("model"),
  modelOptions: document.getElementById("model-options"),
  modelProviderAdd: document.getElementById("model-provider-add"),
  modelProviderList: document.getElementById("model-provider-list"),
  defaultModel: document.getElementById("default-model"),
  openClawImage: document.getElementById("openClawImage"),
  openClawImageField: document.getElementById("openclaw-image-field"),
  version: document.getElementById("version"),
  doctorFix: document.getElementById("doctorFix"),
  doctorFixHint: document.getElementById("doctor-fix-hint"),
  dreamingEnabled: document.getElementById("dreamingEnabled"),
  wikiEnabled: document.getElementById("wikiEnabled"),
  managementHint: document.getElementById("management-hint"),
  managementHelp: document.getElementById("management-help"),
  vertexBox: document.getElementById("vertex-box"),
  gcpProject: document.getElementById("gcpProject"),
  gcpLocation: document.getElementById("gcpLocation"),
  credentialLabel: document.getElementById("credential-label"),
  vertexGuide: document.getElementById("vertex-guide"),
  vertexHelp: document.getElementById("vertex-help"),
  apiKey: document.getElementById("apiKey"),
  gcpCredentials: document.getElementById("gcpCredentials"),
  secretName: document.getElementById("secretName"),
  secretKey: document.getElementById("secretKey"),
  secretNamePreview: document.getElementById("secret-name-preview"),
  advancedToggle: document.getElementById("advanced-toggle"),
  advancedCaret: document.getElementById("advanced-caret"),
  advancedBody: document.getElementById("advanced-body"),
  filesystemSource: document.getElementById("filesystemSource"),
  management: document.getElementById("management"),
  filesystemSourceHint: document.getElementById("filesystem-source-hint"),
  workspaceSourceHelp: document.getElementById("workspace-source-help"),
  gitBox: document.getElementById("git-box"),
  gitURL: document.getElementById("gitURL"),
  gitRef: document.getElementById("gitRef"),
  gitPath: document.getElementById("gitPath"),
  gitSecretName: document.getElementById("gitSecretName"),
  gitUsername: document.getElementById("gitUsername"),
  gitPassword: document.getElementById("gitPassword"),
  detailsToggle: document.getElementById("details-toggle"),
  detailsCaret: document.getElementById("details-caret"),
  detailsBody: document.getElementById("details-body"),
  providerToggle: document.getElementById("provider-toggle"),
  providerCaret: document.getElementById("provider-caret"),
  providerBody: document.getElementById("provider-body"),
  integrationType: document.getElementById("integrationType"),
  integrationToggle: document.getElementById("integration-toggle"),
  integrationCaret: document.getElementById("integration-caret"),
  integrationBody: document.getElementById("integration-body"),
  integrationHelp: document.getElementById("integration-help"),
  integrationName: document.getElementById("integrationName"),
  integrationNameField: document.getElementById("integration-name-field"),
  integrationSecretFields: document.getElementById("integration-secret-fields"),
  integrationSecretLabel: document.getElementById("integration-secret-label"),
  integrationSecretValue: document.getElementById("integrationSecretValue"),
  integrationSecretName: document.getElementById("integrationSecretName"),
  integrationSecretKey: document.getElementById("integrationSecretKey"),
  integrationGithubFields: document.getElementById("integration-github-fields"),
  integrationExposeEnv: document.getElementById("integrationExposeEnv"),
  integrationSlackFields: document.getElementById("integration-slack-fields"),
  integrationAppSecretValue: document.getElementById("integrationAppSecretValue"),
  integrationAppSecretName: document.getElementById("integrationAppSecretName"),
  integrationAppSecretKey: document.getElementById("integrationAppSecretKey"),
  integrationCustomFields: document.getElementById("integration-custom-fields"),
  integrationCredentialType: document.getElementById("integrationCredentialType"),
  integrationDomain: document.getElementById("integrationDomain"),
  integrationProvider: document.getElementById("integrationProvider"),
  integrationHeader: document.getElementById("integrationHeader"),
  integrationValuePrefix: document.getElementById("integrationValuePrefix"),
  integrationPathPrefix: document.getElementById("integrationPathPrefix"),
  integrationTypedChannelConfigField: document.getElementById("integration-typed-channel-config-field"),
  integrationDmPolicy: document.getElementById("integrationDmPolicy"),
  integrationAllowFrom: document.getElementById("integrationAllowFrom"),
  integrationDmPolicyHint: document.getElementById("integration-dm-policy-hint"),
  integrationAllowFromHint: document.getElementById("integration-allow-from-hint"),
  integrationChannelConfigField: document.getElementById("integration-channel-config-field"),
  integrationChannelConfig: document.getElementById("integrationChannelConfig"),
  integrationAdd: document.getElementById("integration-add"),
  integrationList: document.getElementById("integration-list"),
  uploadBox: document.getElementById("upload-box"),
  agentFiles: document.getElementById("agentFiles"),
  uploadName: document.getElementById("upload-name"),
  provision: document.getElementById("provision"),
  reset: document.getElementById("reset"),
  previewOpen: document.getElementById("preview-open"),
  alert: document.getElementById("alert"),
  reviewList: document.getElementById("review-list"),
  instancesCount: document.getElementById("instances-count"),
  instancesBody: document.getElementById("instances-body"),
  previewOverlay: document.getElementById("preview-overlay"),
  previewDialog: document.getElementById("preview-dialog"),
  previewYaml: document.getElementById("preview-yaml"),
  copyYaml: document.getElementById("copy-yaml"),
  previewClose: document.getElementById("preview-close"),
  previewClose2: document.getElementById("preview-close-2"),
};

els.namespace.value = state.namespace;
els.clawName.value = state.selectedName;
els.provider.value = state.provider;
els.model.value = state.model;
els.openClawImage.value = state.openClawImage;
els.version.value = state.version;
els.secretName.value = state.secretName;
els.secretKey.value = state.secretKey;
els.gcpProject.value = state.gcpProject;
els.gcpLocation.value = state.gcpLocation || defaultGCPLocations[state.provider] || "";
els.management.value = state.management;
els.filesystemSource.value = state.filesystemSource;
els.gitURL.value = state.gitURL;
els.gitRef.value = state.gitRef;
els.gitPath.value = state.gitPath;
els.gitSecretName.value = state.gitSecretName;

applyTheme(state.theme);
renderModelOptions();
renderModelProviders();
renderCredentialFields();
renderFilesystemSource();
renderIntegrationFields();
renderIntegrations();
setIntegrationOpen(state.integrations.length > 0);
renderReview();

// ---------- helpers ----------
function isGoogleVertex() {
  return googleVertexProviders.has(els.provider.value);
}

function selectedManagement() {
  if (!state.userManagedEnabled) return "operator";
  return els.management.value === "operator" ? "operator" : "user";
}

function effectiveModel() {
  return els.model.value.trim() || modelDefaults[els.provider.value] || "";
}

function shouldConfigureAgent() {
  return !state.exists || els.model.value.trim() !== "";
}

function credentialNameForProvider(provider) {
  if (provider === "anthropic-vertex" || provider === "google-vertex") {
    return provider;
  }
  return provider;
}

function credentialProviderForProvider(provider) {
  if (provider === "anthropic-vertex") return "anthropic";
  if (provider === "google-vertex") return "google";
  return provider;
}

function defaultSecretName() {
  return defaultSecretNameForProvider(els.provider.value);
}

function defaultSecretNameForProvider(provider) {
  const name = els.clawName.value.trim() || "instance";
  const credentialName = credentialNameForProvider(provider);
  if (googleVertexProviders.has(provider)) {
    return `openclaw-${name}-${credentialName}-gcp`;
  }
  return `openclaw-${name}-${credentialName}-api-key`;
}

function effectiveSecretName() {
  return els.secretName.value.trim() || defaultSecretName();
}

function expectedSecretKey() {
  return expectedSecretKeyForProvider(els.provider.value);
}

function expectedSecretKeyForProvider(provider) {
  return googleVertexProviders.has(provider) ? "sa-key.json" : "api-key";
}

function effectiveSecretKey() {
  return els.secretKey.value.trim() || expectedSecretKey();
}

function selectedProviderCredentialSupplied() {
  const value = isGoogleVertex() ? els.gcpCredentials.value : els.apiKey.value;
  return els.secretName.value.trim() !== "" || value.trim() !== "";
}

function isModelProviderCredentialRef(ref) {
  return Boolean(providerLabels[ref.credential] || providerLabels[ref.provider]);
}

function credentialRefMatchesProvider(ref, provider) {
  const credentialName = credentialNameForProvider(provider);
  return ref.credential === credentialName || (!ref.credential && ref.provider === credentialProviderForProvider(provider));
}

function selectedProviderCredentialRef() {
  const provider = els.provider.value;
  return {
    credential: credentialNameForProvider(provider),
    provider: credentialProviderForProvider(provider),
    type: googleVertexProviders.has(provider) ? "gcp" : "",
    name: effectiveSecretName(),
    key: effectiveSecretKey(),
    action: els.secretName.value.trim() ? "existing" : "create",
  };
}

function modelProviderCredentialRefsForPreview(includeEmptyNew = true) {
  const replacingSelected = selectedProviderCredentialSupplied() || (includeEmptyNew && !state.exists);
  const refs = [];
  if (state.exists) {
    for (const ref of state.currentCredentialRefs) {
      if (!isModelProviderCredentialRef(ref)) continue;
      if (replacingSelected && credentialRefMatchesProvider(ref, els.provider.value)) continue;
      refs.push(ref);
    }
  }
  if (replacingSelected) {
    refs.push(selectedProviderCredentialRef());
  }
  return refs;
}

function applyTheme(theme) {
  state.theme = theme === "dark" ? "dark" : "light";
  document.documentElement.setAttribute("data-theme", state.theme);
  localStorage.setItem("openclaw-deployer.theme", state.theme);
  const toDark = state.theme === "dark";
  els.themeToggle.textContent = toDark ? "☀" : "☾";
  const aria = toDark ? "Switch to light mode" : "Switch to dark mode";
  els.themeToggle.setAttribute("aria-label", aria);
  els.themeToggle.setAttribute("title", aria);
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(payload.error || `Request failed: ${response.status}`);
  }
  return payload;
}

async function init() {
  try {
    const me = await api("/api/me");
    if (me.defaultNamespace && !state.namespace) {
      state.namespace = me.defaultNamespace;
      els.namespace.value = state.namespace;
      localStorage.setItem("openclaw-deployer.namespace", state.namespace);
    }
    if (me.user) {
      els.user.textContent = me.user;
      els.avatar.textContent = me.user.slice(0, 2).toUpperCase();
    }
    state.userManagedEnabled = Boolean(me.userManagedEnabled);
    els.openClawImageField.hidden = !state.userManagedEnabled;
    els.management.disabled = !state.userManagedEnabled;
    els.managementHelp.hidden = !state.userManagedEnabled;
    els.managementHint.textContent = state.userManagedEnabled
      ? "User-managed is the usual choice; operator-managed keeps runtime config in the Claw CR."
      : "User-managed config is disabled by this deployer; new and updated Claws use operator-managed runtime config.";
    if (!state.userManagedEnabled) {
      els.openClawImage.value = "";
      state.openClawImage = "";
      state.management = "operator";
      els.management.value = state.management;
    }
    if (!localStorage.getItem("openclaw-deployer.management") && me.defaultManagement) {
      state.management = me.defaultManagement;
      els.management.value = state.management;
    }
  } catch (error) {
    renderAlert({ kind: "danger", title: "Couldn't load your session", body: error.message });
    return;
  }
  await loadNamespaceSuggestions();
  await refresh();
}

// One-time read so the namespace field can suggest every namespace the user can
// see, without making each refresh list Claws cluster-wide.
async function loadNamespaceSuggestions() {
  try {
    const all = await api("/api/namespaces");
    state.namespaceSuggestions = [...new Set((all.namespaces || []).filter(Boolean))].sort();
    renderNamespaceOptions([]);
  } catch {
    // Best effort: the namespace field stays editable without suggestions.
  }
}

async function refresh() {
  const previousNamespace = state.namespace;
  state.namespace = els.namespace.value.trim();
  state.selectedName = els.clawName.value.trim() || "instance";
  state.provider = els.provider.value;
  state.model = els.model.value.trim();
  state.openClawImage = state.userManagedEnabled ? els.openClawImage.value.trim() : "";
  state.secretName = els.secretName.value.trim();
  state.secretKey = els.secretKey.value.trim();
  state.gcpProject = els.gcpProject.value.trim();
  state.gcpLocation = els.gcpLocation.value.trim();
  state.management = selectedManagement();
  state.gitSecretName = els.gitSecretName.value.trim();
  localStorage.setItem("openclaw-deployer.namespace", state.namespace);
  localStorage.setItem("openclaw-deployer.name", state.selectedName);
  localStorage.setItem("openclaw-deployer.provider", state.provider);
  localStorage.setItem("openclaw-deployer.model", state.model);
  localStorage.setItem("openclaw-deployer.gcpProject", state.gcpProject);
  localStorage.setItem("openclaw-deployer.gcpLocation", state.gcpLocation);
  localStorage.setItem("openclaw-deployer.management", state.management);

  setStatus("Checking status…");
  try {
    const current = await api("/api/claws");
    renderList(current.claws || [], { namespaceChanged: state.namespace !== previousNamespace });
  } catch (error) {
    renderList([], { namespaceChanged: state.namespace !== previousNamespace });
    renderAlert({ kind: "danger", title: "Couldn't list instances", body: error.message });
  }
}

function renderList(claws, opts = {}) {
  state.claws = claws;
  renderNamespaceOptions(claws);
  const namespaceClaws = state.namespace
    ? claws.filter((claw) => (claw.namespace || state.namespace) === state.namespace)
    : claws;
  renderClawNameOptions(namespaceClaws);

  if (opts.namespaceChanged && state.namespace) {
    const names = namespaceClaws.map((claw) => claw.name).filter(Boolean).sort();
    if (names.length > 0 && !names.includes(state.selectedName)) {
      state.selectedName = names[0];
      els.clawName.value = state.selectedName;
      localStorage.setItem("openclaw-deployer.name", state.selectedName);
    } else if (names.length === 0 && state.selectedName !== "instance") {
      state.selectedName = "instance";
      els.clawName.value = state.selectedName;
      localStorage.setItem("openclaw-deployer.name", state.selectedName);
    }
  }
  loadIntegrationsForSelection();

  let selected = null;
  if (state.namespace) {
    selected = namespaceClaws.find((claw) => claw.name === state.selectedName) || null;
  }
  state.exists = Boolean(selected);
  state.ready = Boolean(selected && selected.ready);
  if (selected) {
    state.management = state.userManagedEnabled ? (selected.management || "operator") : "operator";
    els.management.value = state.management;
    state.currentSecretNames = selected.secretNames || [];
    state.currentCredentialRefs = selected.credentialRefs || [];
    if (!state.integrationsDirty) {
      state.integrations = selected.integrations || [];
      state.modelProviders = selected.modelProviders || [];
      state.removedModelProviders = [];
      renderIntegrations();
      renderModelProviders();
    }
    if (selected.model) {
      els.model.value = selected.model;
      state.model = selected.model;
      localStorage.setItem("openclaw-deployer.model", selected.model);
    } else if (!els.model.matches(":focus")) {
      els.model.value = "";
      state.model = "";
      localStorage.removeItem("openclaw-deployer.model");
    }
    if (state.userManagedEnabled && selected.image) {
      els.openClawImage.value = selected.image;
      state.openClawImage = selected.image;
    } else if (!els.openClawImage.matches(":focus")) {
      els.openClawImage.value = "";
      state.openClawImage = "";
    }
    if (selected.version) {
      els.version.value = selected.version;
      state.version = selected.version;
    } else if (!els.version.matches(":focus")) {
      els.version.value = "";
      state.version = "";
    }
    els.doctorFix.checked = Boolean(selected.doctorFix);
    els.doctorFix.disabled = Boolean(selected.doctorFix);
    els.doctorFixHint.textContent = selected.doctorFix
      ? "Doctor migration requested. The operator runs it once for each image reference."
      : "Use this when updating to a new OpenClaw version, or when the logs say a doctor migration is required.";
    els.dreamingEnabled.checked = Boolean(selected.dreamingEnabled);
    els.wikiEnabled.checked = Boolean(selected.wikiEnabled);
  } else {
    state.currentSecretNames = [];
    state.currentCredentialRefs = [];
    if (!state.integrationsDirty && state.integrations.length > 0) {
      clearIntegrationsForSelection();
    }
    if (!els.openClawImage.matches(":focus")) {
      els.openClawImage.value = "";
      state.openClawImage = "";
    }
    if (!els.version.matches(":focus")) {
      els.version.value = "";
      state.version = "";
    }
    els.doctorFix.checked = false;
    els.doctorFix.disabled = false;
    els.doctorFixHint.textContent = "Use this when updating to a new OpenClaw version, or when the logs say a doctor migration is required.";
    els.dreamingEnabled.checked = false;
    els.wikiEnabled.checked = false;
  }

  els.provision.textContent = state.exists ? "Save changes" : "Create OpenClaw";
  renderClaws(claws);
  renderReview();

  if (!state.namespace) {
    renderAlert({ kind: "idle", title: "Let's get started", body: "Pick the project where your OpenClaw should run — everything else follows from there." });
    return;
  }
  if (!state.exists) {
    renderAlert({ kind: "idle", title: "Ready to create", body: `${state.selectedName} doesn't exist yet in ${state.namespace}. Fill in steps 1–3, then click Create OpenClaw.` });
    return;
  }
  if (selected.ready) {
    renderAlert({
      kind: "success",
      title: `${selected.name} is ready`,
      body: `Your OpenClaw is up and running in ${state.namespace}. Open the Control UI to start using it.`,
      link: isSafeHref(selected.gatewayURL) ? selected.gatewayURL : "",
    });
    return;
  }
  if (statusKind(selected) === "failed") {
    renderAlert({ kind: "danger", title: `${selected.name} failed to deploy`, body: selected.message || selected.reason || "The Claw reported a failure." });
    return;
  }
  renderAlert({ kind: "info", title: `Deploying ${selected.name}`, body: selected.message || selected.reason || `${selected.name} is provisioning.`, spin: true });
}

function renderNamespaceOptions(claws) {
  const namespaces = [
    ...new Set([...state.namespaceSuggestions, ...claws.map((claw) => claw.namespace).filter(Boolean)]),
  ].sort();
  els.namespaceOptions.innerHTML = "";
  for (const namespace of namespaces) {
    const option = document.createElement("option");
    option.value = namespace;
    els.namespaceOptions.appendChild(option);
  }
}

function renderClawNameOptions(claws) {
  const names = [...new Set(claws.map((claw) => claw.name).filter(Boolean))].sort();
  els.clawNameOptions.innerHTML = "";
  for (const name of names) {
    const option = document.createElement("option");
    option.value = name;
    els.clawNameOptions.appendChild(option);
  }
}

// The backend exposes `ready` plus a free-form condition reason/message. Map a
// not-ready Claw to "failed" only on clear failure signals, otherwise treat it
// as still deploying.
const failurePattern = /fail|error|backoff|crash|invalid|denied|forbidden|unauthor|not ?found|missing|quota|exceeded|insufficient|timeout/i;

function statusKind(claw) {
  if (claw.idle) {
    return "idle";
  }
  if (claw.ready) {
    return "ready";
  }
  if (failurePattern.test(`${claw.reason || ""} ${claw.message || ""}`)) {
    return "failed";
  }
  return "deploying";
}

const statusMeta = {
  ready: { label: "Ready", cls: "status-label--ready" },
  idle: { label: "Idle", cls: "status-label--idle" },
  deploying: { label: "Deploying", cls: "status-label--deploying" },
  failed: { label: "Failed", cls: "status-label--failed" },
};

function statusIcon(kind) {
  if (kind === "idle") {
    return '<svg width="12" height="12" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="7" fill="currentColor" opacity=".16"/><path d="M6 5v6M10 5v6" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>';
  }
  if (kind === "ready") {
    return '<svg width="12" height="12" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="7" fill="var(--success-border)"/><path d="M5 8.2l2 2 4-4.4" stroke="var(--surface)" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>';
  }
  if (kind === "failed") {
    return '<svg width="12" height="12" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="7" fill="var(--danger-border)"/><path d="M8 4.3v4.4" stroke="var(--surface)" stroke-width="1.8" stroke-linecap="round"/><circle cx="8" cy="11.3" r="1" fill="var(--surface)"/></svg>';
  }
  return '<span class="spinner"></span>';
}

function renderClaws(claws) {
  els.instancesCount.textContent = String(claws.length);
  els.instancesBody.innerHTML = "";

  if (claws.length === 0) {
    const empty = document.createElement("div");
    empty.className = "empty";
    empty.innerHTML =
      '<div class="empty__icon"><svg width="22" height="22" viewBox="0 0 24 24" fill="none"><rect x="4" y="6" width="16" height="12" rx="2" stroke="var(--text-muted)" stroke-width="1.5"/><path d="M4 10h16" stroke="var(--text-muted)" stroke-width="1.5"/></svg></div>' +
      "<h3>No OpenClaws yet</h3>" +
      `<p>${state.namespace ? "Fill in the form above to create your first one." : "Pick a project to see its OpenClaws."}</p>`;
    els.instancesBody.appendChild(empty);
    return;
  }

  const scroll = document.createElement("div");
  scroll.className = "table-scroll";
  const table = document.createElement("div");
  table.className = "table";
  table.innerHTML =
    '<div class="table__col-head">OpenClaw</div>' +
    '<div class="table__col-head">Provider</div>' +
    '<div class="table__col-head">Status</div>' +
    '<div class="table__col-head right">Actions</div>';

  for (const claw of claws) {
    const namespace = claw.namespace || state.namespace;
    const isSelected = namespace === state.namespace && claw.name === state.selectedName;
    const kind = statusKind(claw);
    const meta = statusMeta[kind];

    const nameCell = document.createElement("div");
    nameCell.className = `table__cell name-cell${isSelected ? " selected" : ""}`;
    const nameBtn = document.createElement("button");
    nameBtn.type = "button";
    nameBtn.className = "instance-name";
    nameBtn.textContent = claw.name;
    nameBtn.addEventListener("click", () => {
      state.namespace = namespace;
      state.selectedName = claw.name;
      els.namespace.value = namespace;
      els.clawName.value = claw.name;
      localStorage.setItem("openclaw-deployer.namespace", namespace);
      localStorage.setItem("openclaw-deployer.name", claw.name);
      renderList(state.claws);
    });
    const nsDiv = document.createElement("div");
    nsDiv.className = "instance-ns";
    nsDiv.textContent = namespace;
    nameCell.append(nameBtn, nsDiv);

    const providerCell = document.createElement("div");
    providerCell.className = "table__cell provider-cell";
    providerCell.textContent = providerLabels[claw.provider] || claw.provider || "—";

    const statusCell = document.createElement("div");
    statusCell.className = "table__cell";
    const label = document.createElement("span");
    label.className = `status-label ${meta.cls}`;
    label.innerHTML = `${statusIcon(kind)}<span>${meta.label}</span>`;
    statusCell.appendChild(label);
    const reasonText = kind === "idle"
      ? "Scaled to zero; data and configuration are preserved."
      : claw.message || claw.reason || "";
    if (reasonText && kind !== "ready") {
      const reason = document.createElement("div");
      reason.className = "status-reason";
      reason.textContent = reasonText;
      statusCell.appendChild(reason);
    }

    const actionsCell = document.createElement("div");
    actionsCell.className = "table__cell actions-cell";
    const actions = document.createElement("div");
    actions.className = "row-actions";
    if (!claw.idle && isSafeHref(claw.gatewayURL)) {
      const link = document.createElement("a");
      link.className = "btn btn--sm";
      link.href = claw.gatewayURL;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      link.textContent = "Control UI";
      actions.appendChild(link);
    }
    const idle = document.createElement("button");
    idle.type = "button";
    idle.className = "btn btn--sm claw-action";
    idle.textContent = claw.idle ? "Unidle" : "Idle";
    idle.addEventListener("click", (event) => {
      event.stopPropagation();
      setClawIdle(namespace, claw.name, !claw.idle);
    });
    const restart = document.createElement("button");
    restart.type = "button";
    restart.className = "btn btn--sm claw-action";
    restart.textContent = "Restart";
    restart.addEventListener("click", (event) => {
      event.stopPropagation();
      restartClaw(namespace, claw.name);
    });
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "btn btn--sm btn--danger claw-action";
    remove.textContent = "Delete";
    remove.addEventListener("click", (event) => {
      event.stopPropagation();
      deleteClaw(namespace, claw.name);
    });
    actions.append(idle, restart, remove);
    actionsCell.appendChild(actions);

    table.append(nameCell, providerCell, statusCell, actionsCell);
  }

  scroll.appendChild(table);
  els.instancesBody.appendChild(scroll);
}

function renderModelOptions() {
  els.modelOptions.innerHTML = "";
  for (const model of modelOptions[els.provider.value] || []) {
    const option = document.createElement("option");
    option.value = model;
    els.modelOptions.appendChild(option);
  }
  els.defaultModel.textContent = modelDefaults[els.provider.value] || "—";
}

function buildModelProviderFromForm() {
  const vertex = isGoogleVertex();
  const provider = els.provider.value;
  const model = els.model.value.trim() || modelDefaults[provider] || "";
  const apiKey = (vertex ? els.gcpCredentials.value : els.apiKey.value).trim();
  const secretName = els.secretName.value.trim();
  if (!apiKey && !secretName) {
    throw new Error(vertex ? "Add a service account JSON or Secret name before adding this provider." : "Add an API key or Secret name before adding this provider.");
  }
  if (vertex && apiKey && !isSupportedGCPKey(apiKey)) {
    throw new Error('This does not look like a supported GCP key.');
  }
  return {
    provider,
    model,
    apiKey,
    secretName,
    secretKey: els.secretKey.value.trim(),
    gcpProject: els.gcpProject.value.trim(),
    gcpLocation: els.gcpLocation.value.trim(),
  };
}

function modelProviderAlreadyConfigured(provider) {
  if (state.modelProviders.some((item) => item.provider === provider)) {
    return true;
  }
  if (!state.exists || state.removedModelProviders.some((item) => item.provider === provider)) {
    return false;
  }
  return state.currentCredentialRefs.some((ref) => isModelProviderCredentialRef(ref) && credentialRefMatchesProvider(ref, provider));
}

function clearModelProviderCredentialForm() {
  els.model.value = "";
  els.apiKey.value = "";
  els.gcpCredentials.value = "";
  els.secretName.value = "";
  els.secretKey.value = "";
}

function renderModelProviders() {
  els.modelProviderList.innerHTML = "";
  if (state.modelProviders.length === 0) {
    return;
  }
  for (const [idx, item] of state.modelProviders.entries()) {
    const row = document.createElement("div");
    row.className = "integration-item";
    const main = document.createElement("div");
    main.className = "integration-item__main";
    const title = document.createElement("p");
    title.className = "integration-item__title";
    title.textContent = providerLabels[item.provider] || item.provider;
    const meta = document.createElement("p");
    meta.className = "integration-item__meta";
    meta.textContent = `${item.model || "provider default"} · ${item.secretName || "new key"}`;
    main.append(title, meta);
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "btn btn--sm btn--danger";
    remove.textContent = "Remove";
    remove.addEventListener("click", () => {
      state.removedModelProviders.push(item);
      state.modelProviders.splice(idx, 1);
      state.integrationsDirty = true;
      persistModelProviders();
      renderModelProviders();
      renderReview();
    });
    row.append(main, remove);
    els.modelProviderList.appendChild(row);
  }
}

function renderCredentialFields() {
  const vertex = isGoogleVertex();
  els.vertexBox.hidden = !vertex;
  els.credentialLabel.textContent = vertex ? "Service account key" : "API key";
  els.apiKey.hidden = vertex;
  els.gcpCredentials.hidden = !vertex;
  els.vertexGuide.hidden = !vertex;
  els.vertexHelp.hidden = !vertex;
  if (vertex && !els.gcpLocation.value.trim()) {
    els.gcpLocation.value = defaultGCPLocations[els.provider.value] || "";
  }
  renderCredentialSecretHint();
}

function renderCredentialSecretHint() {
  const name = effectiveSecretName();
  const key = effectiveSecretKey();
  els.secretNamePreview.textContent = `${name}/${key}`;
  els.secretName.placeholder = defaultSecretName();
  els.secretKey.placeholder = expectedSecretKey();
  const hint = document.getElementById("hint-secret-name");
  const code = document.createElement("code");
  code.textContent = `${key}: <value>`;
  hint.textContent = "The data key inside the Secret, for example ";
  hint.appendChild(code);
  hint.append(".");
}

function renderFilesystemSource() {
  const source = els.filesystemSource.value;
  els.gitBox.hidden = source !== "git";
  els.uploadBox.hidden = source !== "upload";
  els.workspaceSourceHelp.hidden = source === "";
}

function renderIntegrationFields() {
  const kind = els.integrationType.value;
  const custom = kind === "custom-credential";
  const slack = kind === "channel-slack";
  const github = kind === "github-pat";
  const typedChannelConfig = kind === "channel-telegram" || kind === "channel-slack";
  const noSecret = kind === "channel-whatsapp" || kind === "websearch-duckduckgo" || kind === "websearch-gemini" ||
    (kind === "custom-credential" && els.integrationCredentialType.value === "none");
  const channel = kind.startsWith("channel-");
  els.integrationCustomFields.hidden = !custom;
  els.integrationGithubFields.hidden = !github;
  els.integrationSlackFields.hidden = !slack;
  els.integrationNameField.hidden = github;
  els.integrationSecretFields.hidden = noSecret;
  els.integrationTypedChannelConfigField.hidden = !typedChannelConfig;
  els.integrationChannelConfigField.hidden = !channel || kind === "channel-whatsapp" || typedChannelConfig;
  els.integrationName.placeholder = defaultIntegrationName(kind);
  els.integrationSecretKey.placeholder = defaultIntegrationSecretKey(kind);
  els.integrationSecretLabel.textContent = integrationValueLabel(kind);
  els.integrationSecretValue.placeholder = integrationValuePlaceholder(kind);
  // Static, hand-written strings with vetted links — safe as innerHTML.
  els.integrationHelp.innerHTML = integrationHelp(kind);
  renderTypedChannelConfigHints(kind);
}

function integrationValueLabel(kind) {
  return {
    "channel-telegram": "Bot token",
    "channel-discord": "Bot token",
    "channel-slack": "Slack bot token",
    "github-pat": "Personal access token",
    "auth-password": "Password",
  }[kind] || "API key";
}

function integrationValuePlaceholder(kind) {
  return {
    "channel-telegram": "123456:ABC-…",
    "channel-discord": "Paste bot token",
    "channel-slack": "xoxb-…",
    "github-pat": "ghp_… or github_pat_…",
    "auth-password": "Choose a password",
  }[kind] || "Paste API key";
}

function defaultIntegrationName(kind) {
  return {
    "channel-telegram": "telegram",
    "channel-discord": "discord",
    "channel-slack": "slack",
    "channel-whatsapp": "whatsapp",
    "github-pat": "github",
    "websearch-brave": "brave-search",
    "websearch-tavily": "tavily-search",
    "auth-password": "gateway-password",
    "custom-credential": "my-credential",
  }[kind] || "";
}

function defaultIntegrationSecretKey(kind) {
  return {
    "channel-telegram": "bot-token",
    "channel-discord": "bot-token",
    "channel-slack": "bot-token",
    "github-pat": "token",
    "auth-password": "password",
  }[kind] || "api-key";
}

function extLink(href, label) {
  return `<a href="${href}" target="_blank" rel="noopener noreferrer">${label}</a>`;
}

function integrationHelp(kind) {
  const helps = {
    "channel-telegram": `Chat with your OpenClaw on Telegram. Message ${extLink("https://t.me/BotFather", "@BotFather")} to create a bot and get its token.`,
    "channel-discord": `Chat with your OpenClaw on Discord. Create a bot in the ${extLink("https://discord.com/developers/applications", "Discord Developer Portal")} and paste its token.`,
    "channel-slack": `Chat with your OpenClaw on Slack. You'll need two tokens from your ${extLink("https://api.slack.com/apps", "Slack app")}: a bot token (xoxb-…) and an app token (xapp-…).`,
    "github-pat": `Let your OpenClaw read and write your GitHub repositories. Paste a ${extLink("https://github.com/settings/tokens", "personal access token")}.`,
    "websearch-brave": `Let your OpenClaw search the web. Get a free API key from ${extLink("https://brave.com/search/api/", "Brave Search API")}.`,
    "websearch-tavily": `Let your OpenClaw search the web. Get an API key from ${extLink("https://tavily.com", "tavily.com")}.`,
    "websearch-duckduckgo": "Let your OpenClaw search the web with DuckDuckGo. No key needed — just click Add.",
    "websearch-gemini": "Let your OpenClaw search the web using Google Gemini. Uses your Gemini API key.",
    "auth-password": "Protect the gateway with a shared password.",
  };
  return helps[kind] || "Connect a custom API by domain or provider.";
}

function renderTypedChannelConfigHints(kind) {
  if (kind === "channel-telegram") {
    els.integrationDmPolicyHint.textContent = "Controls who can direct-message the Telegram bot.";
    els.integrationAllowFrom.placeholder = "12345, 67890";
    els.integrationAllowFromHint.innerHTML = 'Comma-separated Telegram user IDs. Use <code>*</code> with Open for everyone.';
    return;
  }
  if (kind === "channel-slack") {
    els.integrationDmPolicyHint.textContent = "Controls who can direct-message the Slack app.";
    els.integrationAllowFrom.placeholder = "U123, U456";
    els.integrationAllowFromHint.innerHTML = 'Comma-separated Slack user IDs. Use <code>*</code> with Open for everyone.';
    return;
  }
  els.integrationDmPolicyHint.textContent = "Controls direct-message access for this channel.";
  els.integrationAllowFrom.placeholder = "*";
  els.integrationAllowFromHint.innerHTML = 'Comma-separated sender IDs, or <code>*</code> for everyone when policy is Open.';
}

function splitList(value) {
  return value.split(",").map((part) => part.trim()).filter(Boolean);
}

function typedChannelConfigJSON(kind) {
  if (kind !== "channel-telegram" && kind !== "channel-slack") {
    return "";
  }
  const policy = els.integrationDmPolicy.value;
  const allowFrom = splitList(els.integrationAllowFrom.value);
  if (!policy && allowFrom.length === 0) {
    return "";
  }
  if (policy === "allowlist" && allowFrom.length === 0) {
    throw new Error("Allowlist DM policy needs at least one allowed sender.");
  }
  const config = {};
  if (policy) {
    config.dmPolicy = policy;
  }
  if (allowFrom.length > 0) {
    config.allowFrom = allowFrom;
  } else if (policy === "open") {
    config.allowFrom = ["*"];
  }
  return JSON.stringify(config);
}

function persistIntegrations() {
  const key = integrationStorageKey(state.namespace, state.selectedName);
  localStorage.removeItem("openclaw-deployer.integrations");
  if (!key) {
    return;
  }
  const safe = state.integrations.map(({ secretValue, appSecretValue, ...integration }) => integration);
  if (safe.length === 0) {
    localStorage.removeItem(key);
    return;
  }
  localStorage.setItem(key, JSON.stringify(safe));
}

// apiKey is stripped because a Vertex entry carries a service account JSON,
// which must never reach localStorage; a pending key stays in memory only.
function persistModelProviders() {
  const key = modelProviderStorageKey(state.namespace, state.selectedName);
  if (!key) {
    return;
  }
  const safe = state.modelProviders.map(({ apiKey, ...modelProvider }) => modelProvider);
  if (safe.length === 0) {
    localStorage.removeItem(key);
    return;
  }
  localStorage.setItem(key, JSON.stringify(safe));
}

function loadIntegrationsForSelection() {
  const scope = integrationStorageKey(state.namespace, state.selectedName);
  if (scope === state.integrationScope) {
    return;
  }
  state.integrationScope = scope;
  state.integrations = storedIntegrations(state.namespace, state.selectedName);
  state.modelProviders = storedModelProviders(state.namespace, state.selectedName);
  state.removedIntegrations = [];
  state.removedModelProviders = [];
  state.integrationsDirty = false;
  renderIntegrations();
  renderModelProviders();
}

function clearIntegrationsForSelection() {
	state.integrations = [];
	state.modelProviders = [];
	state.removedIntegrations = [];
	state.removedModelProviders = [];
	state.integrationsDirty = false;
	persistIntegrations();
	persistModelProviders();
	renderIntegrations();
	renderModelProviders();
}

function clearStoredIntegrations(namespace, name) {
  const key = integrationStorageKey(namespace, name);
  if (key) {
    localStorage.removeItem(key);
  }
}

function clearStoredModelProviders(namespace, name) {
  const key = modelProviderStorageKey(namespace, name);
  if (key) {
    localStorage.removeItem(key);
  }
}

function renderIntegrations() {
  els.integrationList.innerHTML = "";
  if (state.integrations.length === 0) {
    setIntegrationOpen(false);
    return;
  }
  for (const [idx, integration] of state.integrations.entries()) {
    const item = document.createElement("div");
    item.className = "integration-item";
    const main = document.createElement("div");
    main.className = "integration-item__main";
    const title = document.createElement("p");
    title.className = "integration-item__title";
    title.textContent = integrationLabels[integration.kind] || integration.kind;
    const meta = document.createElement("p");
    meta.className = "integration-item__meta";
    meta.textContent = integrationSummary(integration);
    main.append(title, meta);
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "btn btn--sm btn--danger";
    remove.textContent = "Remove";
    remove.addEventListener("click", () => {
      state.removedIntegrations.push(integration);
      state.integrations.splice(idx, 1);
      state.integrationsDirty = true;
      persistIntegrations();
      renderIntegrations();
      renderReview();
    });
    item.append(main, remove);
    els.integrationList.appendChild(item);
  }
}

function integrationSummary(integration) {
  const name = integration.name || defaultIntegrationName(integration.kind);
  if (integration.kind.startsWith("websearch-")) {
    return `Web search via ${integration.kind.replace("websearch-", "")}`;
  }
  if (integration.kind === "auth-password") {
    return `Gateway password · ${integration.secretName || "saved on deploy"}`;
  }
  if (integration.kind === "github-pat") {
    return `GitHub access · ${integration.secretName || "token saved on deploy"}${integration.exposeEnv ? " · gh CLI env" : ""}`;
  }
  const secret = integration.secretName || (integration.secretValue ? "token saved on deploy" : "no credential");
  return `${name} · ${secret}`;
}

function buildIntegrationFromForm() {
  const kind = els.integrationType.value;
  const channelConfig = typedChannelConfigJSON(kind) || els.integrationChannelConfig.value.trim();
  const integration = {
    kind,
    name: els.integrationName.value.trim(),
    secretName: els.integrationSecretName.value.trim(),
    secretKey: els.integrationSecretKey.value.trim(),
    secretValue: els.integrationSecretValue.value.trim(),
    appSecretName: els.integrationAppSecretName.value.trim(),
    appSecretKey: els.integrationAppSecretKey.value.trim(),
    appSecretValue: els.integrationAppSecretValue.value.trim(),
    credentialType: els.integrationCredentialType.value,
    domain: els.integrationDomain.value.trim(),
    provider: els.integrationProvider.value.trim(),
    header: els.integrationHeader.value.trim(),
    valuePrefix: els.integrationValuePrefix.value,
    pathPrefix: els.integrationPathPrefix.value.trim(),
    channelConfig,
    exposeEnv: els.integrationExposeEnv.checked,
  };
  if (kind === "custom-credential" && !integration.name) {
    throw new Error("Custom credentials need a credential name.");
  }
  if (kind === "channel-slack" && !integration.appSecretName && !integration.appSecretValue) {
    throw new Error("Slack also needs an app token (xapp-…) — paste it below the bot token.");
  }
  const noSecret = kind === "channel-whatsapp" || kind === "websearch-duckduckgo" || kind === "websearch-gemini" ||
    (kind === "custom-credential" && integration.credentialType === "none");
  if (!noSecret && !integration.secretName && !integration.secretValue) {
    throw new Error(`Paste the ${integrationValueLabel(kind).toLowerCase()} for this add-on.`);
  }
  if (integration.channelConfig) {
    JSON.parse(integration.channelConfig);
  }
  return integration;
}

function clearIntegrationSecretInputs() {
  els.integrationSecretValue.value = "";
  els.integrationAppSecretValue.value = "";
  els.integrationDmPolicy.value = "";
  els.integrationAllowFrom.value = "";
  els.integrationChannelConfig.value = "";
  els.integrationExposeEnv.checked = false;
}

function setAdvancedOpen(open) {
  setSectionOpen(els.advancedToggle, els.advancedBody, els.advancedCaret, open);
}

function setIntegrationOpen(open) {
  setSectionOpen(els.integrationToggle, els.integrationBody, els.integrationCaret, open);
}

function setSectionOpen(toggle, body, caret, open) {
  body.hidden = !open;
  toggle.setAttribute("aria-expanded", String(open));
  caret.classList.toggle("open", open);
}

// ---------- status alert + review ----------
function renderAlert({ kind, title, body, link, details, spin }) {
  els.alert.className = `alert alert--${kind === "idle" || kind === "info" ? "info" : kind}`;
  let icon;
  if (spin) {
    icon = '<span class="spinner"></span>';
  } else if (kind === "success") {
    icon = '<svg width="18" height="18" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="7" fill="var(--success-border)"/><path d="M5 8.2l2 2 4-4.4" stroke="#fff" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>';
  } else if (kind === "danger") {
    icon = '<svg width="18" height="18" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="7" fill="var(--danger-border)"/><path d="M8 4.3v4.4" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/><circle cx="8" cy="11.3" r="1" fill="#fff"/></svg>';
  } else {
    icon = '<svg width="18" height="18" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="7" stroke="var(--info-border)" stroke-width="1.4"/><path d="M8 7.2v4" stroke="var(--info-border)" stroke-width="1.6" stroke-linecap="round"/><circle cx="8" cy="4.7" r="1" fill="var(--info-border)"/></svg>';
  }

  const row = document.createElement("div");
  row.className = "alert__row";
  const iconWrap = document.createElement("div");
  iconWrap.className = "alert__icon";
  iconWrap.innerHTML = icon;
  const bodyWrap = document.createElement("div");
  bodyWrap.className = "alert__body";

  const titleEl = document.createElement("p");
  titleEl.className = "alert__title";
  titleEl.textContent = title;
  bodyWrap.appendChild(titleEl);
  if (body) {
    const bodyEl = document.createElement("p");
    bodyEl.className = "alert__text";
    bodyEl.textContent = body;
    bodyWrap.appendChild(bodyEl);
  }

  if (isSafeHref(link)) {
    const actions = document.createElement("div");
    actions.className = "alert__actions";
    const a = document.createElement("a");
    a.className = "alert__link";
    a.href = link;
    a.target = "_blank";
    a.rel = "noopener noreferrer";
    a.innerHTML = 'Open Control UI <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M6 3h7v7M13 3L4 12" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/></svg>';
    const copy = document.createElement("button");
    copy.type = "button";
    copy.className = "copy-btn";
    copy.textContent = state.copied === "alert" ? "Copied!" : "Copy URL";
    copy.addEventListener("click", () => copy_(link, "alert"));
    actions.append(a, copy);
    bodyWrap.appendChild(actions);
  }

  if (details && details.length) {
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "details-toggle";
    const caret = '<svg width="11" height="11" viewBox="0 0 12 12"><path d="M2 4l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>';
    let open = true;
    const list = document.createElement("ul");
    list.className = "details-list";
    for (const detail of details) {
      const li = document.createElement("li");
      li.textContent = detail;
      list.appendChild(li);
    }
    const sync = () => {
      toggle.innerHTML = `${caret}${open ? "Hide details" : "Show details"}`;
      toggle.querySelector("svg").style.transform = open ? "rotate(180deg)" : "rotate(0deg)";
      list.hidden = !open;
    };
    toggle.addEventListener("click", () => {
      open = !open;
      sync();
    });
    sync();
    bodyWrap.append(toggle, list);
  }

  row.append(iconWrap, bodyWrap);
  els.alert.replaceChildren(row);
}

function setStatus(message, isError = false) {
  renderAlert({ kind: isError ? "danger" : "info", title: isError ? "Something went wrong" : message, body: isError ? message : "" });
}

function renderReview() {
  const source = els.filesystemSource.value;
  const providerCredentialRefs = modelProviderCredentialRefsForPreview(false);
  let credential = "Not set";
  if (providerCredentialRefs.length > 0) {
    credential = formatCredentialRefs(providerCredentialRefs);
  } else if (state.exists && state.currentSecretNames.length > 0) {
    credential = `Keep existing: ${state.currentSecretNames.join(", ")}`;
  }
  const providerNames = providerCredentialRefs.length > 0
    ? [...new Set(providerCredentialRefs.map((ref) => credentialRefLabel(ref)))]
    : [providerLabels[els.provider.value] || els.provider.value];
  const modelProviderLabels = state.modelProviders.map((item) => {
    const label = providerLabels[item.provider] || item.provider;
    return `${label}${item.model ? ` (${item.model})` : ""}`;
  });
  const rows = [
    ["Project", els.namespace.value.trim() || "—"],
    ["Name", els.clawName.value.trim() || "—"],
    ["Provider", providerNames.join(", ")],
    ["Model", effectiveModel() || "—"],
    ["OpenClaw image", state.userManagedEnabled && els.openClawImage.value.trim() ? els.openClawImage.value.trim() : "Operator default"],
    ["API key", credential],
    ["Model providers", modelProviderLabels.length ? modelProviderLabels.join(", ") : "None"],
    ["Config ownership", selectedManagement() === "user" ? "User-managed" : "Operator-managed"],
    ["Doctor migration", els.doctorFix.checked ? "Run once per image" : "Not requested"],
    ["Memory", `Dreaming ${els.dreamingEnabled.checked ? "enabled" : "disabled"}; Wiki ${els.wikiEnabled.checked ? "enabled" : "disabled"}`],
    ["Add-ons", state.integrations.length ? state.integrations.map((i) => integrationLabels[i.kind] || i.kind).join(", ") : "None"],
    ["Starting files", source === "git" ? "From Git" : source === "upload" ? "Uploaded folder" : "None"],
  ];
  els.reviewList.replaceChildren(
    ...rows.map(([k, v]) => {
      const row = document.createElement("div");
      row.className = "review__row";
      const dt = document.createElement("dt");
      dt.textContent = k;
      const dd = document.createElement("dd");
      dd.textContent = v;
      row.append(dt, dd);
      return row;
    }),
  );
}

function credentialRefLabel(ref) {
  return providerLabels[ref.credential] || providerLabels[ref.provider] || ref.provider || ref.credential || "Credential";
}

function formatCredentialRefs(refs) {
  return refs.map((ref) => {
    const label = credentialRefLabel(ref);
    if (ref.action === "create") {
      return `${label}: new key`;
    }
    return `${label}: ${ref.name}${ref.key ? `/${ref.key}` : ""}`;
  }).join(", ");
}

// ---------- validation ----------
const errorFields = {
  namespace: "err-namespace",
  clawName: "err-clawName",
  credential: "err-credential",
  gcpProject: "err-gcpProject",
  gcpLocation: "err-gcpLocation",
  gitURL: "err-gitURL",
};

function validate() {
  const vertex = isGoogleVertex();
  const errs = {};
  if (!els.namespace.value.trim()) errs.namespace = "Pick the project where your OpenClaw should run.";
  if (!els.clawName.value.trim()) errs.clawName = "Give your OpenClaw a name.";
  const cred = (vertex ? els.gcpCredentials.value : els.apiKey.value).trim();
  const secretName = els.secretName.value.trim();
  if (!cred && !secretName && state.modelProviders.length === 0 && !state.exists) {
    errs.credential = vertex ? "Paste your service account JSON key, or use an existing Secret." : "Paste your API key, or use an existing Secret.";
  } else if (vertex && cred && !isSupportedGCPKey(cred)) {
    errs.credential = 'This doesn\'t look like a service account key — expected JSON with type "service_account" or "authorized_user".';
  }
  const needsGCPConfig = vertex && (!state.exists || selectedProviderCredentialSupplied());
  if (needsGCPConfig && !els.gcpProject.value.trim()) errs.gcpProject = "Enter your GCP project ID.";
  if (needsGCPConfig && !els.gcpLocation.value.trim()) errs.gcpLocation = "Enter a GCP region.";
  if (els.filesystemSource.value === "git" && !els.gitURL.value.trim()) errs.gitURL = "Enter the repository URL for your starting files.";
  return errs;
}

function renderErrors(errs) {
  const credInput = isGoogleVertex() ? els.gcpCredentials : els.apiKey;
  const inputs = {
    namespace: els.namespace,
    clawName: els.clawName,
    credential: els.secretName.value.trim() ? els.secretName : credInput,
    gcpProject: els.gcpProject,
    gcpLocation: els.gcpLocation,
    gitURL: els.gitURL,
  };
  for (const [key, errId] of Object.entries(errorFields)) {
    const el = document.getElementById(errId);
    const message = errs[key] || "";
    el.textContent = message;
    el.hidden = !message;
    const input = inputs[key];
    if (input) {
      if (message) {
        input.setAttribute("aria-invalid", "true");
      } else {
        input.removeAttribute("aria-invalid");
      }
    }
  }
}

// ---------- manifest preview ----------
function generateYaml() {
  const vertex = isGoogleVertex();
  const name = els.clawName.value.trim() || "instance";
  const ns = els.namespace.value.trim() || "<namespace>";
  let y = "";
  y += "apiVersion: claw.sandbox.redhat.com/v1alpha1\n";
  y += "kind: Claw\n";
  y += "metadata:\n";
  y += "  name: " + name + "\n";
  y += "  namespace: " + ns + "\n";
  y += "spec:\n";
  if (state.userManagedEnabled && els.openClawImage.value.trim()) {
    y += "  image: " + els.openClawImage.value.trim() + "\n";
  }
  if (shouldConfigureAgent()) {
    y += "  provider: " + els.provider.value + "\n";
    y += "  model: " + (effectiveModel() || "<provider default>") + "\n";
  }
  y += "  config:\n";
  y += "    management: " + selectedManagement() + "\n";
  if (els.doctorFix.checked) {
    y += "  migration:\n";
    y += "    doctorFix: true\n";
  }
  y += "  memory:\n";
  y += "    dreaming:\n";
  y += "      enabled: " + els.dreamingEnabled.checked + "\n";
  y += "    wiki:\n";
  y += "      enabled: " + els.wikiEnabled.checked + "\n";
  if (vertex && (shouldConfigureAgent() || selectedProviderCredentialSupplied())) {
    y += "  vertex:\n";
    y += "    projectID: " + (els.gcpProject.value.trim() || "<gcp-project>") + "\n";
    y += "    location: " + (els.gcpLocation.value.trim() || "<region>") + "\n";
  }
  const providerCredentials = modelProviderCredentialRefsForPreview();
  for (const item of state.modelProviders) {
    providerCredentials.push({
      credential: credentialNameForProvider(item.provider),
      provider: credentialProviderForProvider(item.provider),
      type: googleVertexProviders.has(item.provider) ? "gcp" : "",
      name: item.secretName || defaultSecretNameForProvider(item.provider),
      key: item.secretKey || expectedSecretKeyForProvider(item.provider),
      action: item.secretName ? "existing" : "create",
    });
  }
  const credentialIntegrations = state.integrations.filter((i) => i.kind.startsWith("channel-") || i.kind === "custom-credential");
  if (providerCredentials.length || credentialIntegrations.length) {
    y += "  credentials:\n";
    y += providerCredentialsYaml(providerCredentials);
    for (const integration of credentialIntegrations) {
      y += integrationCredentialYaml(integration);
    }
  }
  const webSearch = state.integrations.find((i) => i.kind.startsWith("websearch-"));
  if (webSearch) {
    y += "  webSearch:\n";
    y += "    provider: " + webSearch.kind.replace("websearch-", "") + "\n";
    if (webSearch.kind === "websearch-brave" || webSearch.kind === "websearch-tavily") {
      y += "    secretRef:\n";
      y += "      name: " + (webSearch.secretName || "<created-secret>") + "\n";
      y += "      key: " + (webSearch.secretKey || "api-key") + "\n";
    }
  }
  const passwordAuth = state.integrations.find((i) => i.kind === "auth-password");
  if (passwordAuth) {
    y += "  auth:\n";
    y += "    mode: password\n";
    y += "    passwordSecretRef:\n";
    y += "      name: " + (passwordAuth.secretName || "<created-secret>") + "\n";
    y += "      key: " + (passwordAuth.secretKey || "password") + "\n";
  }
  const githubPAT = state.integrations.find((i) => i.kind === "github-pat");
  if (githubPAT) {
    y += "  repoAccess:\n";
    y += "    github:\n";
    y += "      secretRef:\n";
    y += "        name: " + (githubPAT.secretName || "<created-secret>") + "\n";
    y += "        key: " + (githubPAT.secretKey || "token") + "\n";
    y += "      enableApiProxy: true\n";
    y += "      enableGitHttps: true\n";
    if (githubPAT.exposeEnv) y += "      exposeEnv: true\n";
  }
  const source = els.filesystemSource.value;
  if (source === "git") {
    y += "  workspaceSource:\n    git:\n";
    y += "      url: " + (els.gitURL.value.trim() || "<git-url>") + "\n";
    if (els.gitRef.value.trim()) y += "      ref: " + els.gitRef.value.trim() + "\n";
    if (els.gitPath.value.trim()) y += "      path: " + els.gitPath.value.trim() + "\n";
    if (els.gitSecretName.value.trim() || els.gitUsername.value.trim() || els.gitPassword.value) {
      y += "      secretRef:\n";
      y += "        name: " + (els.gitSecretName.value.trim() || "<created-git-secret>") + "\n";
    }
  } else if (source === "upload") {
    y += "  workspaceSource:\n    configMap:\n      name: " + name + "-workspace\n";
  }
  return y;
}

function providerCredentialsYaml(refs) {
  let y = "";
  const grouped = new Map();
  for (const ref of refs) {
    const credential = ref.credential || ref.provider;
    const provider = ref.provider || credential;
    const type = ref.type || "";
    const key = `${credential}\0${provider}\0${type}`;
    if (!grouped.has(key)) {
      grouped.set(key, { credential, provider, type, refs: [] });
    }
    grouped.get(key).refs.push(ref);
  }
  for (const group of grouped.values()) {
    y += "    - name: " + group.credential + "\n";
    if (group.type) y += "      type: " + group.type + "\n";
    y += "      provider: " + group.provider + "\n";
    y += "      secretRef:\n";
    for (const ref of group.refs) {
      y += "        - name: " + ref.name + "\n";
      y += "          key: " + (ref.key || "api-key") + "\n";
    }
  }
  return y;
}

function integrationCredentialYaml(integration) {
  const name = integration.name || defaultIntegrationName(integration.kind);
  let y = "    - name: " + name + "\n";
  if (integration.kind.startsWith("channel-")) {
    const channel = integration.kind.replace("channel-", "");
    y += "      channel: " + channel + "\n";
    if (channel !== "whatsapp") {
      y += "      secretRef:\n";
      y += "        - name: " + (integration.secretName || "<created-secret>") + "\n";
      y += "          key: " + (integration.secretKey || defaultIntegrationSecretKey(integration.kind)) + "\n";
      if (channel === "slack") {
        y += "          role: botToken\n";
        y += "        - name: " + (integration.appSecretName || integration.secretName || "<created-secret>") + "\n";
        y += "          key: " + (integration.appSecretKey || "app-token") + "\n";
        y += "          role: appToken\n";
      }
    }
    if (integration.channelConfig) {
      y += "      channelConfig:\n";
      y += yamlObject(JSON.parse(integration.channelConfig), "        ");
    }
    return y;
  }
  y += "      type: " + (integration.credentialType || "bearer") + "\n";
  if (integration.provider) y += "      provider: " + integration.provider + "\n";
  if (integration.domain) y += "      domain: " + integration.domain + "\n";
  if (integration.kind === "custom-credential") {
    if (integration.header || integration.valuePrefix) {
      y += "      apiKey:\n";
      if (integration.header) y += "        header: " + integration.header + "\n";
      if (integration.valuePrefix) y += "        valuePrefix: " + integration.valuePrefix + "\n";
    }
    if (integration.pathPrefix) {
      y += "      pathToken:\n";
      y += "        prefix: " + integration.pathPrefix + "\n";
    }
  }
  if (integration.secretName || integration.secretValue) {
    y += "      secretRef:\n";
    y += "        - name: " + (integration.secretName || "<created-secret>") + "\n";
    y += "          key: " + (integration.secretKey || "api-key") + "\n";
  }
  return y;
}

function yamlObject(value, indent) {
  let y = "";
  for (const [key, child] of Object.entries(value)) {
    if (Array.isArray(child)) {
      y += `${indent}${key}: [${child.map((item) => JSON.stringify(item)).join(", ")}]\n`;
    } else if (child && typeof child === "object") {
      y += `${indent}${key}:\n`;
      y += yamlObject(child, indent + "  ");
    } else {
      y += `${indent}${key}: ${JSON.stringify(child)}\n`;
    }
  }
  return y;
}

function openPreview() {
  els.previewYaml.textContent = generateYaml();
  els.previewOverlay.hidden = false;
}

function closePreview() {
  els.previewOverlay.hidden = true;
}

function copy_(text, id) {
  try {
    navigator.clipboard.writeText(text);
  } catch {
    // Clipboard may be unavailable (e.g. insecure context); ignore.
  }
  state.copied = id;
  if (id === "yaml") {
    els.copyYaml.textContent = "Copied!";
  } else {
    renderList(state.claws);
  }
  clearTimeout(copy_.timer);
  copy_.timer = setTimeout(() => {
    state.copied = "";
    els.copyYaml.textContent = "Copy YAML";
    if (id === "alert") {
      renderList(state.claws);
    }
  }, 1600);
}

function setBusy(busy) {
  els.provision.disabled = busy;
  els.reset.disabled = busy;
  for (const button of document.querySelectorAll(".claw-action")) {
    button.disabled = busy;
  }
}

// ---------- actions ----------
els.provision.addEventListener("click", async () => {
  state.submitted = true;
  const errs = validate();
  renderErrors(errs);
  if (Object.keys(errs).length) {
    if (errs.gitURL) {
      setAdvancedOpen(true);
    }
    renderAlert({
      kind: "danger",
      title: "A few fields need attention",
      body: "Each one is marked inline in the form.",
      details: Object.values(errs),
    });
    return;
  }

  const namespace = els.namespace.value.trim();
  const name = els.clawName.value.trim();
  const provider = els.provider.value;
  const model = els.model.value.trim();
  const openClawImage = state.userManagedEnabled ? els.openClawImage.value.trim() : "";
  const version = els.version.value.trim();
  const configureAgent = shouldConfigureAgent();
  const vertex = isGoogleVertex();
  const apiKey = (vertex ? els.gcpCredentials.value : els.apiKey.value).trim();
  const secretName = els.secretName.value.trim();
  const secretKey = els.secretKey.value.trim();
  const gcpProject = els.gcpProject.value.trim();
  const gcpLocation = els.gcpLocation.value.trim();
  const management = selectedManagement();
  const doctorFix = els.doctorFix.checked;
  const dreamingEnabled = els.dreamingEnabled.checked;
  const wikiEnabled = els.wikiEnabled.checked;
  const source = els.filesystemSource.value;
  const gitURL = els.gitURL.value.trim();
  const gitRef = els.gitRef.value.trim();
  const gitPath = els.gitPath.value.trim();
  const gitSecretName = els.gitSecretName.value.trim();
  const gitUsername = els.gitUsername.value.trim();
  const gitPassword = els.gitPassword.value;
  const integrations = state.integrations;
  const removedIntegrations = state.removedIntegrations;
  const modelProviders = state.modelProviders;
  const removedModelProviders = state.removedModelProviders;

  if (source === "upload" && els.agentFiles.files.length === 0) {
    setAdvancedOpen(true);
    setStatus("Choose a folder to upload, or set Starting files back to None.", true);
    return;
  }

  setBusy(true);
  try {
    // An uploaded folder is packaged into a ConfigMap first; provisioning then
    // references it. Git and "None" provision directly.
    let filesystemSource = source;
    let configMapName = "";
    if (source === "upload") {
      setStatus("Uploading folder…");
      configMapName = await uploadAgentFiles(namespace, name, els.agentFiles.files);
      filesystemSource = "configmap";
    }
    setStatus(state.exists ? "Saving changes…" : "Creating your OpenClaw…");
    const current = await api("/api/provision", {
      method: "POST",
      body: JSON.stringify({
        namespace, name, provider, configureAgent, model, openClawImage, version, apiKey, secretName, secretKey, gcpProject, gcpLocation, management, doctorFix,
        dreamingEnabled, wikiEnabled,
        filesystemSource, gitURL, gitRef, gitPath, gitSecretName, gitUsername, gitPassword, configMapName,
        integrations, removedIntegrations, modelProviders, removedModelProviders,
      }),
    });
    els.apiKey.value = "";
    els.gcpCredentials.value = "";
    els.gitPassword.value = "";
    for (const integration of state.integrations) {
      delete integration.secretValue;
      delete integration.appSecretValue;
    }
    for (const modelProvider of state.modelProviders) {
      delete modelProvider.apiKey;
    }
    state.removedIntegrations = [];
    state.removedModelProviders = [];
    state.integrationsDirty = false;
    persistIntegrations();
    persistModelProviders();
    els.agentFiles.value = "";
    state.selectedName = current.name || name;
    els.clawName.value = state.selectedName;
    await refresh();
  } catch (error) {
    setStatus(error.message, true);
  } finally {
    setBusy(false);
  }
});

els.reset.addEventListener("click", () => {
  const previousNamespace = state.namespace;
  const previousName = state.selectedName;
  state.submitted = false;
  els.namespace.value = "";
  els.clawName.value = "instance";
  els.provider.value = "openrouter";
  els.model.value = "";
  els.openClawImage.value = "";
  els.version.value = "";
  els.secretName.value = "";
  els.secretKey.value = "";
  els.gcpProject.value = "";
  els.gcpLocation.value = defaultGCPLocations.openrouter || "";
  els.management.value = "user";
  els.doctorFix.checked = false;
  els.doctorFix.disabled = false;
  els.doctorFixHint.textContent = "Use this when updating to a new OpenClaw version, or when the logs say a doctor migration is required.";
  els.dreamingEnabled.checked = false;
  els.wikiEnabled.checked = false;
  els.apiKey.value = "";
  els.gcpCredentials.value = "";
  els.filesystemSource.value = "";
  els.gitURL.value = "";
  els.gitRef.value = "";
  els.gitPath.value = "";
  els.gitSecretName.value = "";
  els.gitUsername.value = "";
  els.gitPassword.value = "";
  els.agentFiles.value = "";
  state.namespace = "";
  state.selectedName = "instance";
  state.integrations = [];
  state.modelProviders = [];
  state.removedIntegrations = [];
  state.removedModelProviders = [];
  state.integrationScope = integrationStorageKey("", "instance");
  state.integrationsDirty = false;
  state.management = "user";
  state.openClawImage = "";
  state.version = "";
  clearStoredIntegrations(previousNamespace, previousName);
  clearStoredModelProviders(previousNamespace, previousName);
  persistIntegrations();
  persistModelProviders();
  els.uploadName.hidden = true;
  renderErrors({});
  renderModelOptions();
  renderModelProviders();
  renderCredentialFields();
  renderCredentialSecretHint();
  renderIntegrations();
  renderFilesystemSource();
  refresh();
});

async function restartClaw(namespace, name) {
  if (!namespace || !name || !confirm(`Restart ${namespace}/${name}?`)) {
    return;
  }
  setBusy(true);
  setStatus(`Restarting ${namespace}/${name}…`);
  try {
    await api(`/api/restart?namespace=${encodeURIComponent(namespace)}&name=${encodeURIComponent(name)}`, { method: "POST" });
    await refresh();
  } catch (error) {
    setStatus(error.message, true);
  } finally {
    setBusy(false);
  }
}

async function setClawIdle(namespace, name, idle) {
  const action = idle ? "Idle" : "Unidle";
  const detail = idle
    ? " This stops its workloads but preserves data and configuration."
    : " This starts its workloads again.";
  if (!namespace || !name || !confirm(`${action} ${namespace}/${name}?${detail}`)) {
    return;
  }
  setBusy(true);
  setStatus(`${action === "Idle" ? "Idling" : "Unidling"} ${namespace}/${name}…`);
  try {
    await api(`/api/idle?namespace=${encodeURIComponent(namespace)}&name=${encodeURIComponent(name)}&idle=${idle}`, { method: "POST" });
    await refresh();
  } catch (error) {
    setStatus(error.message, true);
  } finally {
    setBusy(false);
  }
}

async function deleteClaw(namespace, name) {
  if (!namespace || !name || !confirm(`Delete ${namespace}/${name}?`)) {
    return;
  }
  setBusy(true);
  setStatus(`Deleting ${namespace}/${name}…`);
  try {
    await api(`/api/claw?namespace=${encodeURIComponent(namespace)}&name=${encodeURIComponent(name)}`, { method: "DELETE" });
    clearStoredIntegrations(namespace, name);
    clearStoredModelProviders(namespace, name);
    if (namespace === state.namespace && name === state.selectedName) {
      clearIntegrationsForSelection();
    }
    await refresh();
  } catch (error) {
    setStatus(error.message, true);
  } finally {
    setBusy(false);
  }
}

async function uploadAgentFiles(namespace, name, fileList) {
  const form = new FormData();
  for (const file of fileList) {
    const relative = file.webkitRelativePath || file.name;
    // Drop the top-level folder name so archive paths are repo-root relative.
    const archivePath = relative.includes("/") ? relative.slice(relative.indexOf("/") + 1) : relative;
    form.append(archivePath, file, file.name);
  }
  const response = await fetch(
    `/api/agentfiles?namespace=${encodeURIComponent(namespace)}&name=${encodeURIComponent(name)}`,
    { method: "POST", body: form },
  );
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(payload.error || `Upload failed: ${response.status}`);
  }
  return payload.configMapName;
}

// ---------- listeners ----------
els.themeToggle.addEventListener("click", () => applyTheme(state.theme === "dark" ? "light" : "dark"));

// "Use the existing Secret" hint links open the matching Advanced disclosure.
for (const button of document.querySelectorAll("[data-open-details]")) {
  button.addEventListener("click", () => {
    const details = document.getElementById(button.dataset.openDetails);
    details.open = true;
    const input = details.querySelector("input");
    if (input) {
      input.focus();
    }
  });
}

els.detailsToggle.addEventListener("click", () => setSectionOpen(els.detailsToggle, els.detailsBody, els.detailsCaret, els.detailsBody.hidden));
els.providerToggle.addEventListener("click", () => setSectionOpen(els.providerToggle, els.providerBody, els.providerCaret, els.providerBody.hidden));
els.integrationToggle.addEventListener("click", () => setIntegrationOpen(els.integrationBody.hidden));
els.advancedToggle.addEventListener("click", () => setAdvancedOpen(els.advancedBody.hidden));

els.previewOpen.addEventListener("click", openPreview);
els.previewClose.addEventListener("click", closePreview);
els.previewClose2.addEventListener("click", closePreview);
els.previewOverlay.addEventListener("click", (event) => {
  if (event.target === els.previewOverlay) {
    closePreview();
  }
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !els.previewOverlay.hidden) {
    closePreview();
  }
});
els.copyYaml.addEventListener("click", () => copy_(generateYaml(), "yaml"));

let namespaceDebounce;
els.namespace.addEventListener("input", () => {
  clearTimeout(namespaceDebounce);
  namespaceDebounce = setTimeout(refresh, 300);
});
els.namespace.addEventListener("change", refresh);
els.clawName.addEventListener("change", refresh);

els.provider.addEventListener("change", () => {
  state.provider = els.provider.value;
  els.model.value = "";
  state.model = "";
  renderModelOptions();
  renderCredentialFields();
  renderCredentialSecretHint();
  localStorage.setItem("openclaw-deployer.provider", state.provider);
  localStorage.setItem("openclaw-deployer.model", "");
  revalidate();
});

els.modelProviderAdd.addEventListener("click", () => {
  try {
    const modelProvider = buildModelProviderFromForm();
    if (modelProviderAlreadyConfigured(modelProvider.provider)) {
      throw new Error(`${providerLabels[modelProvider.provider] || modelProvider.provider} is already configured. Remove it before adding it again.`);
    }
    state.removedModelProviders = state.removedModelProviders.filter((item) => item.provider !== modelProvider.provider);
    state.modelProviders.push(modelProvider);
    state.integrationsDirty = true;
    persistModelProviders();
    clearModelProviderCredentialForm();
    renderModelOptions();
    renderCredentialSecretHint();
    renderModelProviders();
    renderReview();
  } catch (error) {
    renderAlert({
      kind: "danger",
      title: "Model provider needs attention",
      body: error.message,
    });
  }
});

els.filesystemSource.addEventListener("change", () => {
  state.filesystemSource = els.filesystemSource.value;
  localStorage.setItem("openclaw-deployer.filesystemSource", state.filesystemSource);
  renderFilesystemSource();
  revalidate();
});

els.management.addEventListener("change", () => {
  state.management = selectedManagement();
  localStorage.setItem("openclaw-deployer.management", state.management);
  revalidate();
});

els.agentFiles.addEventListener("change", () => {
  const n = els.agentFiles.files ? els.agentFiles.files.length : 0;
  els.uploadName.hidden = n === 0;
  els.uploadName.textContent = n ? `${n} file${n === 1 ? "" : "s"} selected` : "";
});

for (const [el, key] of [
  [els.gitURL, "gitURL"],
  [els.gitRef, "gitRef"],
  [els.gitPath, "gitPath"],
]) {
  el.addEventListener("change", () => {
    state[key] = el.value.trim();
    localStorage.setItem(`openclaw-deployer.${key}`, state[key]);
  });
}

els.integrationType.addEventListener("change", () => {
  renderIntegrationFields();
  renderReview();
});
els.integrationCredentialType.addEventListener("change", renderIntegrationFields);

els.integrationAdd.addEventListener("click", () => {
  try {
    const integration = buildIntegrationFromForm();
    state.integrations.push(integration);
    state.integrationsDirty = true;
    persistIntegrations();
    clearIntegrationSecretInputs();
    setIntegrationOpen(true);
    renderIntegrations();
    renderReview();
  } catch (error) {
    renderAlert({
      kind: "danger",
      title: "Integration needs attention",
      body: error.message,
    });
  }
});

els.gcpProject.addEventListener("change", () => {
  state.gcpProject = els.gcpProject.value.trim();
  localStorage.setItem("openclaw-deployer.gcpProject", state.gcpProject);
});
els.gcpLocation.addEventListener("change", () => {
  state.gcpLocation = els.gcpLocation.value.trim();
  localStorage.setItem("openclaw-deployer.gcpLocation", state.gcpLocation);
});
els.model.addEventListener("change", () => {
  state.model = els.model.value.trim();
  localStorage.setItem("openclaw-deployer.model", state.model);
});
els.openClawImage.addEventListener("change", () => {
  state.openClawImage = state.userManagedEnabled ? els.openClawImage.value.trim() : "";
  renderReview();
});
els.secretName.addEventListener("change", () => {
  state.secretName = els.secretName.value.trim();
  renderCredentialSecretHint();
  revalidate();
});
els.secretKey.addEventListener("change", () => {
  state.secretKey = els.secretKey.value.trim();
  renderCredentialSecretHint();
  revalidate();
});

// Keep the Review summary live, and re-run validation once the user has tried
// to deploy so inline errors clear as fields are fixed.
const formEl = document.getElementById("form");
formEl.addEventListener("input", () => {
  renderCredentialSecretHint();
  renderReview();
  revalidate();
});
// Deploy is an explicit button; never let Enter submit and reload the page.
formEl.addEventListener("submit", (event) => event.preventDefault());

function revalidate() {
  renderReview();
  if (state.submitted) {
    renderErrors(validate());
  }
}

function isSafeHref(href) {
  if (!href) {
    return false;
  }
  try {
    const url = new URL(href, document.baseURI);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

function isSupportedGCPKey(value) {
  try {
    const parsed = JSON.parse(value);
    return parsed.type === "service_account" || parsed.type === "authorized_user";
  } catch {
    return false;
  }
}

init();
setInterval(() => {
  if (state.claws.some((claw) => !claw.ready)) {
    refresh();
  }
}, 10000);
