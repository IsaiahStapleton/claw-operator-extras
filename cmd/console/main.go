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

// Agent Console: one read-only observability UI for OpenClaw agents across
// every namespace the logged-in user can reach.
//
// Two modes. In cluster mode it discovers Claws through the Kubernetes API and
// reads their session files by exec'ing into their pods, impersonating the
// user on every call so the API server decides what they may see. In local
// mode (AGENT_DATA_DIR set) it reads one directory straight off disk, which is
// what `make console-run-local` and the tests use.
//
// It never writes: only GET routes exist, and every command sent into a pod is
// a read.

package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

const (
	defaultListenAddr = ":8080"
	defaultCacheMs    = 2000
	// defaultAgentsDir is where OpenClaw keeps agent state inside a Claw pod.
	defaultAgentsDir = "/home/node/.openclaw/agents"
	// defaultContainer is the Claw pod's container that holds that state.
	defaultContainer = "gateway"
)

type server struct {
	// Kubernetes access (cluster mode).
	apiServer   string
	client      *http.Client
	bearerToken string
	impersonate bool
	agentsDir   string
	container   string

	// Local mode: one directory, no cluster.
	localDir string

	hostname   string
	gatewayURL string
	agentMeta  map[string]AgentMeta
	cacheTTL   time.Duration
	excluded   []string
	static     fs.FS

	// Stores are per (user, namespace, claw): the cache must never be shared
	// across users, or one tenant's snapshot could be served to another.
	mu     sync.Mutex
	stores map[string]*Store
}

func main() {
	s, err := newServer()
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/scope", s.handleScope)
	mux.HandleFunc("GET /api/agents", s.handleAgents)
	mux.HandleFunc("GET /api/runs", s.handleRuns)
	mux.HandleFunc("GET /api/runs/{agent}/{sessionId}", func(w http.ResponseWriter, r *http.Request) {
		s.handleRunDetail(w, r, r.PathValue("agent"), r.PathValue("sessionId"))
	})
	mux.HandleFunc("GET /api/runs/{agent}/{sessionId}/tail", func(w http.ResponseWriter, r *http.Request) {
		s.handleRunTail(w, r, r.PathValue("agent"), r.PathValue("sessionId"))
	})
	mux.HandleFunc("GET /api/handoffs", s.handleHandoffs)
	mux.HandleFunc("GET /api/memory", s.handleMemory)
	mux.HandleFunc("GET /api/wiki", s.handleWiki)
	mux.HandleFunc("GET /api/wiki/page", s.handleWikiPage)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /", s.handleStatic)

	addr := getenv("LISTEN_ADDR", defaultListenAddr)
	if s.localDir != "" {
		log.Printf("agent console listening on %s (local directory %s)", addr, s.localDir)
	} else {
		log.Printf("agent console listening on %s (cluster mode, agents dir %s)", addr, s.agentsDir)
	}
	log.Fatal(http.ListenAndServe(addr, mux))
}

func newServer() (*server, error) {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()

	var excluded []string
	if raw := os.Getenv("EXCLUDED_AGENTS"); raw != "" {
		excluded = strings.Split(raw, ",")
	}

	s := &server{
		localDir:   os.Getenv("AGENT_DATA_DIR"),
		agentsDir:  getenv("CLAW_AGENTS_DIR", defaultAgentsDir),
		container:  getenv("CLAW_CONTAINER", defaultContainer),
		hostname:   hostname,
		gatewayURL: os.Getenv("GATEWAY_URL"),
		agentMeta:  parseAgentMeta(os.Getenv("AGENT_META")),
		cacheTTL:   time.Duration(getenvInt("CONSOLE_CACHE_MS", defaultCacheMs)) * time.Millisecond,
		excluded:   excluded,
		static:     sub,
		stores:     map[string]*Store{},
	}
	if s.localDir != "" {
		return s, nil // local mode needs no cluster access
	}

	if s.apiServer, err = kubeAPIServerURL(); err != nil {
		return nil, err
	}
	if s.client, err = kubeHTTPClient(); err != nil {
		return nil, err
	}
	if s.bearerToken, s.impersonate, err = kubeBearerToken(); err != nil {
		return nil, err
	}
	return s, nil
}

// storeFor returns the cached store for one user's view of one Claw. Keying by
// user as well as by Claw keeps a snapshot built under one identity from ever
// being served to another.
func (s *server) storeFor(identity userIdentity, namespace, claw, pod string) *Store {
	key := identity.Name + "\x00" + namespace + "\x00" + claw + "\x00" + pod
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.stores[key]; ok {
		return st
	}
	src := sessionSource(execSource{
		srv: s, identity: identity, namespace: namespace,
		pod: pod, container: s.container, agentsDir: s.agentsDir,
	})
	st := newStoreFromSource(src, s.cacheTTL, s.excluded)
	s.stores[key] = st
	return st
}

// handleStatic serves the embedded SPA. The app uses hash routing, so unknown
// non-API paths fall back to index.html.
func (s *server) handleStatic(w http.ResponseWriter, r *http.Request) {
	clean := strings.TrimPrefix(r.URL.Path, "/")
	if clean == "" {
		clean = "index.html"
	}
	if _, err := fs.Stat(s.static, clean); err != nil {
		http.ServeFileFS(w, r, s.static, "index.html")
		return
	}
	http.ServeFileFS(w, r, s.static, clean)
}

func parseAgentMeta(raw string) map[string]AgentMeta {
	meta := map[string]AgentMeta{}
	if raw == "" {
		return meta
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		log.Printf("AGENT_META ignored (invalid JSON): %v", err)
		return map[string]AgentMeta{}
	}
	return meta
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
