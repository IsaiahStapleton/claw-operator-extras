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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestValidateNamespace(t *testing.T) {
	tests := map[string]struct {
		namespace string
		wantErr   bool
	}{
		"minimum":    {namespace: "a"},
		"user name":  {namespace: "sallyom-claw"},
		"with digit": {namespace: "user123"},
		"empty":      {namespace: "", wantErr: true},
		"uppercase":  {namespace: "Upper", wantErr: true},
		"bad prefix": {namespace: "-bad", wantErr: true},
		"bad suffix": {namespace: "bad-", wantErr: true},
		"underscore": {namespace: "bad_namespace", wantErr: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateNamespace(tt.namespace)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestKubeJSONImpersonatesUser(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			assert.Equal(t, "https://kubernetes.example.test/api/v1/namespaces/sallyom-claw", r.URL.String())
			assert.Equal(t, "Bearer service-account-token", r.Header.Get("Authorization"))
			assert.Equal(t, "sallyom", r.Header.Get("Impersonate-User"))
			assert.Equal(t, []string{"system:authenticated", "team-a"}, r.Header.Values("Impersonate-Group"))
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}),
	}

	s := &server{
		apiServer:   "https://kubernetes.example.test",
		bearerToken: "service-account-token",
		impersonate: true,
		client:      client,
	}
	identity := userIdentity{Name: "sallyom", Groups: []string{"system:authenticated", "team-a"}}
	require.NoError(t, s.kubeJSON(context.Background(), identity, http.MethodGet, "/api/v1/namespaces/sallyom-claw", nil, nil))
}

func TestKubeJSONReadsLargeResponses(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"items":["` + strings.Repeat("x", 70*1024) + `"]}`)),
			}, nil
		}),
	}
	s := &server{
		apiServer:   "https://kubernetes.example.test",
		bearerToken: "service-account-token",
		client:      client,
	}
	var out map[string]any
	require.NoError(t, s.kubeJSON(context.Background(), userIdentity{}, http.MethodGet, "/api/v1/items", nil, &out))
	items, _, _ := nestedSlice(out, "items")
	require.Len(t, items, 1)
	assert.Len(t, items[0], 70*1024)
}

func TestCurrentIdentity(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "/", nil)
	require.NoError(t, err)
	req.Header.Set("X-Forwarded-User", "sallyom")
	req.Header.Set("X-Forwarded-Groups", "team-a, team-b")
	identity, err := currentIdentity(req)
	require.NoError(t, err)
	assert.Equal(t, "sallyom", identity.Name)
	assert.Equal(t, []string{"team-a", "team-b", "system:authenticated", "system:authenticated:oauth"}, identity.Groups)
}

func TestCurrentUser(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "/", nil)
	require.NoError(t, err)
	req.Header.Set("X-Forwarded-User", "sallyom")
	user, err := currentUser(req)
	require.NoError(t, err)
	assert.Equal(t, "sallyom", user)
}

func TestAllowedNamespaceForUser(t *testing.T) {
	tests := map[string]string{
		"sallyom":             "sallyom-claw",
		"octo-claw":           "octo-claw",
		"Sally.OM@example.io": "sally-om-claw",
	}
	for username, expected := range tests {
		t.Run(username, func(t *testing.T) {
			assert.Equal(t, expected, allowedNamespaceForUser(username, defaultNSSuffix))
		})
	}
}

func TestUpsertCredentialAppendsAndReplacesProvider(t *testing.T) {
	credentials := []any{
		map[string]any{
			"name":     "openai",
			"provider": "openai",
			"secretRef": []any{
				map[string]any{"name": "openclaw-instance-openai-api-key", "key": apiKeySecretKey},
			},
		},
	}
	credentials = upsertCredential(credentials, "instance", "openrouter")
	require.Len(t, credentials, 2)
	credentials = upsertCredential(credentials, "instance", "openai")
	require.Len(t, credentials, 2)
	first := credentials[0].(map[string]any)
	assert.Equal(t, "openai", first["provider"])
}

func TestCurrentClawSpecReturnsNonNotFoundErrors(t *testing.T) {
	tests := map[string]struct {
		status  int
		wantErr bool
	}{
		"not found starts empty":  {status: http.StatusNotFound},
		"forbidden returns error": {status: http.StatusForbidden, wantErr: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{
				Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: tt.status,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"message":"nope"}`)),
					}, nil
				}),
			}
			s := &server{
				apiServer:   "https://kubernetes.example.test",
				bearerToken: "service-account-token",
				client:      client,
			}
			credentials, raw, _, err := s.currentClawSpec(context.Background(), userIdentity{}, "sallyom-claw", "instance")
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Nil(t, credentials)
			assert.Empty(t, raw)
		})
	}
}

