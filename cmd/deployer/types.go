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

import "net/http"

const (
	apiKeySecretKey    = "api-key"
	gcpSecretKey       = "sa-key.json"
	fieldManager       = "openclaw-deployer"
	managedByLabel     = "app.kubernetes.io/managed-by"
	managedByValue     = "openclaw-deployer"
	instanceLabel      = "openclaw-deployer.redhat.com/instance"
	providerLabel      = "openclaw-deployer.redhat.com/provider"
	inClusterCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	inClusterTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultListenAddr  = ":8080"
	defaultNSSuffix    = "-claw"
	defaultManagement  = "operator"
)

type server struct {
	apiServer         string
	bearerToken       string
	impersonate       bool
	namespaceSuffix   string
	defaultManagement string
	client            *http.Client
	static            http.Handler
}

type userIdentity struct {
	Name   string
	Groups []string
}

type provisionRequest struct {
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	AgentName      string `json:"agentName"`
	Model          string `json:"model"`
	Provider       string `json:"provider"`
	ConfigureAgent bool   `json:"configureAgent"`
	APIKey         string `json:"apiKey"`
	SecretName     string `json:"secretName"`
	SecretKey      string `json:"secretKey"`
	GCPProject     string `json:"gcpProject"`
	GCPLocation    string `json:"gcpLocation"`
	Management     string `json:"management"`

	// FilesystemSource seeds the Claw filesystem from a Git repository or a
	// ConfigMap. It maps to spec.agentFiles and is only honored by the operator
	// when Management is "user". Empty leaves any existing source unchanged.
	FilesystemSource string `json:"filesystemSource"`
	GitURL           string `json:"gitURL"`
	GitRef           string `json:"gitRef"`
	GitPath          string `json:"gitPath"`
	GitSecretName    string `json:"gitSecretName"`
	GitUsername      string `json:"gitUsername"`
	GitPassword      string `json:"gitPassword"`
	ConfigMapName    string `json:"configMapName"`
	ConfigMapKey     string `json:"configMapKey"`

	Integrations        []integrationRequest `json:"integrations"`
	RemovedIntegrations []integrationRequest `json:"removedIntegrations"`
}

type integrationRequest struct {
	Kind string `json:"kind"`
	Name string `json:"name"`

	SecretName  string `json:"secretName"`
	SecretKey   string `json:"secretKey"`
	SecretValue string `json:"secretValue"`

	AppSecretName  string `json:"appSecretName"`
	AppSecretKey   string `json:"appSecretKey"`
	AppSecretValue string `json:"appSecretValue"`

	CredentialType string `json:"credentialType"`
	Provider       string `json:"provider"`
	Channel        string `json:"channel"`
	Domain         string `json:"domain"`
	Header         string `json:"header"`
	ValuePrefix    string `json:"valuePrefix"`
	PathPrefix     string `json:"pathPrefix"`
	GCPProject     string `json:"gcpProject"`
	GCPLocation    string `json:"gcpLocation"`
	OAuthClientID  string `json:"oauthClientID"`
	OAuthTokenURL  string `json:"oauthTokenURL"`
	OAuthScopes    string `json:"oauthScopes"`
	ChannelConfig  string `json:"channelConfig"`
	ExposeEnv      bool   `json:"exposeEnv"`
}

type meResponse struct {
	User              string   `json:"user,omitempty"`
	DefaultNamespace  string   `json:"defaultNamespace,omitempty"`
	DefaultManagement string   `json:"defaultManagement"`
	Providers         []string `json:"providers"`
}

type stateResponse struct {
	Namespace      string                  `json:"namespace,omitempty"`
	Name           string                  `json:"name,omitempty"`
	Exists         bool                    `json:"exists"`
	Ready          bool                    `json:"ready"`
	Reason         string                  `json:"reason,omitempty"`
	Message        string                  `json:"message,omitempty"`
	GatewayURL     string                  `json:"gatewayURL,omitempty"`
	Provider       string                  `json:"provider,omitempty"`
	Providers      []string                `json:"providers,omitempty"`
	Model          string                  `json:"model,omitempty"`
	AgentName      string                  `json:"agentName,omitempty"`
	Management     string                  `json:"management,omitempty"`
	CreatedAt      string                  `json:"createdAt,omitempty"`
	SecretNames    []string                `json:"secretNames,omitempty"`
	CredentialRefs []credentialRefResponse `json:"credentialRefs,omitempty"`
}

type credentialRefResponse struct {
	Credential string `json:"credential,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Name       string `json:"name,omitempty"`
	Key        string `json:"key,omitempty"`
}

type listResponse struct {
	Claws []stateResponse `json:"claws"`
}

type namespacesResponse struct {
	Namespaces []string `json:"namespaces"`
}
