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

// Who is asking. The oauth-proxy sidecar authenticates the request and
// forwards the user's identity headers plus their OAuth access token
// (X-Forwarded-Access-Token). The token is what authorizes every Kubernetes
// read; the API server validates it itself, so a spoofed header alone grants
// nothing. The username headers are display-only.
//
// The console must still only be reached through the proxy: the Service
// exposes only the proxy port (4180), and the app port (8080) is reachable
// only on pod loopback, where the proxy is the sole client.

package main

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
)

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
	// DEVELOPER_USERNAME lets the binary run without an oauth-proxy in front of
	// it, but only in dev mode: a cluster deployment must never accept an
	// identity from anywhere but the proxy, or any in-cluster caller could
	// name themselves anyone.
	if devMode() {
		if user := strings.TrimSpace(os.Getenv("DEVELOPER_USERNAME")); user != "" {
			return user, nil
		}
	}
	return "", errors.New("OpenShift username was not forwarded to the console")
}

// devMode reports whether the console is running in an explicitly-flagged
// development configuration. The local-auth escape hatches — a fixed
// DEVELOPER_USERNAME, a DEVELOPER_BEARER_TOKEN standing in for the forwarded
// access token, TLS roots falling back to the public bundle — each weaken the
// tenant isolation a cluster deployment depends on, so none of them take
// effect unless this is set. In a cluster it never is, so a stray dev var
// cannot silently turn the security model off.
func devMode() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("AGENT_CONSOLE_DEV")), "true")
}

func currentIdentity(r *http.Request) (userIdentity, error) {
	user, err := currentUser(r)
	if err != nil {
		return userIdentity{}, err
	}
	token := strings.TrimSpace(r.Header.Get("X-Forwarded-Access-Token"))
	if token == "" && devMode() {
		token = strings.TrimSpace(os.Getenv("DEVELOPER_BEARER_TOKEN"))
	}
	if token == "" {
		return userIdentity{}, errors.New("no OAuth access token was forwarded to the console (is oauth-proxy running with --pass-access-token?)")
	}
	return userIdentity{Name: user, Token: token}, nil
}

// Names are interpolated into API paths and exec arguments, so they are
// validated against the Kubernetes naming rules before use rather than
// escaped afterwards.
var dnsNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func validateName(kind, name string) error {
	if name == "" {
		return apiError{StatusCode: http.StatusBadRequest, Message: kind + " is required"}
	}
	if len(name) > 253 || !dnsNameRE.MatchString(name) {
		return apiError{StatusCode: http.StatusBadRequest, Message: "invalid " + kind + ": " + name}
	}
	return nil
}