func TestHandleDeleteRemovesAllManagedProviderSecrets(t *testing.T) {
	deleted := map[string]bool{}
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := "{}"
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/claws/instance") {
				body = `{
					"metadata": {"name": "instance"},
					"spec": {
						"credentials": [
							{"name": "openai", "provider": "openai"},
							{"name": "openrouter", "provider": "openrouter"}
						]
					}
				}`
			}
			if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/secrets/") {
				body = `{
					"metadata": {
						"labels": {
							"app.kubernetes.io/managed-by": "openclaw-deployer",
							"openclaw-deployer.redhat.com/instance": "instance"
						}
					}
				}`
			}
			if r.Method == http.MethodDelete {
				deleted[r.URL.Path] = true
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}

	s := &server{
		apiServer:       "https://kubernetes.example.test",
		bearerToken:     "service-account-token",
		namespaceSuffix: defaultNSSuffix,
		client:          client,
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/claw?namespace=sallyom-claw&name=instance", nil)
	req.Header.Set("X-Forwarded-User", "sallyom")
	rec := httptest.NewRecorder()

	s.handleDelete(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, path := range []string{
		"/apis/claw.sandbox.redhat.com/v1alpha1/namespaces/sallyom-claw/claws/instance",
		"/api/v1/namespaces/sallyom-claw/secrets/openclaw-instance-openai-api-key",
		"/api/v1/namespaces/sallyom-claw/secrets/openclaw-instance-openrouter-api-key",
	} {
		assert.True(t, deleted[path], "expected delete for %s", path)
	}
}

func TestCredentialSecretNamesIncludesTopLevelSecretRefs(t *testing.T) {
	claw := map[string]any{
		"spec": map[string]any{
			"credentials": []any{
				map[string]any{
					"name":      "openai",
					"secretRef": []any{map[string]any{"name": "provider-secret", "key": "api-key"}},
				},
			},
			"webSearch": map[string]any{
				"provider":  "brave",
				"secretRef": map[string]any{"name": "brave-secret", "key": "api-key"},
			},
			"auth": map[string]any{
				"mode":              "password",
				"passwordSecretRef": map[string]any{"name": "password-secret", "key": "password"},
			},
			"agentFiles": map[string]any{
				"git": map[string]any{
					"url":       "https://example.com/repo.git",
					"secretRef": map[string]any{"name": "git-secret"},
				},
			},
			"repoAccess": map[string]any{
				"github": map[string]any{
					"secretRef": map[string]any{"name": "github-secret", "key": "token"},
				},
			},
		},
	}

	assert.ElementsMatch(t, []string{"provider-secret", "brave-secret", "password-secret", "git-secret", "github-secret"}, credentialSecretNames(claw))
}

func TestHandleDeleteRemovesLabeledManagedSecrets(t *testing.T) {
	deleted := map[string]bool{}
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := "{}"
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/claws/instance") {
				body = `{"metadata":{"name":"instance"},"spec":{}}`
			}
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/secrets") {
				assert.Equal(t, managedByLabel+"="+managedByValue+","+instanceLabel+"=instance", r.URL.Query().Get("labelSelector"))
				body = `{"items":[{"metadata":{"name":"stale-telegram-secret"}}]}`
			}
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/secrets/stale-telegram-secret") {
				body = `{
					"metadata": {
						"labels": {
							"app.kubernetes.io/managed-by": "openclaw-deployer",
							"openclaw-deployer.redhat.com/instance": "instance"
						}
					}
				}`
			}
			if r.Method == http.MethodDelete {
				deleted[r.URL.Path] = true
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}
	s := &server{apiServer: "https://kubernetes.example.test", bearerToken: "service-account-token", client: client}
	req := httptest.NewRequest(http.MethodDelete, "/api/claw?namespace=sallyom-claw&name=instance", nil)
	req.Header.Set("X-Forwarded-User", "sallyom")
	rec := httptest.NewRecorder()

	s.handleDelete(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, deleted["/api/v1/namespaces/sallyom-claw/secrets/stale-telegram-secret"])
}

func TestHandleProvisionWithSecretNameSkipsSecretApply(t *testing.T) {
	var applied map[string]any
	secretTouched := false
	clawApplied := false
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			statusCode := http.StatusOK
			body := `{}`
			if strings.Contains(r.URL.Path, "/secrets/") {
				secretTouched = true
			}
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/claws/instance") && !clawApplied {
				statusCode = http.StatusNotFound
				body = `{"message":"not found"}`
			}
			if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/claws/instance") {
				clawApplied = true
				require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			}
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/claws/instance") && clawApplied {
				body = `{
					"metadata": {"namespace": "sallyom-claw", "name": "instance"},
					"spec": {
						"credentials": [{
							"name": "openai",
							"provider": "openai",
							"secretRef": [{"name": "existing-openai-key", "key": "api-key"}]
						}]
					}
				}`
			}
			return &http.Response{
				StatusCode: statusCode,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}
	s := &server{
		apiServer:   "https://kubernetes.example.test",
		bearerToken: "service-account-token",
		client:      client,
	}
	body := `{"namespace":"sallyom-claw","name":"instance","provider":"openai","secretName":"existing-openai-key","secretKey":"OPENAI_API_KEY"}`
	req := httptest.NewRequest(http.MethodPost, "/api/provision", strings.NewReader(body))
	req.Header.Set("X-Forwarded-User", "sallyom")
	rec := httptest.NewRecorder()

	s.handleProvision(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, secretTouched)
	credentials, _, _ := nestedSlice(applied, "spec", "credentials")
	require.Len(t, credentials, 1)
	first := credentials[0].(map[string]any)
	secretRefs := first["secretRef"].([]any)
	secretRef := secretRefs[0].(map[string]any)
	assert.Equal(t, "existing-openai-key", secretRef["name"])
	assert.Equal(t, "OPENAI_API_KEY", secretRef["key"])
}

