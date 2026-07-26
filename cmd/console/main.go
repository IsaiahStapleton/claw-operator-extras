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

// Agent Console: a read-only observability UI for OpenClaw agents. It scans a
// directory of trajectory/transcript JSONL files and serves a PatternFly-styled
// single-page app plus a small JSON API. It never writes to the data mount and
// exposes only GET routes, so it is safe to run against live agent state.

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
	"time"
)

//go:embed static/*
var staticFiles embed.FS

const (
	defaultListenAddr = ":8080"
	defaultDataDir    = "/data/agents"
	defaultCacheMs    = 2000
)

type server struct {
	store      *Store
	gatewayURL string
	hostname   string
	dataDir    string
	agentMeta  map[string]AgentMeta
	static     fs.FS
}

func main() {
	s := newServer()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
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
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /", s.handleStatic)

	addr := getenv("LISTEN_ADDR", defaultListenAddr)
	log.Printf("agent console listening on %s (data dir %s)", addr, s.dataDir)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func newServer() *server {
	dataDir := getenv("AGENT_DATA_DIR", defaultDataDir)
	cacheMs := getenvInt("CONSOLE_CACHE_MS", defaultCacheMs)
	var excluded []string
	if raw := os.Getenv("EXCLUDED_AGENTS"); raw != "" {
		excluded = strings.Split(raw, ",")
	}
	hostname, _ := os.Hostname()

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("embed static: %v", err)
	}

	return &server{
		store:      newStore(dataDir, time.Duration(cacheMs)*time.Millisecond, excluded),
		gatewayURL: os.Getenv("GATEWAY_URL"),
		hostname:   hostname,
		dataDir:    dataDir,
		agentMeta:  parseAgentMeta(os.Getenv("AGENT_META")),
		static:     sub,
	}
}

// handleStatic serves the embedded SPA. The app uses hash routing, so only the
// index and its assets are ever requested; unknown non-API paths fall back to
// index.html to stay robust if that changes.
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
