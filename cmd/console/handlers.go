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

// Read-only JSON API over the agent data directory. Every handler is a GET;
// there is no mutating route by construction.

package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// dataStatus is the integrity envelope attached to every list response.
type dataStatus struct {
	OK              bool   `json:"ok"`
	Error           string `json:"error"`
	BadLines        int    `json:"badLines"`
	ScannedFiles    int    `json:"scannedFiles"`
	UnreadableFiles int    `json:"unreadableFiles"`
	TruncatedEvents int    `json:"truncatedEvents"`
}

func toDataStatus(snap *Snapshot) dataStatus {
	return dataStatus{
		OK: snap.OK, Error: snap.Error, BadLines: snap.BadLines,
		ScannedFiles: snap.ScannedFiles, UnreadableFiles: snap.UnreadableFiles,
		TruncatedEvents: snap.TruncatedEvents,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// RunView enriches a Run with handoff edges for the runs list.
type RunView struct {
	Run
	HandoffsOut int      `json:"handoffsOut"`
	SpawnedBy   *Handoff `json:"spawnedBy"`
}

// buildRunViews attaches handoff counts and parent edges, matching the Node
// server's /api/runs enrichment.
func buildRunViews(runs []Run, edges []Handoff) []RunView {
	views := make([]RunView, 0, len(runs))
	for _, r := range runs {
		out := 0
		var spawnedBy *Handoff
		for i := range edges {
			e := &edges[i]
			if e.FromSessionID == r.SessionID && e.FromRunID == r.RunID {
				out++
			}
			// Attribution is by sessionId: multi-run sessions share the edge.
			if spawnedBy == nil && e.ToSessionID == r.SessionID {
				spawnedBy = e
			}
		}
		views = append(views, RunView{Run: r, HandoffsOut: out, SpawnedBy: spawnedBy})
	}
	return views
}

/* --------------------------------------------------------- request scope */

// resolveStore turns a request into the store for one Claw, under the
// logged-in user's identity. In local mode there is one implicit Claw and no
// cluster; in cluster mode the namespace and claw come from the query string
// and every downstream call is impersonated.
func (s *server) resolveStore(r *http.Request) (*Store, error) {
	if s.localDir != "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if st, ok := s.stores["local"]; ok {
			return st, nil
		}
		st := newStoreFromSource(dirSource{root: s.localDir}, s.cacheTTL, s.excluded)
		s.stores["local"] = st
		return st, nil
	}

	identity, err := currentIdentity(r)
	if err != nil {
		return nil, apiError{StatusCode: http.StatusUnauthorized, Message: err.Error()}
	}
	namespace := r.URL.Query().Get("namespace")
	claw := r.URL.Query().Get("claw")
	if err := validateName("namespace", namespace); err != nil {
		return nil, err
	}
	if err := validateName("claw", claw); err != nil {
		return nil, err
	}

	pod, err := s.clawPod(r.Context(), identity, namespace, claw)
	if err != nil {
		return nil, err
	}
	return s.storeFor(identity, namespace, claw, pod), nil
}

// handleScope lists the Claws this user may read. The UI calls it first to
// populate its picker; an empty list means the user has access to none.
func (s *server) handleScope(w http.ResponseWriter, r *http.Request) {
	if s.localDir != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"local": true,
			"claws": []ClawRef{{Namespace: "local", Name: "local", Ready: true}},
			"user":  "",
		})
		return
	}
	identity, err := currentIdentity(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	claws, err := s.listClaws(r.Context(), identity)
	if err != nil {
		writeJSON(w, statusCodeFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"local": false,
		"claws": claws,
		"user":  identity.Name,
	})
}

func (s *server) handleAgents(w http.ResponseWriter, r *http.Request) {
	store, err := s.resolveStore(r)
	if err != nil {
		writeJSON(w, statusCodeFor(err), map[string]string{"error": err.Error()})
		return
	}
	snap := store.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"agents":   s.buildAgentViews(snap, time.Now()),
		"data":     toDataStatus(snap),
		"hostname": s.hostname,
	})
}

func (s *server) handleRuns(w http.ResponseWriter, r *http.Request) {
	store, err := s.resolveStore(r)
	if err != nil {
		writeJSON(w, statusCodeFor(err), map[string]string{"error": err.Error()})
		return
	}
	snap := store.snapshot()
	q := r.URL.Query()
	edges := findHandoffs(snap.Sessions, snap.Agents)

	agent := q.Get("agent")
	outcome := q.Get("outcome")
	search := strings.ToLower(q.Get("q"))
	sinceMs := int64(0)
	if since := q.Get("since"); since != "" {
		sinceMs = tsMillis(since)
	}

	var filtered []Run
	for _, run := range snap.Runs {
		if agent != "" && run.Agent != agent {
			continue
		}
		if outcome != "" && run.Outcome != outcome {
			continue
		}
		if sinceMs != 0 && tsMillis(run.StartedAt) < sinceMs {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(run.Prompt), search) {
			continue
		}
		filtered = append(filtered, run)
	}

	total := len(filtered)
	limit := clampInt(q.Get("limit"), 50, 1, 500)
	if limit < len(filtered) {
		filtered = filtered[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runs":  buildRunViews(filtered, edges),
		"total": total,
		"data":  toDataStatus(snap),
	})
}