func TestHandleProvisionExistingClawSkipsGCPValidationWithoutCredentialInput(t *testing.T) {
	clawApplied := false
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			statusCode := http.StatusOK
			body := `{}`
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/claws/instance") {
				body = `{
					"metadata": {"namespace": "sallyom-claw", "name": "instance"},
					"spec": {
						"credentials": [{
							"name": "openai",
							"provider": "openai",
							"secretRef": [{"name": "existing-openai-key", "key": "api-key"}]
						}]
					}
				}`
			}
			if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/claws/instance") {
				clawApplied = true
			}
			return &http.Response{
				StatusCode: statusCode,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}
	s := &server{
		apiServer:   "https://kubernetes.example.test",
		bearerToken: "service-account-token",
		client:      client,
	}
	body := `{"namespace":"sallyom-claw","name":"instance","provider":"google-vertex","integrations":[{"kind":"github-pat","secretName":"github-token"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/provision", strings.NewReader(body))
	req.Header.Set("X-Forwarded-User", "sallyom")
	rec := httptest.NewRecorder()

	s.handleProvision(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, clawApplied)
}

func TestHandleDeleteUsesRequestedNamespace(t *testing.T) {
	deleted := map[string]bool{}
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := "{}"
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/claws/unmanaged") {
				body = `{"metadata": {"namespace": "somalley-unmanaged-openclaw-test", "name": "unmanaged"}}`
			}
			if r.Method == http.MethodDelete {
				deleted[r.URL.Path] = true
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}

	s := &server{
		apiServer:       "https://kubernetes.example.test",
		bearerToken:     "service-account-token",
		namespaceSuffix: defaultNSSuffix,
		client:          client,
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/claw?namespace=somalley-unmanaged-openclaw-test&name=unmanaged", nil)
	req.Header.Set("X-Forwarded-User", "sallyom")
	rec := httptest.NewRecorder()

	s.handleDelete(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, deleted["/apis/claw.sandbox.redhat.com/v1alpha1/namespaces/somalley-unmanaged-openclaw-test/claws/unmanaged"])
}

func TestHandleDeleteCleansManagedSecretsWhenStateReadFails(t *testing.T) {
	deleted := map[string]bool{}
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			statusCode := http.StatusOK
			body := "{}"
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/claws/instance") {
				statusCode = http.StatusForbidden
				body = `{"message":"state read failed"}`
			}
			if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/secrets/") {
				body = `{
					"metadata": {
						"labels": {
							"app.kubernetes.io/managed-by": "openclaw-deployer",
							"openclaw-deployer.redhat.com/instance": "instance"
						}
					}
				}`
			}
			if r.Method == http.MethodDelete {
				deleted[r.URL.Path] = true
			}
			return &http.Response{
				StatusCode: statusCode,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}

	s := &server{
		apiServer:       "https://kubernetes.example.test",
		bearerToken:     "service-account-token",
		namespaceSuffix: defaultNSSuffix,
		client:          client,
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/claw?namespace=sallyom-claw&name=instance", nil)
	req.Header.Set("X-Forwarded-User", "sallyom")
	rec := httptest.NewRecorder()

	s.handleDelete(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, deleted["/apis/claw.sandbox.redhat.com/v1alpha1/namespaces/sallyom-claw/claws/instance"])
	for _, secretName := range managedProviderSecretNames("instance") {
		assert.True(t, deleted["/api/v1/namespaces/sallyom-claw/secrets/"+secretName], "expected delete for %s", secretName)
	}
}

func TestHandleClawsListsAllVisibleNamespaces(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodGet, r.Method)
			require.Equal(t, "/apis/claw.sandbox.redhat.com/v1alpha1/claws", r.URL.Path)
			body := `{
				"items": [
					{"metadata": {"namespace": "sallyom-claw", "name": "shifty"}},
					{"metadata": {"namespace": "somalley-unmanaged-openclaw-test", "name": "unmanaged"}}
				]
			}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}
	s := &server{
		apiServer:   "https://kubernetes.example.test",
		bearerToken: "service-account-token",
		client:      client,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/claws", nil)
	req.Header.Set("X-Forwarded-User", "sallyom")
	rec := httptest.NewRecorder()

	s.handleClaws(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload listResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Claws, 2)
	assert.Equal(t, "sallyom-claw", payload.Claws[0].Namespace)
	assert.Equal(t, "shifty", payload.Claws[0].Name)
	assert.Equal(t, "somalley-unmanaged-openclaw-test", payload.Claws[1].Namespace)
	assert.Equal(t, "unmanaged", payload.Claws[1].Name)
}

func TestHandleClawsFallsBackToVisibleOpenShiftProjects(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/apis/claw.sandbox.redhat.com/v1alpha1/claws", "/api/v1/namespaces":
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"message":"forbidden"}`)),
				}, nil
			case "/apis/project.openshift.io/v1/projects":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"items":[{"metadata":{"name":"sallyom-claw"}},{"metadata":{"name":"somalley-unmanaged-openclaw-test"}}]}`)),
				}, nil
			case "/apis/claw.sandbox.redhat.com/v1alpha1/namespaces/sallyom-claw/claws":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"items":[{"metadata":{"namespace":"sallyom-claw","name":"shifty"}}]}`)),
				}, nil
			case "/apis/claw.sandbox.redhat.com/v1alpha1/namespaces/somalley-unmanaged-openclaw-test/claws":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"items":[{"metadata":{"namespace":"somalley-unmanaged-openclaw-test","name":"unmanaged"}}]}`)),
				}, nil
			default:
				t.Fatalf("unexpected request path %s", r.URL.Path)
				return nil, nil
			}
		}),
	}
	s := &server{
		apiServer:   "https://kubernetes.example.test",
		bearerToken: "service-account-token",
		client:      client,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/claws", nil)
	req.Header.Set("X-Forwarded-User", "sallyom")
	rec := httptest.NewRecorder()

	s.handleClaws(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload listResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Claws, 2)
	assert.Equal(t, "sallyom-claw", payload.Claws[0].Namespace)
	assert.Equal(t, "shifty", payload.Claws[0].Name)
	assert.Equal(t, "somalley-unmanaged-openclaw-test", payload.Claws[1].Namespace)
	assert.Equal(t, "unmanaged", payload.Claws[1].Name)
}

func TestHandleNamespacesListsVisibleOpenShiftProjects(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/api/v1/namespaces":
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"message":"forbidden"}`)),
				}, nil
			case "/apis/project.openshift.io/v1/projects":
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"items":[{"metadata":{"name":"sallyom-claw"}},{"metadata":{"name":"somalley-dev"}}]}`)),
				}, nil
			default:
				t.Fatalf("unexpected request path %s", r.URL.Path)
				return nil, nil
			}
		}),
	}
	s := &server{
		apiServer:   "https://kubernetes.example.test",
		bearerToken: "service-account-token",
		client:      client,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/namespaces", nil)
	req.Header.Set("X-Forwarded-User", "sallyom")
	rec := httptest.NewRecorder()

	s.handleNamespaces(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload namespacesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, []string{"sallyom-claw", "somalley-dev"}, payload.Namespaces)
}

