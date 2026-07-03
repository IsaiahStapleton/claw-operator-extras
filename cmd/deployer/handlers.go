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
	"net/http"
	"strings"
)

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, err := currentUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, meResponse{
		User:              user,
		DefaultNamespace:  allowedNamespaceForUser(user, s.namespaceSuffix),
		DefaultManagement: s.defaultConfigManagement(),
		Providers:         []string{"openrouter", "openai", "google", "google-vertex", "anthropic", "anthropic-vertex", "xai"},
	})
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, staticFiles, "static/index.html")
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	identity, err := currentIdentity(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	namespace := r.URL.Query().Get("namespace")
	if err := validateNamespace(namespace); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		name = "instance"
	}
	if err := validateResourceName(name, "Claw name"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	state, err := s.getState(r.Context(), identity, namespace, name)
	if err != nil {
		var apiErr apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			writeJSON(w, http.StatusOK, stateResponse{Namespace: namespace, Name: name, Exists: false})
			return
		}
		writeError(w, statusCodeFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	identity, err := currentIdentity(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	namespaces, err := s.visibleNamespaceNames(r.Context(), identity)
	if err != nil {
		writeError(w, statusCodeFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, namespacesResponse{Namespaces: namespaces})
}

func (s *server) handleClaws(w http.ResponseWriter, r *http.Request) {
	identity, err := currentIdentity(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	namespace := r.URL.Query().Get("namespace")

	var claws []stateResponse
	var listErr error
	if namespace == "" {
		claws, listErr = s.listAllClaws(r.Context(), identity)
	} else {
		if err := validateNamespace(namespace); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		claws, listErr = s.listClaws(r.Context(), identity, namespace)
	}
	if listErr != nil {
		var apiErr apiError
		if errors.As(listErr, &apiErr) && (apiErr.StatusCode == http.StatusNotFound || (namespace != "" && apiErr.StatusCode == http.StatusForbidden)) {
			writeJSON(w, http.StatusOK, listResponse{Claws: []stateResponse{}})
			return
		}
		writeError(w, statusCodeFor(listErr), listErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, listResponse{Claws: claws})
}

func (s *server) handleProvision(w http.ResponseWriter, r *http.Request) {
	identity, err := currentIdentity(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	var req provisionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = "instance"
	}
	req.Model = strings.TrimSpace(req.Model)
	if req.Model != "" {
		req.Model = normalizeModelRef(req.Provider, req.Model)
		req.ConfigureAgent = true
	}
	if strings.TrimSpace(req.Management) == "" {
		req.Management = s.defaultConfigManagement()
	} else {
		req.Management, err = normalizeConfigManagement(req.Management)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.SecretName = strings.TrimSpace(req.SecretName)
	req.SecretKey = strings.TrimSpace(req.SecretKey)
	req.GCPProject = strings.TrimSpace(req.GCPProject)
	req.GCPLocation = strings.TrimSpace(req.GCPLocation)
	if err := validateNamespace(req.Namespace); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateResourceName(req.Name, "Claw name"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.AgentName = agentNameFromClawName(req.Name)
	if _, ok := providers[req.Provider]; !ok {
		writeError(w, http.StatusBadRequest, "unsupported provider")
		return
	}
	provider := providers[req.Provider]
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.SecretName != "" {
		if err := validateResourceName(req.SecretName, "Secret name"); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	hasCredentialInput := req.APIKey != "" || req.SecretName != ""
	if provider.RequiresGCP && hasCredentialInput && (req.GCPProject == "" || req.GCPLocation == "") {
		writeError(w, http.StatusBadRequest, "GCP project and location are required")
		return
	}
	if provider.RequiresGCP && req.APIKey != "" {
		if err := validateGCPServiceAccountJSON(req.APIKey); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	req.FilesystemSource = strings.ToLower(strings.TrimSpace(req.FilesystemSource))
	req.GitURL = strings.TrimSpace(req.GitURL)
	req.GitRef = strings.TrimSpace(req.GitRef)
	req.GitPath = strings.TrimSpace(req.GitPath)
	req.GitSecretName = strings.TrimSpace(req.GitSecretName)
	req.GitUsername = strings.TrimSpace(req.GitUsername)
	req.GitPassword = strings.TrimSpace(req.GitPassword)
	req.ConfigMapName = strings.TrimSpace(req.ConfigMapName)
	req.ConfigMapKey = strings.TrimSpace(req.ConfigMapKey)
	normalizeIntegrations(req.Integrations)
	normalizeIntegrations(req.RemovedIntegrations)
	if err := validateFilesystemSource(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateProvisionIntegrations(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !hasCredentialInput {
		if _, err := s.getState(r.Context(), identity, req.Namespace, req.Name); err != nil {
			var apiErr apiError
			if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				if provider.RequiresGCP {
					writeError(w, http.StatusBadRequest, "GCP service account JSON or Secret name is required")
				} else {
					writeError(w, http.StatusBadRequest, "API key or Secret name is required")
				}
				return
			}
			writeError(w, statusCodeFor(err), err.Error())
			return
		}
	}
	if err := s.ensureProject(r.Context(), identity, req.Namespace); err != nil {
		writeError(w, statusCodeFor(err), "failed to create project: "+err.Error())
		return
	}
	if req.APIKey != "" {
		if err := s.applySecret(r.Context(), identity, req); err != nil {
			writeError(w, statusCodeFor(err), "failed to create provider secret: "+err.Error())
			return
		}
	}
	if err := s.applyIntegrationSecrets(r.Context(), identity, req); err != nil {
		writeError(w, statusCodeFor(err), "failed to create integration secret: "+err.Error())
		return
	}
	if err := s.applyClaw(r.Context(), identity, req); err != nil {
		writeError(w, statusCodeFor(err), "failed to create Claw: "+err.Error())
		return
	}

	state, err := s.getState(r.Context(), identity, req.Namespace, req.Name)
	if err != nil {
		writeError(w, statusCodeFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *server) handleRestart(w http.ResponseWriter, r *http.Request) {
	identity, err := currentIdentity(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	namespace := r.URL.Query().Get("namespace")
	if err := validateNamespace(namespace); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := r.URL.Query().Get("name")
	if err := validateResourceName(name, "Claw name"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.restartDeployments(r.Context(), identity, namespace, name); err != nil {
		writeError(w, statusCodeFor(err), "failed to restart OpenClaw: "+err.Error())
		return
	}
	state, err := s.getState(r.Context(), identity, namespace, name)
	if err != nil {
		writeError(w, statusCodeFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	identity, err := currentIdentity(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	namespace := r.URL.Query().Get("namespace")
	if err := validateNamespace(namespace); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := r.URL.Query().Get("name")
	if err := validateResourceName(name, "Claw name"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	state, err := s.getState(r.Context(), identity, namespace, name)
	secretNames := state.SecretNames
	if err != nil {
		secretNames = managedProviderSecretNames(name)
	}
	if err := s.delete(r.Context(), identity, apiPath("apis/claw.sandbox.redhat.com/v1alpha1/namespaces", namespace, "claws", name)); err != nil {
		writeError(w, statusCodeFor(err), "failed to delete Claw: "+err.Error())
		return
	}
	if len(secretNames) == 0 {
		providers := state.Providers
		if len(providers) == 0 && state.Provider != "" {
			providers = []string{state.Provider}
		}
		for _, provider := range providers {
			secretNames = appendUnique(secretNames, secretName(name, provider))
		}
	}
	if managedSecretNames, err := s.managedSecretNames(r.Context(), identity, namespace, name); err == nil {
		for _, secretName := range managedSecretNames {
			secretNames = appendUnique(secretNames, secretName)
		}
	}
	for _, secretName := range secretNames {
		_ = s.deleteManagedSecret(r.Context(), identity, namespace, name, secretName)
	}
	_ = s.deleteManagedAgentFiles(r.Context(), identity, namespace, name)
	writeJSON(w, http.StatusOK, stateResponse{Exists: false})
}

func managedProviderSecretNames(name string) []string {
	secretNames := []string{}
	for provider := range providers {
		secretNames = appendUnique(secretNames, secretNameForRequest(provisionRequest{Name: name, Provider: provider}))
	}
	return secretNames
}
