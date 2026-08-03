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
// server, rather than a client library. Every tenant-visible read carries the
// logged-in user's own OAuth token, forwarded by the oauth-proxy, so the API
// server (not this process) decides what they may see. The console holds no
// credential of its own that can read tenant data.

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	inClusterCAPath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	clawAPIGroup    = "claw.sandbox.redhat.com"
	clawAPIVersion  = "v1alpha1"
)

// userIdentity is the logged-in user: the display name the oauth-proxy
// reported, and the OAuth access token that authorizes their reads.
type userIdentity struct {
	Name  string
	Token string
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
		// Falling back to the system roots validates the API server against
		// public CAs, which for a cluster-internal endpoint is effectively no
		// validation. Only a dev run may do this, and it is announced.
		if devMode() && os.Getenv("KUBE_API_SERVER") != "" {
			log.Printf("WARNING: in-cluster CA unreadable; verifying the API server against system roots (AGENT_CONSOLE_DEV)")
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

// setAuth authorizes a request with the logged-in user's forwarded OAuth
// token. Callers must pass the identity for any request whose result reaches
// a browser: what the user's token may not read, the console cannot read.
func (s *server) setAuth(req *http.Request, identity userIdentity) {
	req.Header.Set("Authorization", "Bearer "+identity.Token)
}

// kubeGet performs a GET as the logged-in user and decodes the response into
// out.
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