func TestNormalizeModelRef(t *testing.T) {
	tests := map[string]struct {
		provider string
		model    string
		want     string
	}{
		"openrouter nested":                     {provider: "openrouter", model: "anthropic/claude-sonnet-4-6", want: "openrouter/anthropic/claude-sonnet-4-6"},
		"openrouter full":                       {provider: "openrouter", model: "openrouter/auto", want: "openrouter/auto"},
		"anthropic bare":                        {provider: "anthropic", model: "claude-sonnet-4-6", want: "anthropic/claude-sonnet-4-6"},
		"anthropic vertex bare":                 {provider: "anthropic-vertex", model: "claude-sonnet-4-6", want: "anthropic-vertex/claude-sonnet-4-6"},
		"anthropic vertex remaps direct prefix": {provider: "anthropic-vertex", model: "anthropic/claude-sonnet-4-6", want: "anthropic-vertex/claude-sonnet-4-6"},
		"google empty":                          {provider: "google", model: "", want: ""},
		"google vertex empty":                   {provider: "google-vertex", model: "", want: ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeModelRef(tt.provider, tt.model))
		})
	}
}

func TestApplyClawWithoutModelSetsAgentNameOnly(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
				}, nil
			}
			require.Equal(t, http.MethodPatch, r.Method)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{
		apiServer:   "https://kubernetes.example.test",
		bearerToken: "service-account-token",
		client:      client,
	}
	req := provisionRequest{
		Namespace:      "sallyom-claw",
		Name:           "instance",
		Provider:       "google",
		ConfigureAgent: true,
		AgentName:      "Instance",
	}

	require.NoError(t, s.applyClaw(context.Background(), userIdentity{}, req))
	raw, _, _ := nestedMap(applied, "spec", "config", "raw")
	agents, _, _ := nestedSlice(raw, "agents", "list")
	require.Len(t, agents, 1)
	defaultAgent := agents[0].(map[string]any)
	assert.Equal(t, "Instance", defaultAgent["name"])
	_, hasModel, _ := nestedValue(raw, "agents", "defaults", "model")
	assert.False(t, hasModel, "blank model should not force a model config")
	management, _, _ := nestedString(applied, "spec", "config", "management")
	assert.Equal(t, "user", management)
}

func TestApplyClawSetsUserConfigManagement(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
				}, nil
			}
			require.Equal(t, http.MethodPatch, r.Method)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{
		apiServer:         "https://kubernetes.example.test",
		bearerToken:       "service-account-token",
		defaultManagement: "user",
		client:            client,
	}
	req := provisionRequest{
		Namespace: "sallyom-claw",
		Name:      "instance",
		Provider:  "google",
	}

	require.NoError(t, s.applyClaw(context.Background(), userIdentity{}, req))
	management, _, _ := nestedString(applied, "spec", "config", "management")
	assert.Equal(t, "user", management)
}

func TestApplyClawSetsOpenClawImage(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
				}, nil
			}
			require.Equal(t, http.MethodPatch, r.Method)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{
		apiServer:   "https://kubernetes.example.test",
		bearerToken: "service-account-token",
		client:      client,
	}
	req := provisionRequest{
		Namespace:     "sallyom-claw",
		Name:          "instance",
		Provider:      "google",
		OpenClawImage: "quay.io/example/openclaw:custom",
	}

	require.NoError(t, s.applyClaw(context.Background(), userIdentity{}, req))
	image, _, _ := nestedString(applied, "spec", "image")
	assert.Equal(t, "quay.io/example/openclaw:custom", image)
}

