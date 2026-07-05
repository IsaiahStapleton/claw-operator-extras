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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
)

var (
	namespaceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dnsCharRE   = regexp.MustCompile(`[^a-z0-9-]+`)
	providers   = map[string]providerOption{
		"anthropic": {
			CredentialName:     "anthropic",
			CredentialProvider: "anthropic",
			ModelProvider:      "anthropic",
			SecretKey:          apiKeySecretKey,
		},
		"anthropic-vertex": {
			CredentialName:     "anthropic-vertex",
			CredentialProvider: "anthropic",
			CredentialType:     "gcp",
			ModelProvider:      "anthropic-vertex",
			SecretKey:          gcpSecretKey,
			RequiresGCP:        true,
		},
		"google": {
			CredentialName:     "google",
			CredentialProvider: "google",
			ModelProvider:      "google",
			SecretKey:          apiKeySecretKey,
		},
		"google-vertex": {
			CredentialName:     "google-vertex",
			CredentialProvider: "google",
			CredentialType:     "gcp",
			ModelProvider:      "google",
			SecretKey:          gcpSecretKey,
			RequiresGCP:        true,
		},
		"openai": {
			CredentialName:     "openai",
			CredentialProvider: "openai",
			ModelProvider:      "openai",
			SecretKey:          apiKeySecretKey,
		},
		"openrouter": {
			CredentialName:     "openrouter",
			CredentialProvider: "openrouter",
			ModelProvider:      "openrouter",
			SecretKey:          apiKeySecretKey,
		},
		"xai": {
			CredentialName:     "xai",
			CredentialProvider: "xai",
			ModelProvider:      "xai",
			SecretKey:          apiKeySecretKey,
		},
	}
)

type providerOption struct {
	CredentialName     string
	CredentialProvider string
	CredentialType     string
	ModelProvider      string
	SecretKey          string
	RequiresGCP        bool
}

func currentUser(r *http.Request) (string, error) {
	for _, header := range []string{
		"X-Forwarded-User",
		"X-Auth-Request-User",
		"X-Forwarded-Preferred-Username",
		"X-Forwarded-Email",
	} {
		if user := strings.TrimSpace(r.Header.Get(header)); user != "" {
			return user, nil
		}
	}
	if user := strings.TrimSpace(os.Getenv("DEVELOPER_USERNAME")); user != "" {
		return user, nil
	}
	return "", errors.New("OpenShift username was not forwarded to the deployer")
}

func currentIdentity(r *http.Request) (userIdentity, error) {
	user, err := currentUser(r)
	if err != nil {
		return userIdentity{}, err
	}
	return userIdentity{
		Name:   user,
		Groups: impersonationGroups(r),
	}, nil
}

func impersonationGroups(r *http.Request) []string {
	groups := []string{}
	for _, header := range []string{"X-Forwarded-Groups", "X-Auth-Request-Groups"} {
		for _, value := range r.Header.Values(header) {
			for _, group := range strings.Split(value, ",") {
				group = strings.TrimSpace(group)
				if group != "" {
					groups = appendUnique(groups, group)
				}
			}
		}
	}
	groups = appendUnique(groups, "system:authenticated")
	groups = appendUnique(groups, "system:authenticated:oauth")
	return groups
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		if child, ok := value.(map[string]any); ok {
			dst[key] = cloneMap(child)
			continue
		}
		dst[key] = value
	}
	return dst
}

func ensureMap(parent map[string]any, key string) map[string]any {
	child, ok := parent[key].(map[string]any)
	if !ok {
		child = map[string]any{}
		parent[key] = child
	}
	return child
}

func validateNamespace(namespace string) error {
	return validateResourceName(namespace, "namespace")
}

func validateResourceName(name, field string) error {
	if name == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(name) > 63 || !namespaceRE.MatchString(name) {
		return fmt.Errorf("%s must be a valid Kubernetes resource name", field)
	}
	return nil
}

func validateGCPServiceAccountJSON(value string) error {
	var payload struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return errors.New(`valid JSON with type "service_account" or "authorized_user" is required`)
	}
	if payload.Type != "service_account" && payload.Type != "authorized_user" {
		return errors.New(`valid JSON with type "service_account" or "authorized_user" is required`)
	}
	return nil
}

// validateFilesystemSource normalizes and validates the agentFiles seeding
// fields on req. Seeding is only honored by the operator for user-managed
// Claws, so a source on an operator-managed request is rejected.
func validateFilesystemSource(req *provisionRequest) error {
	switch req.FilesystemSource {
	case "", "none":
		req.FilesystemSource = ""
		return nil
	case "git":
		if req.Management != "user" {
			return errors.New("filesystem seeding requires user-managed config")
		}
		return validateGitURL(req.GitURL)
	case "configmap":
		if req.Management != "user" {
			return errors.New("filesystem seeding requires user-managed config")
		}
		return validateResourceName(req.ConfigMapName, "ConfigMap name")
	default:
		return errors.New(`filesystem source must be "git" or "configmap"`)
	}
}

