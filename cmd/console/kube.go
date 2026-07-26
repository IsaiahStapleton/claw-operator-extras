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

// Kubernetes access, modeled on cmd/deployer: plain net/http against the API
// server with impersonation headers, rather than a client library. Every
// tenant-visible read is impersonated so the API server — not this process —
// decides what the logged-in user may see.

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	inClusterCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	inClusterTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	clawAPIGroup       = "claw.sandbox.redhat.com"
	clawAPIVersion     = "v1alpha1"
)

// userIdentity is the logged-in user, as reported by the oauth-proxy.
type userIdentity struct {
	Name   string
	Groups []string
}

// apiError carries the API server's status code so handlers can pass an
// authorization failure through to the browser unchanged instead of
// flattening every failure to a 500.
type apiError struct {
	StatusCode int
	Message    string
}

func (e apiError) Error() string { return e.Message }

func statusCodeFor(err error) int {
	var apiErr apiError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return http.StatusInternalServerError
}

func kubeAPIServerURL() (string, error) {
	if override := os.Getenv("KUBE_API_SERVER"); override != "" {
		return strings.TrimRight(override, "/"), nil
	}
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := getenv("KUBERNETES_SERVICE_PORT", "443")
	if host == "" {
		return "", errors.New("KUBERNETES_SERVICE_HOST is not set; set KUBE_API_SERVER for local testing")
	}
	return "https://" + host + ":" + port, nil
}

func kubeHTTPClient() (*http.Client, error) {
	caPEM, err := os.ReadFile(inClusterCAPath)
	if err != nil {
		if os.Getenv("KUBE_API_SERVER") != "" {
			return &http.Client{Timeout: 30 * time.Second}, nil
		}
		return nil, fmt.Errorf("read Kubernetes CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("failed to parse Kubernetes CA bundle")
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}, nil
}

func kubeBearerToken() (string, bool, error) {
	if token := strings.TrimSpace(os.Getenv("DEVELOPER_BEARER_TOKEN")); token != "" {
		return token, strings.EqualFold(os.Getenv("AGENT_CONSOLE_IMPERSONATE"), "true"), nil
	}
	token, err := os.ReadFile(inClusterTokenPath)
	if err != nil {
		return "", false, fmt.Errorf("read Kubernetes service account token: %w", err)
	}
	return strings.TrimSpace(string(token)), true, nil
}

// setAuth applies the console's own credential plus, when impersonation is
// enabled, the logged-in user's identity. Callers must pass the identity for
// any request whose result reaches a browser.
func (s *server) setAuth(req *http.Request, identity userIdentity) {
	req.Header.Set("Authorization", "Bearer "+s.bearerToken)
	if s.impersonate {
		req.Header.Set("Impersonate-User", identity.Name)
		for _, group := range identity.Groups {
			req.Header.Add("Impersonate-Group", group)
		}
	}
}

// kubeGet performs an impersonated GET and decodes the response into out.
func (s *server) kubeGet(ctx context.Context, identity userIdentity, requestPath string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiServer+requestPath, nil)
	if err != nil {
		return err
	}
	s.setAuth(req, identity)
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError{StatusCode: resp.StatusCode, Message: kubeErrorMessage(body, resp.Status)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// kubeErrorMessage prefers the API server's own explanation, which usually
// names the missing permission.
func kubeErrorMessage(body []byte, fallback string) string {
	var status struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &status); err == nil && status.Message != "" {
		return status.Message
	}
	return fallback
}