func TestHandleProvisionRejectsOpenClawImageWhenUserManagedDisabled(t *testing.T) {
	s := &server{userManagedDisabled: true}
	body := strings.NewReader(`{
		"namespace": "sallyom-claw",
		"name": "instance",
		"provider": "openrouter",
		"management": "operator",
		"openClawImage": "quay.io/example/openclaw:custom"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/provision", body)
	req.Header.Set("X-Forwarded-User", "sallyom")
	rr := httptest.NewRecorder()

	s.handleProvision(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "require user-managed config")
}

func TestHandleProvisionRejectsUserManagedWhenDisabled(t *testing.T) {
	s := &server{userManagedDisabled: true, defaultManagement: "operator"}
	body := strings.NewReader(`{
		"namespace": "sallyom-claw",
		"name": "instance",
		"provider": "openrouter",
		"management": "user"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/provision", body)
	req.Header.Set("X-Forwarded-User", "sallyom")
	rr := httptest.NewRecorder()

	s.handleProvision(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "user-managed config is disabled")
}

func TestStateFromClawDefaultsConfigManagementToOperator(t *testing.T) {
	state := stateFromClaw(map[string]any{
		"metadata": map[string]any{"name": "instance"},
		"spec":     map[string]any{"config": map[string]any{}},
	})

	assert.Equal(t, "operator", state.Management)
}

func TestStateFromClawIncludesOpenClawImage(t *testing.T) {
	state := stateFromClaw(map[string]any{
		"metadata": map[string]any{"name": "instance"},
		"spec": map[string]any{
			"image":  "quay.io/example/openclaw:custom",
			"config": map[string]any{},
		},
	})

	assert.Equal(t, "quay.io/example/openclaw:custom", state.Image)
}

func TestStateFromClawIncludesProviderCredentialRefs(t *testing.T) {
	state := stateFromClaw(map[string]any{
		"metadata": map[string]any{"name": "instance"},
		"spec": map[string]any{
			"credentials": []any{
				map[string]any{
					"name":     "openai",
					"provider": "openai",
					"secretRef": []any{
						map[string]any{"name": "shared-provider-secret", "key": "openai_api_key"},
					},
				},
				map[string]any{
					"name":     "openrouter",
					"provider": "openrouter",
					"secretRef": []any{
						map[string]any{"name": "shared-provider-secret", "key": "openrouter_api_key"},
					},
				},
			},
		},
	})

	require.Len(t, state.CredentialRefs, 2)
	assert.Equal(t, credentialRefResponse{
		Credential: "openai",
		Provider:   "openai",
		Name:       "shared-provider-secret",
		Key:        "openai_api_key",
	}, state.CredentialRefs[0])
	assert.Equal(t, credentialRefResponse{
		Credential: "openrouter",
		Provider:   "openrouter",
		Name:       "shared-provider-secret",
		Key:        "openrouter_api_key",
	}, state.CredentialRefs[1])
	assert.Equal(t, []string{"shared-provider-secret"}, state.SecretNames)
}

func TestProviderCredentialForVertex(t *testing.T) {
	req := provisionRequest{
		Name:        "instance",
		Provider:    "anthropic-vertex",
		GCPProject:  "my-project",
		GCPLocation: "us-east5",
	}
	credential := providerCredentialForRequest(req)
	assert.Equal(t, "anthropic-vertex", credential["name"])
	assert.Equal(t, "anthropic", credential["provider"])
	assert.Equal(t, "gcp", credential["type"])
	secretRefs := credential["secretRef"].([]map[string]string)
	require.Len(t, secretRefs, 1)
	assert.Equal(t, "openclaw-instance-anthropic-vertex-gcp", secretRefs[0]["name"])
	assert.Equal(t, gcpSecretKey, secretRefs[0]["key"])
	assert.Equal(t, map[string]string{"project": "my-project", "location": "us-east5"}, credential["gcp"])
}

func TestProviderCredentialUsesProvidedSecretName(t *testing.T) {
	req := provisionRequest{
		Name:       "instance",
		Provider:   "openai",
		SecretName: "existing-openai-key",
		SecretKey:  "OPENAI_API_KEY",
	}
	credential := providerCredentialForRequest(req)
	secretRefs := credential["secretRef"].([]map[string]string)
	require.Len(t, secretRefs, 1)
	assert.Equal(t, "existing-openai-key", secretRefs[0]["name"])
	assert.Equal(t, "OPENAI_API_KEY", secretRefs[0]["key"])
}

func TestApplySecretUsesProvidedSecretKey(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/secrets/existing-openai-key") {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{
		apiServer:   "https://kubernetes.example.test",
		bearerToken: "service-account-token",
		client:      client,
	}
	req := provisionRequest{
		Namespace:  "sallyom-claw",
		Name:       "instance",
		Provider:   "openai",
		APIKey:     "sk-test",
		SecretName: "existing-openai-key",
		SecretKey:  "OPENAI_API_KEY",
	}

	require.NoError(t, s.applySecret(context.Background(), userIdentity{}, req))
	data := applied["data"].(map[string]any)
	assert.Contains(t, data, "OPENAI_API_KEY")
	assert.NotContains(t, data, apiKeySecretKey)
}

func TestValidateGCPServiceAccountJSON(t *testing.T) {
	tests := map[string]struct {
		value   string
		wantErr bool
	}{
		"service account":  {value: `{"type":"service_account"}`},
		"authorized user":  {value: `{"type":"authorized_user"}`},
		"external account": {value: `{"type":"external_account"}`, wantErr: true},
		"not json":         {value: `not-json`, wantErr: true},
		"missing type":     {value: `{}`, wantErr: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateGCPServiceAccountJSON(tt.value)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestNormalizeConfigManagement(t *testing.T) {
	tests := map[string]struct {
		value   string
		want    string
		wantErr bool
	}{
		"default":  {want: "user"},
		"operator": {value: "operator", want: "operator"},
		"user":     {value: "user", want: "user"},
		"trim":     {value: " User ", want: "user"},
		"invalid":  {value: "other", wantErr: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeConfigManagement(tt.value)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAgentNameFromClawName(t *testing.T) {
	tests := map[string]string{
		"instance":           "Instance",
		"research-assistant": "Research Assistant",
		"team-ai-helper":     "Team Ai Helper",
		"":                   "OpenClaw",
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, want, agentNameFromClawName(name))
		})
	}
}

func TestApplyAgentConfig(t *testing.T) {
	raw := applyAgentConfig(map[string]any{}, "SallyBot", "openrouter/anthropic/claude-sonnet-4-6")
	primary, _, _ := nestedString(raw, "agents", "defaults", "model", "primary")
	assert.Equal(t, "openrouter/anthropic/claude-sonnet-4-6", primary)
	agents, _, _ := nestedSlice(raw, "agents", "list")
	require.Len(t, agents, 1)
	first := agents[0].(map[string]any)
	assert.Equal(t, "SallyBot", first["name"])
}

func TestApplyAgentConfigPreservesUserAgents(t *testing.T) {
	raw := map[string]any{
		"agents": map[string]any{
			"list": []any{
				map[string]any{
					"id":   "custom",
					"name": "Custom",
				},
				map[string]any{
					"id":   "default",
					"name": "Old Default",
				},
			},
		},
	}
	next := applyAgentConfig(raw, "SallyBot", "openrouter/auto")
	agents, _, _ := nestedSlice(next, "agents", "list")
	require.Len(t, agents, 2)
	custom := agents[0].(map[string]any)
	defaultAgent := agents[1].(map[string]any)
	assert.Equal(t, "Custom", custom["name"])
	assert.Equal(t, "SallyBot", defaultAgent["name"])
	assert.Equal(t, map[string]any{"primary": "openrouter/auto"}, defaultAgent["model"])
}

func TestValidateFilesystemSource(t *testing.T) {
	tests := map[string]struct {
		req     provisionRequest
		wantErr bool
	}{
		"none":              {req: provisionRequest{}},
		"git user":          {req: provisionRequest{FilesystemSource: "git", GitURL: "https://example.com/repo.git", Management: "user"}},
		"git operator":      {req: provisionRequest{FilesystemSource: "git", GitURL: "https://example.com/repo.git", Management: "operator"}, wantErr: true},
		"git bad url":       {req: provisionRequest{FilesystemSource: "git", GitURL: "git@example.com:repo.git", Management: "user"}, wantErr: true},
		"git empty url":     {req: provisionRequest{FilesystemSource: "git", Management: "user"}, wantErr: true},
		"configmap user":    {req: provisionRequest{FilesystemSource: "configmap", ConfigMapName: "seed", Management: "user"}},
		"configmap no name": {req: provisionRequest{FilesystemSource: "configmap", Management: "user"}, wantErr: true},
		"unknown source":    {req: provisionRequest{FilesystemSource: "ftp", Management: "user"}, wantErr: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			req := tt.req
			err := validateFilesystemSource(&req)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestAgentFilesSpec(t *testing.T) {
	assert.Nil(t, agentFilesSpec(provisionRequest{}))

	git := agentFilesSpec(provisionRequest{FilesystemSource: "git", GitURL: "https://example.com/repo.git", GitRef: "main", GitPath: "agents"})
	assert.Equal(t, map[string]any{"git": map[string]any{
		"url":  "https://example.com/repo.git",
		"ref":  "main",
		"path": "agents",
	}}, git)

	cm := agentFilesSpec(provisionRequest{FilesystemSource: "configmap", ConfigMapName: "seed"})
	assert.Equal(t, map[string]any{"configMapRef": map[string]any{"name": "seed"}}, cm)
}

func TestApplyClawSetsGitAgentFiles(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
				}, nil
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{apiServer: "https://kubernetes.example.test", bearerToken: "t", client: client}
	req := provisionRequest{
		Namespace:        "sallyom-claw",
		Name:             "instance",
		Provider:         "google",
		Management:       "user",
		FilesystemSource: "git",
		GitURL:           "https://example.com/repo.git",
		GitRef:           "main",
	}

	require.NoError(t, s.applyClaw(context.Background(), userIdentity{}, req))
	url, _, _ := nestedString(applied, "spec", "agentFiles", "git", "url")
	assert.Equal(t, "https://example.com/repo.git", url)
	ref, _, _ := nestedString(applied, "spec", "agentFiles", "git", "ref")
	assert.Equal(t, "main", ref)
}

func TestApplyClawPreservesExistingAgentFiles(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{
						"metadata": {"name": "instance"},
						"spec": {"agentFiles": {"git": {"url": "https://example.com/repo.git"}}}
					}`)),
				}, nil
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{apiServer: "https://kubernetes.example.test", bearerToken: "t", client: client}
	req := provisionRequest{Namespace: "sallyom-claw", Name: "instance", Provider: "google", Management: "user"}

	require.NoError(t, s.applyClaw(context.Background(), userIdentity{}, req))
	url, _, _ := nestedString(applied, "spec", "agentFiles", "git", "url")
	assert.Equal(t, "https://example.com/repo.git", url)
}

func TestApplyClawPreservesCredentialsWithoutCredentialInput(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{
						"metadata": {"name": "instance"},
						"spec": {
							"credentials": [{
								"name": "openai",
								"provider": "openai",
								"secretRef": [{"name": "existing-openai-key", "key": "api-key"}]
							}]
						}
					}`)),
				}, nil
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{apiServer: "https://kubernetes.example.test", bearerToken: "t", client: client}
	req := provisionRequest{Namespace: "sallyom-claw", Name: "instance", Provider: "openai", Management: "operator"}

	require.NoError(t, s.applyClaw(context.Background(), userIdentity{}, req))
	credentials, _, _ := nestedSlice(applied, "spec", "credentials")
	require.Len(t, credentials, 1)
	first := credentials[0].(map[string]any)
	secretRefs := first["secretRef"].([]any)
	secretRef := secretRefs[0].(map[string]any)
	assert.Equal(t, "existing-openai-key", secretRef["name"])
}

func TestApplyClawPreservesAgentConfigWithoutConfigureAgent(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{
						"metadata": {"name": "instance"},
						"spec": {
							"config": {
								"raw": {
									"agents": {
										"defaults": {
											"model": {"primary": "openai/gpt-5.5"}
										}
									}
								}
							},
							"credentials": [{
								"name": "openai",
								"provider": "openai",
								"secretRef": [{"name": "existing-openai-key", "key": "api-key"}]
							}]
						}
					}`)),
				}, nil
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{apiServer: "https://kubernetes.example.test", bearerToken: "t", client: client}
	req := provisionRequest{
		Namespace:  "sallyom-claw",
		Name:       "instance",
		Provider:   "openrouter",
		Management: "operator",
		Integrations: []integrationRequest{{
			Kind:       "github-pat",
			SecretName: "github-token",
		}},
	}

	require.NoError(t, s.applyClaw(context.Background(), userIdentity{}, req))
	model, _, _ := nestedString(applied, "spec", "config", "raw", "agents", "defaults", "model", "primary")
	assert.Equal(t, "openai/gpt-5.5", model)
}

func TestApplyClawAddsTelegramIntegration(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
				}, nil
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{apiServer: "https://kubernetes.example.test", bearerToken: "t", client: client}
	req := provisionRequest{
		Namespace:  "sallyom-claw",
		Name:       "instance",
		Provider:   "openai",
		Management: "operator",
		Integrations: []integrationRequest{{
			Kind:       "channel-telegram",
			SecretName: "telegram-secret",
			SecretKey:  "token",
		}},
	}

	require.NoError(t, s.applyClaw(context.Background(), userIdentity{}, req))
	credentials, _, _ := nestedSlice(applied, "spec", "credentials")
	require.Len(t, credentials, 1)
	telegram := credentials[0].(map[string]any)
	assert.Equal(t, "telegram", telegram["name"])
	assert.Equal(t, "telegram", telegram["channel"])
	refs := telegram["secretRef"].([]any)
	require.Len(t, refs, 1)
	ref := refs[0].(map[string]any)
	assert.Equal(t, "telegram-secret", ref["name"])
	assert.Equal(t, "token", ref["key"])
}

func TestIntegrationSecretKeyDefaultsSlackToBotToken(t *testing.T) {
	assert.Equal(t, "bot-token", integrationSecretKey(integrationRequest{Kind: "channel-slack"}))
	assert.Equal(t, "custom-key", integrationSecretKey(integrationRequest{Kind: "channel-slack", SecretKey: "custom-key"}))
}

func TestApplyIntegrationsAddsGitHubRepoAccess(t *testing.T) {
	credentials, _, _, repoAccess, err := applyIntegrationsToSpec(nil, provisionRequest{
		Name: "instance",
		Integrations: []integrationRequest{{
			Kind:        "github-pat",
			SecretValue: "ghp_secret",
			ExposeEnv:   true,
		}},
	})

	require.NoError(t, err)
	assert.Empty(t, credentials)
	github := repoAccess["github"].(map[string]any)
	ref := github["secretRef"].(map[string]any)
	assert.Equal(t, "openclaw-instance-github-pat", ref["name"])
	assert.Equal(t, "token", ref["key"])
	assert.Equal(t, true, github["enableApiProxy"])
	assert.Equal(t, true, github["enableGitHttps"])
	assert.Equal(t, true, github["exposeEnv"])
}

func TestApplyIntegrationsMigratesDeployerGitHubCredentialToRepoAccess(t *testing.T) {
	credentials := []any{
		map[string]any{
			"name":   "github",
			"type":   "bearer",
			"domain": "api.github.com",
			"secretRef": []any{
				map[string]any{"name": "openclaw-instance-github-pat", "key": "token"},
			},
		},
		map[string]any{
			"name":     "openai",
			"provider": "openai",
		},
	}

	next, _, _, repoAccess, err := applyIntegrationsToSpec(credentials, provisionRequest{
		Name: "instance",
		Integrations: []integrationRequest{{
			Kind:       "github-pat",
			SecretName: "github-token",
		}},
	})

	require.NoError(t, err)
	require.Len(t, next, 1)
	assert.Equal(t, "openai", next[0].(map[string]any)["name"])
	ref := repoAccess["github"].(map[string]any)["secretRef"].(map[string]any)
	assert.Equal(t, "github-token", ref["name"])
	assert.Equal(t, "token", ref["key"])
}

func TestApplyIntegrationsPreservesAbsentManagedCredentials(t *testing.T) {
	credentials := []any{
		map[string]any{
			"name":      "telegram",
			"channel":   "telegram",
			"secretRef": []any{map[string]any{"name": "openclaw-instance-telegram-bot-token", "key": "bot-token"}},
		},
		map[string]any{
			"name":      "custom-api",
			"provider":  "custom",
			"secretRef": []any{map[string]any{"name": "openclaw-instance-custom-api", "key": "api-key"}},
		},
		map[string]any{
			"name":   "github",
			"type":   "bearer",
			"domain": "api.github.com",
			"secretRef": []any{
				map[string]any{"name": "openclaw-instance-github-pat", "key": "token"},
			},
		},
		map[string]any{
			"name":      "work-telegram",
			"channel":   "telegram",
			"secretRef": []any{map[string]any{"name": "team-owned-secret", "key": "bot-token"}},
		},
		map[string]any{
			"name":     "openai",
			"provider": "openai",
		},
	}

	next, _, _, _, err := applyIntegrationsToSpec(credentials, provisionRequest{
		Name: "instance",
		Integrations: []integrationRequest{{
			Kind:       "channel-slack",
			SecretName: "slack-secret",
		}},
	})

	require.NoError(t, err)
	names := []string{}
	for _, credential := range next {
		credentialMap := credential.(map[string]any)
		name, _ := credentialMap["name"].(string)
		names = append(names, name)
	}
	assert.ElementsMatch(t, []string{"telegram", "custom-api", "github", "work-telegram", "openai", "slack"}, names)
}

func TestApplyIntegrationsRemovesExplicitManagedCredentials(t *testing.T) {
	credentials := []any{
		map[string]any{
			"name":      "telegram",
			"channel":   "telegram",
			"secretRef": []any{map[string]any{"name": "openclaw-instance-telegram-bot-token", "key": "bot-token"}},
		},
		map[string]any{
			"name":   "github",
			"type":   "bearer",
			"domain": "api.github.com",
			"secretRef": []any{
				map[string]any{"name": "openclaw-instance-github-pat", "key": "token"},
			},
		},
		map[string]any{
			"name":      "work-telegram",
			"channel":   "telegram",
			"secretRef": []any{map[string]any{"name": "team-owned-secret", "key": "bot-token"}},
		},
	}

	next, _, _, _, err := applyIntegrationsToSpec(credentials, provisionRequest{
		Name: "instance",
		RemovedIntegrations: []integrationRequest{
			{Kind: "channel-telegram"},
			{Kind: "github-pat"},
		},
	})

	require.NoError(t, err)
	names := []string{}
	for _, credential := range next {
		credentialMap := credential.(map[string]any)
		name, _ := credentialMap["name"].(string)
		names = append(names, name)
	}
	assert.ElementsMatch(t, []string{"work-telegram"}, names)
}

func TestApplyClawPreservesAbsentManagedTopLevelIntegrations(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{
						"metadata": {"name": "instance"},
						"spec": {
							"auth": {
								"mode": "password",
								"passwordSecretRef": {"name": "openclaw-instance-gateway-password", "key": "password"}
							},
							"webSearch": {
								"provider": "brave",
								"secretRef": {"name": "openclaw-instance-brave-search-api-key", "key": "api-key"}
							},
							"repoAccess": {
								"github": {
									"secretRef": {"name": "openclaw-instance-github-pat", "key": "token"},
									"enableApiProxy": true,
									"enableGitHttps": true
								}
							}
						}
					}`)),
				}, nil
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{apiServer: "https://kubernetes.example.test", bearerToken: "t", client: client}
	req := provisionRequest{Namespace: "sallyom-claw", Name: "instance", Provider: "openai", Management: "operator"}

	require.NoError(t, s.applyClaw(context.Background(), userIdentity{}, req))
	auth, ok, _ := nestedMap(applied, "spec", "auth")
	require.True(t, ok)
	assert.Equal(t, "password", auth["mode"])
	webSearch, ok, _ := nestedMap(applied, "spec", "webSearch")
	require.True(t, ok)
	assert.Equal(t, "brave", webSearch["provider"])
	repoAccess, ok, _ := nestedMap(applied, "spec", "repoAccess")
	require.True(t, ok)
	secretName, _, _ := nestedString(repoAccess, "github", "secretRef", "name")
	assert.Equal(t, "openclaw-instance-github-pat", secretName)
}

