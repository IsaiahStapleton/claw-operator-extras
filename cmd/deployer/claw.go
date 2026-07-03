/*
Copyright 2026 Red Hat.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func (s *server) listClaws(ctx context.Context, identity userIdentity, namespace string) ([]stateResponse, error) {
	var list map[string]any
	if err := s.kubeJSON(ctx, identity, http.MethodGet, apiPath("apis/claw.sandbox.redhat.com/v1alpha1/namespaces", namespace, "claws"), nil, &list); err != nil {
		return nil, err
	}
	return clawStatesFromList(list), nil
}

func (s *server) listAllClaws(ctx context.Context, identity userIdentity) ([]stateResponse, error) {
	var list map[string]any
	if err := s.kubeJSON(ctx, identity, http.MethodGet, "/apis/claw.sandbox.redhat.com/v1alpha1/claws", nil, &list); err != nil {
		var apiErr apiError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
			return nil, err
		}
		return s.listClawsByVisibleNamespaces(ctx, identity)
	}
	return clawStatesFromList(list), nil
}

func (s *server) listClawsByVisibleNamespaces(ctx context.Context, identity userIdentity) ([]stateResponse, error) {
	namespaces, err := s.visibleNamespaceNames(ctx, identity)
	if err != nil {
		return nil, err
	}
	claws := []stateResponse{}
	for _, name := range namespaces {
		namespaceClaws, err := s.listClaws(ctx, identity, name)
		if err != nil {
			var apiErr apiError
			if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusForbidden || apiErr.StatusCode == http.StatusNotFound) {
				continue
			}
			return nil, err
		}
		claws = append(claws, namespaceClaws...)
	}
	sortClaws(claws)
	return claws, nil
}

func (s *server) visibleNamespaceNames(ctx context.Context, identity userIdentity) ([]string, error) {
	var namespaceList map[string]any
	if err := s.kubeJSON(ctx, identity, http.MethodGet, "/api/v1/namespaces", nil, &namespaceList); err != nil {
		var apiErr apiError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
			return nil, err
		}
		var projectList map[string]any
		if projectErr := s.kubeJSON(ctx, identity, http.MethodGet, "/apis/project.openshift.io/v1/projects", nil, &projectList); projectErr != nil {
			return nil, projectErr
		}
		namespaceList = projectList
	}
	items, _, _ := nestedSlice(namespaceList, "items")
	names := make([]string, 0, len(items))
	for _, item := range items {
		namespace, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := nestedString(namespace, "metadata", "name")
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func clawStatesFromList(list map[string]any) []stateResponse {
	items, _, _ := nestedSlice(list, "items")
	claws := make([]stateResponse, 0, len(items))
	for _, item := range items {
		claw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		claws = append(claws, stateFromClaw(claw))
	}
	sortClaws(claws)
	return claws
}

func sortClaws(claws []stateResponse) {
	sort.Slice(claws, func(i, j int) bool {
		if claws[i].Namespace != claws[j].Namespace {
			return claws[i].Namespace < claws[j].Namespace
		}
		return claws[i].Name < claws[j].Name
	})
}

func (s *server) getState(ctx context.Context, identity userIdentity, namespace, name string) (stateResponse, error) {
	var claw map[string]any
	err := s.kubeJSON(ctx, identity, http.MethodGet, apiPath("apis/claw.sandbox.redhat.com/v1alpha1/namespaces", namespace, "claws", name), nil, &claw)
	if err != nil {
		return stateResponse{}, err
	}
	return stateFromClaw(claw), nil
}

func stateFromClaw(claw map[string]any) stateResponse {
	ready, reason, message := readyCondition(claw)
	gatewayURL, _, _ := nestedString(claw, "status", "gatewayURL")
	if gatewayURL == "" {
		gatewayURL, _, _ = nestedString(claw, "status", "url")
	}
	providers := credentialProviders(claw)
	provider := ""
	if len(providers) > 0 {
		provider = providers[0]
	}
	name, _, _ := nestedString(claw, "metadata", "name")
	namespace, _, _ := nestedString(claw, "metadata", "namespace")
	createdAt, _, _ := nestedString(claw, "metadata", "creationTimestamp")
	model, _, _ := nestedString(claw, "spec", "config", "raw", "agents", "defaults", "model", "primary")
	agentName := firstAgentName(claw)
	management, _, _ := nestedString(claw, "spec", "config", "management")
	if management == "" {
		management = defaultManagement
	}

	return stateResponse{
		Namespace:      namespace,
		Name:           name,
		Exists:         true,
		Ready:          ready,
		Reason:         reason,
		Message:        message,
		GatewayURL:     gatewayURL,
		Provider:       provider,
		Providers:      providers,
		Model:          model,
		AgentName:      agentName,
		Management:     management,
		CreatedAt:      createdAt,
		SecretNames:    credentialSecretNames(claw),
		CredentialRefs: credentialRefs(claw),
	}
}

func (s *server) applySecret(ctx context.Context, identity userIdentity, req provisionRequest) error {
	return s.applyOpaqueSecret(ctx, identity, req.Namespace, credentialSecretName(req), map[string]string{
		credentialSecretKey(req): req.APIKey,
	}, map[string]string{
		managedByLabel: managedByValue,
		instanceLabel:  req.Name,
		providerLabel:  req.Provider,
	})
}

func (s *server) applyOpaqueSecret(ctx context.Context, identity userIdentity, namespace, name string, data, labels map[string]string) error {
	encoded := make(map[string]string, len(data))
	for key, value := range data {
		encoded[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	body := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels":    labels,
		},
		"type": "Opaque",
		"data": encoded,
	}
	return s.apply(ctx, identity, apiPath("api/v1/namespaces", namespace, "secrets", name), body)
}

func (s *server) applyIntegrationSecrets(ctx context.Context, identity userIdentity, req provisionRequest) error {
	for _, integration := range req.Integrations {
		secrets := integrationSecrets(req.Name, integration)
		for _, secret := range secrets {
			if len(secret.data) == 0 {
				continue
			}
			if err := s.applyOpaqueSecret(ctx, identity, req.Namespace, secret.name, secret.data, map[string]string{
				managedByLabel: managedByValue,
				instanceLabel:  req.Name,
			}); err != nil {
				return err
			}
		}
	}
	if req.GitUsername != "" || req.GitPassword != "" {
		secretName := req.GitSecretName
		if secretName == "" {
			secretName = "openclaw-" + req.Name + "-git-credentials"
		}
		if err := s.applyOpaqueSecret(ctx, identity, req.Namespace, secretName, map[string]string{
			"username": req.GitUsername,
			"password": req.GitPassword,
		}, map[string]string{
			managedByLabel: managedByValue,
			instanceLabel:  req.Name,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) ensureProject(ctx context.Context, identity userIdentity, namespace string) error {
	if err := s.kubeJSON(ctx, identity, http.MethodGet, apiPath("api/v1/namespaces", namespace), nil, nil); err == nil {
		return nil
	} else {
		var apiErr apiError
		if !errors.As(err, &apiErr) || (apiErr.StatusCode != http.StatusNotFound && apiErr.StatusCode != http.StatusForbidden) {
			return err
		}
	}

	body := map[string]any{
		"apiVersion": "project.openshift.io/v1",
		"kind":       "ProjectRequest",
		"metadata": map[string]string{
			"name": namespace,
		},
	}
	err := s.kubeJSON(ctx, identity, http.MethodPost, "/apis/project.openshift.io/v1/projectrequests", body, nil)
	var apiErr apiError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
		return nil
	}
	return err
}

func (s *server) applyClaw(ctx context.Context, identity userIdentity, req provisionRequest) error {
	if req.Management == "" {
		req.Management = s.defaultConfigManagement()
	}
	credentials, rawConfig, agentFiles, err := s.currentClawSpec(ctx, identity, req.Namespace, req.Name)
	if err != nil {
		return err
	}
	existingAuth, existingWebSearch, existingRepoAccess, err := s.currentClawTopLevelMaps(ctx, identity, req.Namespace, req.Name)
	if err != nil {
		return err
	}
	if req.APIKey != "" || req.SecretName != "" {
		credentials = upsertProvisionCredential(credentials, req)
	}
	credentials, auth, webSearch, repoAccess, err := applyIntegrationsToSpec(credentials, req)
	if err != nil {
		return err
	}
	if req.ConfigureAgent && req.AgentName != "" {
		rawConfig = applyAgentConfig(rawConfig, req.AgentName, req.Model)
	}
	if next := agentFilesSpec(req); next != nil {
		agentFiles = next
	}

	spec := map[string]any{
		"credentials": credentials,
		"config": map[string]any{
			"raw":        rawConfig,
			"mergeMode":  "merge",
			"management": req.Management,
		},
	}
	if len(agentFiles) > 0 {
		spec["agentFiles"] = agentFiles
	}
	if len(auth) > 0 {
		spec["auth"] = auth
	} else if len(existingAuth) > 0 && shouldPreserveAuth(req.Name, existingAuth, req.RemovedIntegrations) {
		spec["auth"] = existingAuth
	}
	if len(webSearch) > 0 {
		spec["webSearch"] = webSearch
	} else if len(existingWebSearch) > 0 && shouldPreserveWebSearch(req.Name, existingWebSearch, req.RemovedIntegrations) {
		spec["webSearch"] = existingWebSearch
	}
	if len(repoAccess) > 0 {
		spec["repoAccess"] = repoAccess
	} else if len(existingRepoAccess) > 0 && shouldPreserveRepoAccess(req.Name, existingRepoAccess, req.RemovedIntegrations) {
		spec["repoAccess"] = existingRepoAccess
	}

	body := map[string]any{
		"apiVersion": "claw.sandbox.redhat.com/v1alpha1",
		"kind":       "Claw",
		"metadata": map[string]any{
			"name":      req.Name,
			"namespace": req.Namespace,
			"labels": map[string]string{
				managedByLabel: managedByValue,
			},
		},
		"spec": spec,
	}
	return s.apply(ctx, identity, apiPath("apis/claw.sandbox.redhat.com/v1alpha1/namespaces", req.Namespace, "claws", req.Name), body)
}

// agentFilesSpec builds the spec.agentFiles object for a provision request, or
// nil when no filesystem source was requested (so an existing source is left
// untouched on update). The operator only acts on agentFiles for user-managed
// Claws; handleProvision enforces that before this is called.
func agentFilesSpec(req provisionRequest) map[string]any {
	switch req.FilesystemSource {
	case "git":
		git := map[string]any{"url": req.GitURL}
		if req.GitRef != "" {
			git["ref"] = req.GitRef
		}
		if req.GitPath != "" {
			git["path"] = req.GitPath
		}
		if req.GitSecretName != "" || req.GitUsername != "" || req.GitPassword != "" {
			name := req.GitSecretName
			if name == "" {
				name = "openclaw-" + req.Name + "-git-credentials"
			}
			git["secretRef"] = map[string]any{"name": name}
		}
		return map[string]any{"git": git}
	case "configmap":
		ref := map[string]any{"name": req.ConfigMapName}
		if req.ConfigMapKey != "" {
			ref["key"] = req.ConfigMapKey
		}
		return map[string]any{"configMapRef": ref}
	default:
		return nil
	}
}

func (s *server) defaultConfigManagement() string {
	if s.defaultManagement == "" {
		return defaultManagement
	}
	return s.defaultManagement
}

func (s *server) currentClawSpec(ctx context.Context, identity userIdentity, namespace, name string) ([]any, map[string]any, map[string]any, error) {
	var claw map[string]any
	err := s.kubeJSON(ctx, identity, http.MethodGet, apiPath("apis/claw.sandbox.redhat.com/v1alpha1/namespaces", namespace, "claws", name), nil, &claw)
	if err != nil {
		var apiErr apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil, map[string]any{}, nil, nil
		}
		return nil, nil, nil, err
	}
	credentials, _, _ := nestedSlice(claw, "spec", "credentials")
	raw, _, _ := nestedMap(claw, "spec", "config", "raw")
	agentFiles, _, _ := nestedMap(claw, "spec", "agentFiles")
	return credentials, cloneMap(raw), cloneMap(agentFiles), nil
}

func (s *server) currentClawTopLevelMaps(ctx context.Context, identity userIdentity, namespace, name string) (map[string]any, map[string]any, map[string]any, error) {
	var claw map[string]any
	err := s.kubeJSON(ctx, identity, http.MethodGet, apiPath("apis/claw.sandbox.redhat.com/v1alpha1/namespaces", namespace, "claws", name), nil, &claw)
	if err != nil {
		var apiErr apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, err
	}
	auth, _, _ := nestedMap(claw, "spec", "auth")
	webSearch, _, _ := nestedMap(claw, "spec", "webSearch")
	repoAccess, _, _ := nestedMap(claw, "spec", "repoAccess")
	return cloneMap(auth), cloneMap(webSearch), cloneMap(repoAccess), nil
}

func upsertCredential(credentials []any, instanceName, provider string) []any {
	return upsertProvisionCredential(credentials, provisionRequest{Name: instanceName, Provider: provider})
}

func upsertProvisionCredential(credentials []any, req provisionRequest) []any {
	option := providers[req.Provider]
	next := make([]any, 0, len(credentials)+1)
	replaced := false
	for _, credential := range credentials {
		credentialMap, ok := credential.(map[string]any)
		if !ok {
			continue
		}
		name, _ := credentialMap["name"].(string)
		if name == option.CredentialName {
			next = append(next, providerCredentialForRequest(req))
			replaced = true
			continue
		}
		next = append(next, credentialMap)
	}
	if !replaced {
		next = append(next, providerCredentialForRequest(req))
	}
	return next
}

func providerCredentialForRequest(req provisionRequest) map[string]any {
	option := providers[req.Provider]
	credential := map[string]any{
		"name":     option.CredentialName,
		"provider": option.CredentialProvider,
		"secretRef": []map[string]string{
			{"name": credentialSecretName(req), "key": credentialSecretKey(req)},
		},
	}
	if option.CredentialType != "" {
		credential["type"] = option.CredentialType
	}
	if option.RequiresGCP {
		credential["gcp"] = map[string]string{
			"project":  req.GCPProject,
			"location": req.GCPLocation,
		}
	}
	return credential
}

type secretToApply struct {
	name string
	data map[string]string
}

func defaultIntegrationName(kind string) string {
	switch kind {
	case "channel-telegram":
		return "telegram"
	case "channel-discord":
		return "discord"
	case "channel-slack":
		return "slack"
	case "channel-whatsapp":
		return "whatsapp"
	case "github-pat":
		return "github"
	case "websearch-brave":
		return "brave-search"
	case "websearch-tavily":
		return "tavily-search"
	case "auth-password":
		return "gateway-password"
	case "custom-credential":
		return "custom"
	default:
		return strings.TrimPrefix(kind, "channel-")
	}
}

func integrationSecretName(instanceName string, integration integrationRequest) string {
	if integration.SecretName != "" {
		return integration.SecretName
	}
	name := integration.Name
	if name == "" {
		name = defaultIntegrationName(integration.Kind)
	}
	switch integration.Kind {
	case "channel-telegram":
		return "openclaw-" + instanceName + "-telegram-bot-token"
	case "channel-discord":
		return "openclaw-" + instanceName + "-discord-bot-token"
	case "channel-slack":
		return "openclaw-" + instanceName + "-slack-tokens"
	case "github-pat":
		return "openclaw-" + instanceName + "-github-pat"
	case "websearch-brave":
		return "openclaw-" + instanceName + "-brave-search-api-key"
	case "websearch-tavily":
		return "openclaw-" + instanceName + "-tavily-search-api-key"
	case "auth-password":
		return "openclaw-" + instanceName + "-gateway-password"
	default:
		return "openclaw-" + instanceName + "-" + name
	}
}

func integrationSecretKey(integration integrationRequest) string {
	if integration.SecretKey != "" {
		return integration.SecretKey
	}
	switch integration.Kind {
	case "channel-telegram", "channel-discord", "channel-slack":
		return "bot-token"
	case "github-pat":
		return "token"
	case "auth-password":
		return "password"
	default:
		return "api-key"
	}
}

func integrationAppSecretName(instanceName string, integration integrationRequest) string {
	if integration.AppSecretName != "" {
		return integration.AppSecretName
	}
	return integrationSecretName(instanceName, integration)
}

func integrationAppSecretKey(integration integrationRequest) string {
	if integration.AppSecretKey != "" {
		return integration.AppSecretKey
	}
	return "app-token"
}

func integrationSecrets(instanceName string, integration integrationRequest) []secretToApply {
	var secrets []secretToApply
	if integration.SecretValue != "" {
		secrets = append(secrets, secretToApply{
			name: integrationSecretName(instanceName, integration),
			data: map[string]string{
				integrationSecretKey(integration): integration.SecretValue,
			},
		})
	}
	if integration.AppSecretValue != "" {
		appName := integrationAppSecretName(instanceName, integration)
		appKey := integrationAppSecretKey(integration)
		for i := range secrets {
			if secrets[i].name == appName {
				secrets[i].data[appKey] = integration.AppSecretValue
				return secrets
			}
		}
		secrets = append(secrets, secretToApply{
			name: appName,
			data: map[string]string{appKey: integration.AppSecretValue},
		})
	}
	return secrets
}

func applyIntegrationsToSpec(credentials []any, req provisionRequest) ([]any, map[string]any, map[string]any, map[string]any, error) {
	var auth map[string]any
	var webSearch map[string]any
	var repoAccess map[string]any
	credentials = pruneRemovedDeployerCredentials(credentials, req.Name, req.RemovedIntegrations)
	for _, integration := range req.Integrations {
		switch integration.Kind {
		case "channel-telegram", "channel-discord", "channel-slack", "channel-whatsapp":
			credential, err := channelCredential(req.Name, integration)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			credentials = upsertCredentialMap(credentials, credential)
		case "github-pat":
			credentials = pruneRemovedDeployerCredentials(credentials, req.Name, []integrationRequest{integration})
			repoAccess = githubRepoAccess(req.Name, integration)
		case "custom-credential":
			credential, err := customCredential(req.Name, integration)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			credentials = upsertCredentialMap(credentials, credential)
		case "websearch-brave", "websearch-tavily", "websearch-duckduckgo", "websearch-gemini":
			webSearch = webSearchSpec(req.Name, integration)
		case "auth-password":
			auth = map[string]any{
				"mode": "password",
				"passwordSecretRef": map[string]any{
					"name": integrationSecretName(req.Name, integration),
					"key":  integrationSecretKey(integration),
				},
			}
		case "":
			continue
		default:
			return nil, nil, nil, nil, fmt.Errorf("unsupported integration kind %q", integration.Kind)
		}
	}
	return credentials, auth, webSearch, repoAccess, nil
}

func pruneRemovedDeployerCredentials(credentials []any, instanceName string, removedIntegrations []integrationRequest) []any {
	removed := integrationCredentialNames(removedIntegrations)
	next := make([]any, 0, len(credentials))
	for _, credential := range credentials {
		credentialMap, ok := credential.(map[string]any)
		if !ok {
			next = append(next, credential)
			continue
		}
		if isRemovedDeployerCredential(instanceName, credentialMap, removed) {
			continue
		}
		next = append(next, credentialMap)
	}
	return next
}

func integrationCredentialNames(integrations []integrationRequest) map[string]bool {
	names := map[string]bool{}
	for _, integration := range integrations {
		switch integration.Kind {
		case "channel-telegram", "channel-discord", "channel-slack", "channel-whatsapp", "github-pat":
			name := integration.Name
			if name == "" {
				name = defaultIntegrationName(integration.Kind)
			}
			names[name] = true
		case "custom-credential":
			if integration.Name != "" {
				names[integration.Name] = true
			}
		}
	}
	return names
}

func isRemovedDeployerCredential(instanceName string, credential map[string]any, removed map[string]bool) bool {
	name, _ := credential["name"].(string)
	if name == "" || !removed[name] {
		return false
	}
	channel, _ := credential["channel"].(string)
	if isDefaultDeployerChannelCredential(name, channel) {
		return true
	}
	if isDefaultDeployerGitHubCredential(name, credential) {
		return true
	}
	return hasSecretRefName(credential, "openclaw-"+instanceName+"-"+name)
}

func shouldPreserveAuth(instanceName string, auth map[string]any, removedIntegrations []integrationRequest) bool {
	return !hasRemovedIntegration(removedIntegrations, "auth-password") || !isDeployerManagedAuth(instanceName, auth)
}

func shouldPreserveWebSearch(instanceName string, webSearch map[string]any, removedIntegrations []integrationRequest) bool {
	provider, _ := webSearch["provider"].(string)
	return !hasRemovedIntegration(removedIntegrations, "websearch-"+provider) || !isDeployerManagedWebSearch(instanceName, webSearch)
}

func shouldPreserveRepoAccess(instanceName string, repoAccess map[string]any, removedIntegrations []integrationRequest) bool {
	if !hasRemovedIntegration(removedIntegrations, "github-pat") {
		return true
	}
	return !isDeployerManagedGitHubRepoAccess(instanceName, repoAccess, removedIntegrations)
}

func hasRemovedIntegration(integrations []integrationRequest, kind string) bool {
	for _, integration := range integrations {
		if integration.Kind == kind {
			return true
		}
	}
	return false
}

func isDefaultDeployerChannelCredential(name, channel string) bool {
	switch channel {
	case "telegram", "discord", "slack", "whatsapp":
		return name == defaultIntegrationName("channel-"+channel)
	default:
		return false
	}
}

func isDefaultDeployerGitHubCredential(name string, credential map[string]any) bool {
	if name != defaultIntegrationName("github-pat") {
		return false
	}
	credentialType, _ := credential["type"].(string)
	domain, _ := credential["domain"].(string)
	return credentialType == "bearer" && domain == "api.github.com"
}

func hasSecretRefName(credential map[string]any, secretName string) bool {
	secretRefs, _, _ := nestedSlice(credential, "secretRef")
	for _, ref := range secretRefs {
		refMap, ok := ref.(map[string]any)
		if !ok {
			continue
		}
		name, _ := refMap["name"].(string)
		if name == secretName {
			return true
		}
	}
	return false
}

func isDeployerManagedAuth(instanceName string, auth map[string]any) bool {
	mode, _ := auth["mode"].(string)
	if mode != "password" {
		return false
	}
	secretName, _, _ := nestedString(auth, "passwordSecretRef", "name")
	return secretName == integrationSecretName(instanceName, integrationRequest{Kind: "auth-password"})
}

func isDeployerManagedWebSearch(instanceName string, webSearch map[string]any) bool {
	provider, _ := webSearch["provider"].(string)
	switch provider {
	case "brave", "tavily":
		secretName, _, _ := nestedString(webSearch, "secretRef", "name")
		return secretName == integrationSecretName(instanceName, integrationRequest{Kind: "websearch-" + provider})
	default:
		return false
	}
}

func isDeployerManagedGitHubRepoAccess(instanceName string, repoAccess map[string]any, removedIntegrations []integrationRequest) bool {
	secretName, _, _ := nestedString(repoAccess, "github", "secretRef", "name")
	if secretName == "" {
		return false
	}
	for _, integration := range removedIntegrations {
		if integration.Kind == "github-pat" && secretName == integrationSecretName(instanceName, integration) {
			return true
		}
	}
	return false
}

func githubRepoAccess(instanceName string, integration integrationRequest) map[string]any {
	github := map[string]any{
		"secretRef": map[string]any{
			"name": integrationSecretName(instanceName, integration),
			"key":  integrationSecretKey(integration),
		},
		"enableApiProxy": true,
		"enableGitHttps": true,
	}
	if integration.ExposeEnv {
		github["exposeEnv"] = true
	}
	return map[string]any{
		"github": github,
	}
}

func channelCredential(instanceName string, integration integrationRequest) (map[string]any, error) {
	channel := strings.TrimPrefix(integration.Kind, "channel-")
	name := integration.Name
	if name == "" {
		name = channel
	}
	credential := map[string]any{
		"name":    name,
		"channel": channel,
	}
	if channel != "whatsapp" {
		refs := []map[string]string{{
			"name": integrationSecretName(instanceName, integration),
			"key":  integrationSecretKey(integration),
		}}
		if channel == "slack" {
			refs[0]["role"] = "botToken"
			refs = append(refs, map[string]string{
				"name": integrationAppSecretName(instanceName, integration),
				"key":  integrationAppSecretKey(integration),
				"role": "appToken",
			})
		}
		credential["secretRef"] = refs
	}
	if integration.ChannelConfig != "" {
		var config map[string]any
		if err := json.Unmarshal([]byte(integration.ChannelConfig), &config); err != nil {
			return nil, fmt.Errorf("integration %q channelConfig is invalid JSON: %w", name, err)
		}
		credential["channelConfig"] = config
	}
	return credential, nil
}

func customCredential(instanceName string, integration integrationRequest) (map[string]any, error) {
	name := integration.Name
	if name == "" {
		return nil, errors.New("custom credential name is required")
	}
	credential := map[string]any{"name": name}
	if integration.CredentialType != "" {
		credential["type"] = integration.CredentialType
	}
	if integration.Provider != "" {
		credential["provider"] = integration.Provider
	}
	if integration.Domain != "" {
		credential["domain"] = integration.Domain
	}
	if integration.SecretName != "" || integration.SecretValue != "" {
		credential["secretRef"] = []map[string]string{{
			"name": integrationSecretName(instanceName, integration),
			"key":  integrationSecretKey(integration),
		}}
	}
	if integration.Header != "" || integration.ValuePrefix != "" {
		apiKey := map[string]string{}
		if integration.Header != "" {
			apiKey["header"] = integration.Header
		}
		if integration.ValuePrefix != "" {
			apiKey["valuePrefix"] = integration.ValuePrefix
		}
		credential["apiKey"] = apiKey
	}
	if integration.PathPrefix != "" {
		credential["pathToken"] = map[string]string{"prefix": integration.PathPrefix}
	}
	if integration.GCPProject != "" || integration.GCPLocation != "" {
		credential["gcp"] = map[string]string{"project": integration.GCPProject, "location": integration.GCPLocation}
	}
	if integration.OAuthClientID != "" || integration.OAuthTokenURL != "" || integration.OAuthScopes != "" {
		oauth := map[string]any{"clientID": integration.OAuthClientID, "tokenURL": integration.OAuthTokenURL}
		if integration.OAuthScopes != "" {
			oauth["scopes"] = splitCSV(integration.OAuthScopes)
		}
		credential["oauth2"] = oauth
	}
	return credential, nil
}

func webSearchSpec(instanceName string, integration integrationRequest) map[string]any {
	provider := strings.TrimPrefix(integration.Kind, "websearch-")
	spec := map[string]any{"provider": provider}
	if provider == "brave" || provider == "tavily" {
		spec["secretRef"] = map[string]any{
			"name": integrationSecretName(instanceName, integration),
			"key":  integrationSecretKey(integration),
		}
	}
	return spec
}

func upsertCredentialMap(credentials []any, credential map[string]any) []any {
	name, _ := credential["name"].(string)
	next := make([]any, 0, len(credentials)+1)
	replaced := false
	for _, existing := range credentials {
		existingMap, ok := existing.(map[string]any)
		if !ok {
			continue
		}
		existingName, _ := existingMap["name"].(string)
		if existingName == name {
			next = append(next, credential)
			replaced = true
			continue
		}
		next = append(next, existingMap)
	}
	if !replaced {
		next = append(next, credential)
	}
	return next
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func applyAgentConfig(raw map[string]any, agentName, model string) map[string]any {
	config := cloneMap(raw)
	agents := ensureMap(config, "agents")
	defaults := ensureMap(agents, "defaults")
	if model != "" {
		defaults["model"] = map[string]any{"primary": model}
		models := ensureMap(defaults, "models")
		models[model] = map[string]any{"alias": model}
	} else {
		// Blank model means "use the provider default": drop any override this
		// deployer previously set so a stale model does not linger.
		delete(defaults, "model")
		delete(defaults, "models")
	}

	defaultAgent := map[string]any{
		"id":        "default",
		"name":      agentName,
		"identity":  map[string]string{"name": agentName},
		"workspace": "~/.openclaw/workspace",
	}
	if model != "" {
		defaultAgent["model"] = map[string]any{"primary": model}
	}
	existing, _ := agents["list"].([]any)
	list := append([]any(nil), existing...)
	for i, item := range list {
		agent, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := agent["id"].(string)
		name, _ := agent["name"].(string)
		if id == "default" || name == agentName {
			next := cloneMap(agent)
			for key, value := range defaultAgent {
				next[key] = value
			}
			if model == "" {
				delete(next, "model")
			}
			list[i] = next
			agents["list"] = list
			return config
		}
	}
	agents["list"] = append(list, defaultAgent)
	return config
}

func (s *server) restartDeployments(ctx context.Context, identity userIdentity, namespace, name string) error {
	restartedAt := time.Now().UTC().Format(time.RFC3339)
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]string{
						"openclaw-deployer.redhat.com/restartedAt": restartedAt,
					},
				},
			},
		},
	}
	for _, deployment := range []string{name, name + "-proxy"} {
		if err := s.mergePatch(ctx, identity, apiPath("apis/apps/v1/namespaces", namespace, "deployments", deployment), patch); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) deleteManagedAgentFiles(ctx context.Context, identity userIdentity, namespace, name string) error {
	configMapName := agentFilesConfigMapName(name)
	var configMap map[string]any
	configMapPath := apiPath("api/v1/namespaces", namespace, "configmaps", configMapName)
	if err := s.kubeJSON(ctx, identity, http.MethodGet, configMapPath, nil, &configMap); err != nil {
		return err
	}
	managedBy, _, _ := nestedString(configMap, "metadata", "labels", managedByLabel)
	instance, _, _ := nestedString(configMap, "metadata", "labels", instanceLabel)
	if managedBy != managedByValue || instance != name {
		return nil
	}
	return s.delete(ctx, identity, configMapPath)
}

func (s *server) deleteManagedSecret(ctx context.Context, identity userIdentity, namespace, name, secretName string) error {
	var secret map[string]any
	secretPath := apiPath("api/v1/namespaces", namespace, "secrets", secretName)
	if err := s.kubeJSON(ctx, identity, http.MethodGet, secretPath, nil, &secret); err != nil {
		return err
	}
	managedBy, _, _ := nestedString(secret, "metadata", "labels", managedByLabel)
	instance, _, _ := nestedString(secret, "metadata", "labels", instanceLabel)
	if managedBy != managedByValue || instance != name {
		return nil
	}
	return s.delete(ctx, identity, secretPath)
}

func (s *server) managedSecretNames(ctx context.Context, identity userIdentity, namespace, name string) ([]string, error) {
	selector := managedByLabel + "=" + managedByValue + "," + instanceLabel + "=" + name
	path := apiPath("api/v1/namespaces", namespace, "secrets") + "?labelSelector=" + url.QueryEscape(selector)
	var list map[string]any
	if err := s.kubeJSON(ctx, identity, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	items, _, _ := nestedSlice(list, "items")
	names := []string{}
	for _, item := range items {
		secret, ok := item.(map[string]any)
		if !ok {
			continue
		}
		secretName, _, _ := nestedString(secret, "metadata", "name")
		if secretName != "" {
			names = appendUnique(names, secretName)
		}
	}
	return names, nil
}

func readyCondition(claw map[string]any) (bool, string, string) {
	conditions, _, _ := nestedSlice(claw, "status", "conditions")
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok || condition["type"] != "Ready" {
			continue
		}
		reason, _ := condition["reason"].(string)
		message, _ := condition["message"].(string)
		status, _ := condition["status"].(string)
		return status == "True", reason, message
	}
	return false, "", "Waiting for status"
}

func credentialProviders(claw map[string]any) []string {
	credentials, _, _ := nestedSlice(claw, "spec", "credentials")
	providers := make([]string, 0, len(credentials))
	for _, credential := range credentials {
		credentialMap, ok := credential.(map[string]any)
		if !ok {
			continue
		}
		provider, _ := credentialMap["provider"].(string)
		if provider != "" {
			providers = append(providers, provider)
		}
	}
	return providers
}

func credentialRefs(claw map[string]any) []credentialRefResponse {
	credentials, _, _ := nestedSlice(claw, "spec", "credentials")
	refs := []credentialRefResponse{}
	for _, credential := range credentials {
		credentialMap, ok := credential.(map[string]any)
		if !ok {
			continue
		}
		provider, _ := credentialMap["provider"].(string)
		if provider == "" {
			continue
		}
		credentialName, _ := credentialMap["name"].(string)
		secretRefs, _, _ := nestedSlice(credentialMap, "secretRef")
		for _, ref := range secretRefs {
			refMap, ok := ref.(map[string]any)
			if !ok {
				continue
			}
			name, _ := refMap["name"].(string)
			key, _ := refMap["key"].(string)
			if name == "" {
				continue
			}
			refs = append(refs, credentialRefResponse{
				Credential: credentialName,
				Provider:   provider,
				Name:       name,
				Key:        key,
			})
		}
	}
	return refs
}

func credentialSecretNames(claw map[string]any) []string {
	credentials, _, _ := nestedSlice(claw, "spec", "credentials")
	secretNames := []string{}
	for _, credential := range credentials {
		credentialMap, ok := credential.(map[string]any)
		if !ok {
			continue
		}
		secretRefs, _, _ := nestedSlice(credentialMap, "secretRef")
		for _, ref := range secretRefs {
			refMap, ok := ref.(map[string]any)
			if !ok {
				continue
			}
			name, _ := refMap["name"].(string)
			if name != "" {
				secretNames = appendUnique(secretNames, name)
			}
		}
	}
	for _, fields := range [][]string{
		{"spec", "webSearch", "secretRef", "name"},
		{"spec", "auth", "passwordSecretRef", "name"},
		{"spec", "agentFiles", "git", "secretRef", "name"},
		{"spec", "repoAccess", "github", "secretRef", "name"},
	} {
		name, _, _ := nestedString(claw, fields...)
		if name != "" {
			secretNames = appendUnique(secretNames, name)
		}
	}
	return secretNames
}

func firstAgentName(claw map[string]any) string {
	agents, _, _ := nestedSlice(claw, "spec", "config", "raw", "agents", "list")
	if len(agents) == 0 {
		return ""
	}
	first, ok := agents[0].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := first["name"].(string)
	return name
}
