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
	"embed"
	"log"
	"net/http"
	"strings"
)

//go:embed static/*
var staticFiles embed.FS

func main() {
	s, err := newServer()
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("GET /api/namespaces", s.handleNamespaces)
	mux.HandleFunc("GET /api/claws", s.handleClaws)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/provision", s.handleProvision)
	mux.HandleFunc("POST /api/agentfiles", s.handleAgentFiles)
	mux.HandleFunc("POST /api/restart", s.handleRestart)
	mux.HandleFunc("DELETE /api/claw", s.handleDelete)
	mux.Handle("GET /static/", s.static)
	mux.HandleFunc("GET /", s.handleIndex)

	addr := getenv("LISTEN_ADDR", defaultListenAddr)
	log.Printf("openclaw deployer listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func newServer() (*server, error) {
	apiServer, err := kubeAPIServerURL()
	if err != nil {
		return nil, err
	}

	client, err := kubeHTTPClient()
	if err != nil {
		return nil, err
	}
	bearerToken, impersonate, err := kubeBearerToken()
	if err != nil {
		return nil, err
	}
	defaultConfigManagement, err := normalizeConfigManagement(getenv("CLAW_CONFIG_MANAGEMENT_DEFAULT", defaultManagement))
	if err != nil {
		return nil, err
	}
	userManagedDisabled := strings.EqualFold(getenv(disableUserConfigEnv, "false"), "true")
	if userManagedDisabled && defaultConfigManagement == "user" {
		defaultConfigManagement = "operator"
	}

	return &server{
		apiServer:           apiServer,
		bearerToken:         bearerToken,
		impersonate:         impersonate,
		namespaceSuffix:     getenv("CLAW_NAMESPACE_SUFFIX", defaultNSSuffix),
		defaultManagement:   defaultConfigManagement,
		userManagedDisabled: userManagedDisabled,
		client:              client,
		static:              http.FileServer(http.FS(staticFiles)),
	}, nil
}