func TestApplyClawRemovesExplicitManagedTopLevelIntegrations(t *testing.T) {
	var applied map[string]any
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{
						"metadata": {"name": "instance"},
						"spec": {
							"auth": {
								"mode": "password",
								"passwordSecretRef": {"name": "openclaw-instance-gateway-password", "key": "password"}
							},
							"webSearch": {
								"provider": "brave",
								"secretRef": {"name": "openclaw-instance-brave-search-api-key", "key": "api-key"}
							},
							"repoAccess": {
								"github": {
									"secretRef": {"name": "openclaw-instance-github-pat", "key": "token"},
									"enableApiProxy": true,
									"enableGitHttps": true
								}
							}
						}
					}`)),
				}, nil
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applied))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	s := &server{apiServer: "https://kubernetes.example.test", bearerToken: "t", client: client}
	req := provisionRequest{
		Namespace:  "sallyom-claw",
		Name:       "instance",
		Provider:   "openai",
		Management: "operator",
		RemovedIntegrations: []integrationRequest{
			{Kind: "auth-password"},
			{Kind: "websearch-brave"},
			{Kind: "github-pat"},
		},
	}

	require.NoError(t, s.applyClaw(context.Background(), userIdentity{}, req))
	_, ok, _ := nestedMap(applied, "spec", "auth")
	assert.False(t, ok)
	_, ok, _ = nestedMap(applied, "spec", "webSearch")
	assert.False(t, ok)
	_, ok, _ = nestedMap(applied, "spec", "repoAccess")
	assert.False(t, ok)
}

func TestHandleProvisionWithWebSearchIntegrationCreatesSecretAndSpec(t *testing.T) {
	appliedSecrets := map[string]map[string]any{}
	var appliedClaw map[string]any
	clawApplied := false
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			statusCode := http.StatusOK
			body := `{}`
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/claws/instance") && !clawApplied {
				statusCode = http.StatusNotFound
				body = `{"message":"not found"}`
			}
			if r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/secrets/") {
				var secret map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&secret))
				appliedSecrets[r.URL.Path] = secret
			}
			if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/claws/instance") {
				clawApplied = true
				require.NoError(t, json.NewDecoder(r.Body).Decode(&appliedClaw))
			}
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/claws/instance") && clawApplied {
				body = `{"metadata":{"namespace":"sallyom-claw","name":"instance"},"spec":{"webSearch":{"provider":"brave","secretRef":{"name":"brave-secret","key":"api-key"}}}}`
			}
			return &http.Response{
				StatusCode: statusCode,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}
	s := &server{apiServer: "https://kubernetes.example.test", bearerToken: "t", client: client}
	body := `{
		"namespace":"sallyom-claw",
		"name":"instance",
		"provider":"openai",
		"secretName":"openai-secret",
		"integrations":[{"kind":"websearch-brave","secretName":"brave-secret","secretKey":"api-key","secretValue":"brave-token"}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/provision", strings.NewReader(body))
	req.Header.Set("X-Forwarded-User", "sallyom")
	rec := httptest.NewRecorder()

	s.handleProvision(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, appliedSecrets, "/api/v1/namespaces/sallyom-claw/secrets/brave-secret")
	provider, _, _ := nestedString(appliedClaw, "spec", "webSearch", "provider")
	assert.Equal(t, "brave", provider)
	secretName, _, _ := nestedString(appliedClaw, "spec", "webSearch", "secretRef", "name")
	assert.Equal(t, "brave-secret", secretName)
}

func TestCleanArchivePath(t *testing.T) {
	tests := map[string]string{
		"workspace-main/AGENTS.md": "workspace-main/AGENTS.md",
		"/openclaw.json":           "openclaw.json",
		"./skills/x.md":            "skills/x.md",
		"../../etc/passwd":         "",
		"":                         "",
		"  spaced/file.md  ":       "spaced/file.md",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			assert.Equal(t, want, cleanArchivePath(input))
		})
	}
}