func normalizeIntegrations(integrations []integrationRequest) {
	for i := range integrations {
		integrations[i].Kind = strings.ToLower(strings.TrimSpace(integrations[i].Kind))
		integrations[i].Name = strings.TrimSpace(integrations[i].Name)
		integrations[i].SecretName = strings.TrimSpace(integrations[i].SecretName)
		integrations[i].SecretKey = strings.TrimSpace(integrations[i].SecretKey)
		integrations[i].SecretValue = strings.TrimSpace(integrations[i].SecretValue)
		integrations[i].AppSecretName = strings.TrimSpace(integrations[i].AppSecretName)
		integrations[i].AppSecretKey = strings.TrimSpace(integrations[i].AppSecretKey)
		integrations[i].AppSecretValue = strings.TrimSpace(integrations[i].AppSecretValue)
		integrations[i].CredentialType = strings.TrimSpace(integrations[i].CredentialType)
		integrations[i].Provider = strings.TrimSpace(integrations[i].Provider)
		integrations[i].Channel = strings.TrimSpace(integrations[i].Channel)
		integrations[i].Domain = strings.TrimSpace(integrations[i].Domain)
		integrations[i].Header = strings.TrimSpace(integrations[i].Header)
		integrations[i].ValuePrefix = strings.TrimSpace(integrations[i].ValuePrefix)
		integrations[i].PathPrefix = strings.TrimSpace(integrations[i].PathPrefix)
		integrations[i].GCPProject = strings.TrimSpace(integrations[i].GCPProject)
		integrations[i].GCPLocation = strings.TrimSpace(integrations[i].GCPLocation)
		integrations[i].OAuthClientID = strings.TrimSpace(integrations[i].OAuthClientID)
		integrations[i].OAuthTokenURL = strings.TrimSpace(integrations[i].OAuthTokenURL)
		integrations[i].OAuthScopes = strings.TrimSpace(integrations[i].OAuthScopes)
		integrations[i].ChannelConfig = strings.TrimSpace(integrations[i].ChannelConfig)
	}
}

func normalizeModelProviders(modelProviders []modelProviderRequest) {
	for i := range modelProviders {
		modelProviders[i].Provider = strings.ToLower(strings.TrimSpace(modelProviders[i].Provider))
		modelProviders[i].Model = strings.TrimSpace(modelProviders[i].Model)
		if modelProviders[i].Model != "" {
			modelProviders[i].Model = normalizeModelRef(modelProviders[i].Provider, modelProviders[i].Model)
		}
		modelProviders[i].APIKey = strings.TrimSpace(modelProviders[i].APIKey)
		modelProviders[i].SecretName = strings.TrimSpace(modelProviders[i].SecretName)
		modelProviders[i].SecretKey = strings.TrimSpace(modelProviders[i].SecretKey)
		modelProviders[i].GCPProject = strings.TrimSpace(modelProviders[i].GCPProject)
		modelProviders[i].GCPLocation = strings.TrimSpace(modelProviders[i].GCPLocation)
	}
}