func (s *server) handleRunDetail(w http.ResponseWriter, r *http.Request, agent, sessionID string) {
	q := r.URL.Query()
	offset := clampInt(q.Get("offset"), 0, 0, 1<<30)
	limit := clampInt(q.Get("limit"), 500, 1, 1000)
	store, err := s.resolveStore(r)
	if err != nil {
		writeJSON(w, statusCodeFor(err), map[string]string{"error": err.Error()})
		return
	}
	snap := store.snapshot()
	edges := findHandoffs(snap.Sessions, snap.Agents)

	var parent *Handoff
	var children []Handoff
	for i := range edges {
		e := &edges[i]
		if e.ToSessionID == sessionID {
			parent = e
		}
		if e.FromSessionID == sessionID {
			children = append(children, *e)
		}
	}
	if children == nil {
		children = []Handoff{}
	}
	run := latestRunOf(snap.Runs, agent, sessionID)

	if detail := store.sessionEvents(agent, sessionID, offset, limit); detail != nil {
		events := make([]map[string]any, 0, len(detail.Events))
		for _, e := range detail.Events {
			events = append(events, replayEvent(e))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agent": agent, "sessionId": sessionID, "total": detail.Total,
			"badLines": detail.BadLines, "offset": offset, "source": "trajectory",
			"parent": parent, "children": children, "run": run, "events": events,
		})
		return
	}

	// Fall back to plain transcript.
	if msgs, total, ok := store.sessionTranscript(agent, sessionID, offset, limit); ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"agent": agent, "sessionId": sessionID, "total": total,
			"badLines": 0, "offset": offset, "source": "transcript",
			"parent": nil, "children": []Handoff{}, "run": run, "events": msgs,
		})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
}

// handleRunTail returns only events past a known count, so the replay view can
// cheaply poll a running session for new events.
func (s *server) handleRunTail(w http.ResponseWriter, r *http.Request, agent, sessionID string) {
	after := clampInt(r.URL.Query().Get("after"), 0, 0, 1<<30)
	store, err := s.resolveStore(r)
	if err != nil {
		writeJSON(w, statusCodeFor(err), map[string]string{"error": err.Error()})
		return
	}
	detail := store.sessionEvents(agent, sessionID, after, 1000)
	if detail == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	events := make([]map[string]any, 0, len(detail.Events))
	for _, e := range detail.Events {
		events = append(events, replayEvent(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent": agent, "sessionId": sessionID, "total": detail.Total,
		"after": after, "events": events,
	})
}

func replayEvent(e Event) map[string]any {
	var model any
	if e.ModelID != "" {
		model = e.ModelID
	}
	data := e.Data
	if data == nil {
		data = map[string]any{}
	}
	return map[string]any{
		"ts": e.TS, "seq": e.Seq, "type": e.Type, "runId": e.RunID,
		"summary": eventSummary(e), "data": data, "model": model,
	}
}

func (s *server) handleHandoffs(w http.ResponseWriter, r *http.Request) {
	store, err := s.resolveStore(r)
	if err != nil {
		writeJSON(w, statusCodeFor(err), map[string]string{"error": err.Error()})
		return
	}
	snap := store.snapshot()
	edges := findHandoffs(snap.Sessions, snap.Agents)
	writeJSON(w, http.StatusOK, map[string]any{
		"handoffs": edges, "data": toDataStatus(snap),
	})
}

// handleMemory reports the Claw's durable notes as they exist on disk.
//
// This deliberately does not derive the feed from tool calls. OpenClaw's
// consolidation and wiki synthesis write notes directly, without an agent tool
// call to observe, so a tool-derived feed saw only a fraction of them — 22 of
// 422 notes on a real Claw. Reading the stores is the only way to answer "what
// has my fleet committed to memory" truthfully. Where a tool call did record a
// write, it still supplies the attribution the filesystem cannot.
func (s *server) handleMemory(w http.ResponseWriter, r *http.Request) {
	store, err := s.resolveStore(r)
	if err != nil {
		writeJSON(w, statusCodeFor(err), map[string]string{"error": err.Error()})
		return
	}
	limit := clampInt(r.URL.Query().Get("limit"), 50, 1, 500)
	writes, snap, err := store.memoryFeed(r.Context(), limit)
	if err != nil {
		writeJSON(w, statusCodeFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"writes": writes, "data": toDataStatus(snap),
	})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	store, err := s.resolveStore(r)
	if err != nil {
		writeJSON(w, statusCodeFor(err), map[string]string{"error": err.Error()})
		return
	}
	snap := store.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"gateway":      gatewayHealth(r.Context(), s.gatewayURL),
		"data":         toDataStatus(snap),
		"staleAfterMs": staleAfter.Milliseconds(),
		"hostname":     s.hostname,
	})
}

func (s *server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	store, err := s.resolveStore(r)
	if err != nil {
		writeJSON(w, statusCodeFor(err), map[string]string{"error": err.Error()})
		return
	}
	snap := store.snapshot()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(renderMetrics(snap)))
}

func latestRunOf(runs []Run, agent, sessionID string) *Run {
	for i := range runs {
		if runs[i].Agent == agent && runs[i].SessionID == sessionID {
			return &runs[i]
		}
	}
	return nil
}

// clampInt parses a query int with a default and inclusive [min,max] bounds.
func clampInt(raw string, def, min, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