func TestBuildAgentFilesArchive(t *testing.T) {
	archive, err := buildAgentFilesArchive(map[string][]byte{
		"openclaw.json":            []byte(`{"a":1}`),
		"workspace-main/AGENTS.md": []byte("hello"),
	})
	require.NoError(t, err)

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	found := map[string]string{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		content, err := io.ReadAll(tr)
		require.NoError(t, err)
		found[header.Name] = string(content)
	}
	assert.Equal(t, `{"a":1}`, found["openclaw.json"])
	assert.Equal(t, "hello", found["workspace-main/AGENTS.md"])
}

func TestApplyAgentConfigClearsStaleModelWhenEmpty(t *testing.T) {
	raw := map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"model":  map[string]any{"primary": "openai/gpt-5.5"},
				"models": map[string]any{"openai/gpt-5.5": map[string]any{"alias": "openai/gpt-5.5"}},
			},
			"list": []any{
				map[string]any{"id": "default", "name": "Instance", "model": map[string]any{"primary": "openai/gpt-5.5"}},
			},
		},
	}
	next := applyAgentConfig(raw, "Instance", "")

	_, hasModel, _ := nestedValue(next, "agents", "defaults", "model")
	assert.False(t, hasModel, "defaults.model should be cleared")
	_, hasModels, _ := nestedValue(next, "agents", "defaults", "models")
	assert.False(t, hasModels, "defaults.models should be cleared")
	agents, _, _ := nestedSlice(next, "agents", "list")
	require.Len(t, agents, 1)
	_, agentHasModel := agents[0].(map[string]any)["model"]
	assert.False(t, agentHasModel, "default agent model override should be cleared")
}

func TestReadyCondition(t *testing.T) {
	claw := map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":    "Ready",
					"status":  "True",
					"reason":  "Ready",
					"message": "Claw instance is ready",
				},
			},
		},
	}
	ready, reason, message := readyCondition(claw)
	assert.True(t, ready)
	assert.Equal(t, "Ready", reason)
	assert.NotEmpty(t, message)
}