func validateModelProviders(modelProviders []modelProviderRequest) error {
	for _, modelProvider := range modelProviders {
		if modelProvider.Provider == "" {
			continue
		}
		option, ok := providers[modelProvider.Provider]
		if !ok {
			return fmt.Errorf("unsupported provider %q", modelProvider.Provider)
		}
		if modelProvider.SecretName != "" {
			if err := validateResourceName(modelProvider.SecretName, "Secret name"); err != nil {
				return err
			}
		}
		hasCredentialInput := modelProvider.APIKey != "" || modelProvider.SecretName != ""
		if option.RequiresGCP && hasCredentialInput && (modelProvider.GCPProject == "" || modelProvider.GCPLocation == "") {
			return fmt.Errorf("GCP project and location are required for provider %q", modelProvider.Provider)
		}
		if option.RequiresGCP && modelProvider.APIKey != "" {
			if err := validateGCPServiceAccountJSON(modelProvider.APIKey); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRemovedModelProviders(modelProviders []modelProviderRequest) error {
	for _, modelProvider := range modelProviders {
		if modelProvider.Provider == "" {
			continue
		}
		if _, ok := providers[modelProvider.Provider]; !ok {
			return fmt.Errorf("unsupported provider %q", modelProvider.Provider)
		}
		if modelProvider.SecretName != "" {
			if err := validateResourceName(modelProvider.SecretName, "Secret name"); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasModelProviderCredentialInput(modelProviders []modelProviderRequest) bool {
	for _, modelProvider := range modelProviders {
		if modelProvider.APIKey != "" || modelProvider.SecretName != "" {
			return true
		}
	}
	return false
}

func validateProvisionIntegrations(req provisionRequest) error {
	if req.GitSecretName != "" {
		if err := validateResourceName(req.GitSecretName, "Git Secret name"); err != nil {
			return err
		}
	}
	if (req.GitUsername != "" || req.GitPassword != "") && (req.GitUsername == "" || req.GitPassword == "") {
		return errors.New("git username and password are both required when creating a Git Secret")
	}
	for _, integration := range req.Integrations {
		if integration.Kind == "" {
			continue
		}
		if integration.Name != "" {
			if err := validateResourceName(integration.Name, "integration name"); err != nil {
				return err
			}
		}
		if integration.SecretName != "" {
			if err := validateResourceName(integration.SecretName, "Secret name"); err != nil {
				return err
			}
		}
		if integration.AppSecretName != "" {
			if err := validateResourceName(integration.AppSecretName, "app Secret name"); err != nil {
				return err
			}
		}
		switch integration.Kind {
		case "channel-telegram", "channel-discord", "github-pat", "websearch-brave", "websearch-tavily", "auth-password":
			if integration.SecretName == "" && integration.SecretValue == "" {
				return fmt.Errorf("%s requires a pasted value or existing Secret name", integration.Kind)
			}
		case "channel-slack":
			if integration.SecretName == "" && integration.SecretValue == "" {
				return errors.New("channel-slack requires a bot token value or existing Secret name")
			}
			if integration.AppSecretName == "" && integration.AppSecretValue == "" {
				return errors.New("channel-slack requires an app token value or existing app Secret name")
			}
		case "channel-whatsapp", "websearch-duckduckgo", "websearch-gemini":
		case "custom-credential":
			if integration.Name == "" {
				return errors.New("custom credential name is required")
			}
		default:
			return fmt.Errorf("unsupported integration kind %q", integration.Kind)
		}
	}
	return nil
}

func validateGitURL(raw string) error {
	if raw == "" {
		return errors.New("git URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("git URL must be an http(s) URL")
	}
	return nil
}

func normalizeConfigManagement(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultManagement, nil
	}
	if value != "operator" && value != "user" {
		return "", errors.New(`config management must be "operator" or "user"`)
	}
	return value, nil
}

func normalizeModelRef(provider, model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	modelProvider := modelProviderFor(provider)
	if strings.Contains(model, "/") {
		if provider == "openrouter" && !strings.HasPrefix(model, "openrouter/") {
			return "openrouter/" + model
		}
		if provider == "anthropic-vertex" && strings.HasPrefix(model, "anthropic/") {
			return "anthropic-vertex/" + strings.TrimPrefix(model, "anthropic/")
		}
		return model
	}
	if provider == "openrouter" {
		return "openrouter/" + model
	}
	return modelProvider + "/" + model
}

func modelProviderFor(provider string) string {
	if option, ok := providers[provider]; ok && option.ModelProvider != "" {
		return option.ModelProvider
	}
	return provider
}

func secretName(name, provider string) string {
	return "openclaw-" + name + "-" + provider + "-api-key"
}

func secretNameForRequest(req provisionRequest) string {
	option := providers[req.Provider]
	if option.RequiresGCP {
		return "openclaw-" + req.Name + "-" + option.CredentialName + "-gcp"
	}
	return secretName(req.Name, option.CredentialName)
}

func credentialSecretName(req provisionRequest) string {
	if req.SecretName != "" {
		return req.SecretName
	}
	return secretNameForRequest(req)
}

func credentialSecretKey(req provisionRequest) string {
	if req.SecretKey != "" {
		return req.SecretKey
	}
	return providers[req.Provider].SecretKey
}

func agentNameFromClawName(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	if len(words) == 0 {
		return "OpenClaw"
	}
	return strings.Join(words, " ")
}

func allowedNamespaceForUser(username, suffix string) string {
	name := strings.ToLower(strings.TrimSpace(username))
	if at := strings.Index(name, "@"); at >= 0 {
		name = name[:at]
	}
	name = dnsCharRE.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if suffix != "" && !strings.HasSuffix(name, suffix) {
		name += suffix
	}
	return name
}

func apiPath(parts ...string) string {
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		for _, subpart := range strings.Split(part, "/") {
			if subpart != "" {
				escaped = append(escaped, url.PathEscape(subpart))
			}
		}
	}
	return "/" + path.Join(escaped...)
}

func nestedString(obj map[string]any, fields ...string) (string, bool, error) {
	v, ok, err := nestedValue(obj, fields...)
	if err != nil || !ok {
		return "", ok, err
	}
	s, ok := v.(string)
	return s, ok, nil
}

func nestedBool(obj map[string]any, fields ...string) (bool, bool, error) {
	v, ok, err := nestedValue(obj, fields...)
	if err != nil || !ok {
		return false, ok, err
	}
	b, ok := v.(bool)
	return b, ok, nil
}

func nestedSlice(obj map[string]any, fields ...string) ([]any, bool, error) {
	v, ok, err := nestedValue(obj, fields...)
	if err != nil || !ok {
		return nil, ok, err
	}
	s, ok := v.([]any)
	return s, ok, nil
}

func nestedMap(obj map[string]any, fields ...string) (map[string]any, bool, error) {
	v, ok, err := nestedValue(obj, fields...)
	if err != nil || !ok {
		return nil, ok, err
	}
	m, ok := v.(map[string]any)
	return m, ok, nil
}

func nestedValue(obj map[string]any, fields ...string) (any, bool, error) {
	var current any = obj
	for _, field := range fields {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("field %q is not an object", field)
		}
		current, ok = m[field]
		if !ok {
			return nil, false, nil
		}
	}
	return current, true, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
