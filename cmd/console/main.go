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
// reads their session files by exec'ing into their pods, carrying the
// logged-in user's own forwarded OAuth token on every call so the API server
// decides what they may see. In local mode (AGENT_DATA_DIR set) it reads one
// directory straight off disk, which is what `make console-run-local` and the
// tests use.
//
// It never writes: only GET routes exist, and every command sent into a pod is
// a read.

package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
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
	// Kubernetes access (cluster mode). No credential lives here: every
	// tenant read is authorized by the requesting user's forwarded token.
	apiServer string
	client    *http.Client
	agentsDir string
	container string

	// Local mode: one directory, no cluster.
	localDir string

	hostname string
	cacheTTL time.Duration
	excluded []string
	static   fs.FS

	// stateDir persists what the console has observed. Empty means the record
	// lives only in memory and is lost on restart.
	stateDir string

	// Stores are per (user, namespace, claw): the cache must never be shared
	// across users, or one tenant's snapshot could be served to another. Every
	// distinct viewer and every pod restart mints a new key, so idle stores are
	// reaped to bound memory in a long-lived multi-tenant console. storeSeen is
	// guarded by mu.
	mu        sync.Mutex
	stores    map[string]*Store
	storeSeen map[string]time.Time
	// Watchers are per Claw, not per user. What a Claw's notes did is a fact
	// about the Claw; sharing the observation is what lets one user see a diff
	// the console happened to catch while another was looking. Reaching a
	// watcher at all still requires a read the API server authorized under the
	// user's own token, so this widens no one's access.
	watchers map[string]*memoryWatcher
}

func main() {
	s, err := newServer()
	if err != nil {
		log.Fatal(err)
	}
	go s.reapIdleStores()

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
	mux.HandleFunc("GET /api/memory/note", s.handleMemoryNote)
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
	for _, a := range strings.Split(os.Getenv("EXCLUDED_AGENTS"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			excluded = append(excluded, a)
		}
	}

	s := &server{
		localDir:  os.Getenv("AGENT_DATA_DIR"),
		agentsDir: getenv("CLAW_AGENTS_DIR", defaultAgentsDir),
		container: getenv("CLAW_CONTAINER", defaultContainer),
		hostname:  hostname,
		cacheTTL:  time.Duration(getenvInt("CONSOLE_CACHE_MS", defaultCacheMs)) * time.Millisecond,
		excluded:  excluded,
		static:    sub,
		stateDir:  os.Getenv("CONSOLE_STATE_DIR"),
		stores:    map[string]*Store{},
		storeSeen: map[string]time.Time{},
		watchers:  map[string]*memoryWatcher{},
	}
	// An unwritable state directory is reported and then ignored: the console
	// degrades to an in-memory record, which the memory page says outright,
	// rather than refusing to start over a durability feature.
	if s.stateDir != "" {
		if err := os.MkdirAll(s.stateDir, 0o755); err != nil {
			log.Printf("CONSOLE_STATE_DIR unusable, memory observations will not survive a restart: %v", err)
			s.stateDir = ""
		}
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
	return s, nil
}

// storeFor returns the cached store for one user's view of one Claw. The key
// includes the login token (hashed, so raw credentials are not spread through
// map keys): a snapshot built under one credential is never served to
// another, and a fresh login pays one cold scan rather than inheriting a
// store whose captured token may have expired.
func (s *server) storeFor(identity userIdentity, namespace, claw, pod string) *Store {
	sum := sha256.Sum256([]byte(identity.Token))
	key := identity.Name + "\x00" + hex.EncodeToString(sum[:8]) + "\x00" + namespace + "\x00" + claw + "\x00" + pod
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storeSeen == nil {
		s.storeSeen = map[string]time.Time{}
	}
	s.storeSeen[key] = time.Now()
	if st, ok := s.stores[key]; ok {
		return st
	}
	src := sessionSource(execSource{
		srv: s, identity: identity, namespace: namespace,
		pod: pod, container: s.container, agentsDir: s.agentsDir,
	})
	st := newStoreFromSource(src, s.cacheTTL, s.excluded, s.watcherFor(namespace, claw))
	s.stores[key] = st
	return st
}

// storeIdleTTL is how long a per-viewer store may sit unused before it is
// reaped. Its parsed-session cache is rebuilt on next access, so eviction costs
// one cold scan, not correctness.
const storeIdleTTL = 30 * time.Minute

// reapIdleStores drops stores no request has touched within storeIdleTTL, so a
// long-lived console does not accumulate one Store per distinct viewer and pod
// name forever. Watchers are left alone: they are per Claw, not per viewer, and
// already bounded.
func (s *server) reapIdleStores() {
	for range time.Tick(storeIdleTTL / 3) {
		cutoff := time.Now().Add(-storeIdleTTL)
		s.mu.Lock()
		for key, seen := range s.storeSeen {
			if seen.Before(cutoff) {
				delete(s.stores, key)
				delete(s.storeSeen, key)
			}
		}
		s.mu.Unlock()
	}
}

// watcherFor returns the shared memory watcher for one Claw. Callers hold s.mu.
func (s *server) watcherFor(namespace, claw string) *memoryWatcher {
	key := namespace + "\x00" + claw
	if s.watchers == nil {
		s.watchers = map[string]*memoryWatcher{}
	}
	if w, ok := s.watchers[key]; ok {
		return w
	}
	w := newMemoryWatcher(watcherPath(s.stateDir, namespace, claw))
	s.watchers[key] = w
	return w
}

// handleStatic serves the embedded SPA. The app uses hash routing, so unknown
// non-API paths fall back to index.html.
//
// embed.FS carries no modtime, so http.ServeFileFS cannot set Last-Modified or
// ETag. Without those, a browser that cached a prior deploy's app.js has no
// way to revalidate and may serve stale JS indefinitely. Setting no-cache
// forces revalidation on every navigation; the cost is one conditional GET per
// page load, which is negligible for a handful of small embedded files.
func (s *server) handleStatic(w http.ResponseWriter, r *http.Request) {
	clean := strings.TrimPrefix(r.URL.Path, "/")
	if clean == "" {
		clean = "index.html"
	}
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := fs.Stat(s.static, clean); err != nil {
		http.ServeFileFS(w, r, s.static, "index.html")
		return
	}
	http.ServeFileFS(w, r, s.static, clean)
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
