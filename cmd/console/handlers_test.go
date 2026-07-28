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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testServer wires a server in local-directory mode over a fixture tree, with
// no snapshot caching so each request re-reads.
func testServer(t *testing.T, root string) *server {
	t.Helper()
	return &server{
		localDir:  root,
		hostname:  "test-host",
		cacheTTL:  0,
		agentMeta: map[string]AgentMeta{"main": {Emoji: "🌱", Title: "Podling", Desc: "Coordinator"}},
		stores:    map[string]*Store{},
	}
}

// fixtureRoot builds a two-agent tree with a handoff, a memory write, a
// running run, and a transcript-only session.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{
		"main": {"sess-parent": {
			ev("session.started", evOpts{TS: iso(now.Add(-20 * time.Minute))}),
			ev("prompt.submitted", evOpts{TS: iso(now.Add(-19 * time.Minute)),
				Data: map[string]any{"prompt": "investigate the gateway crash"}}),
			ev("tool.call", evOpts{TS: iso(now.Add(-18 * time.Minute)), Data: map[string]any{
				"name": "spawn_agent", "arguments": map[string]any{"agent": "security"}}}),
			ev("tool.call", evOpts{TS: iso(now.Add(-17 * time.Minute)), Data: map[string]any{
				"name":      "write",
				"arguments": map[string]any{"path": "memory-map/tasks/gateway.md", "content": "OOMKilled"}}}),
			ev("model.completed", evOpts{TS: iso(now.Add(-16 * time.Minute)),
				Data: map[string]any{"usage": map[string]any{"input": float64(100), "output": float64(50)}}}),
		}},
		"security": {"sess-child": {
			ev("session.started", evOpts{TS: iso(now.Add(-18*time.Minute + 30*time.Second))}),
			ev("prompt.submitted", evOpts{TS: iso(now.Add(-17 * time.Minute)),
				Data: map[string]any{"prompt": "audit the cluster"}}),
			// No terminal event and recent => running.
			ev("tool.call", evOpts{TS: iso(now.Add(-30 * time.Second)), Data: map[string]any{
				"name": "bash", "arguments": map[string]any{"command": "oc get pods"}}}),
		}},
	})
	return root
}

func getJSON(t *testing.T, s *server, method, target string, h func(http.ResponseWriter, *http.Request)) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return rec.Code, body
}

func TestHandleAgentsReportsStatusAndResolvedMeta(t *testing.T) {
	s := testServer(t, fixtureRoot(t))
	code, body := getJSON(t, s, "GET", "/api/agents", s.handleAgents)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	agents, _ := body["agents"].([]any)
	if len(agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(agents))
	}
	byName := map[string]map[string]any{}
	for _, a := range agents {
		m := a.(map[string]any)
		byName[m["name"].(string)] = m
	}

	if got := byName["security"]["status"]; got != "active" {
		t.Fatalf("security status = %v, want active (its latest run is running)", got)
	}
	if step, _ := byName["security"]["currentStep"].(string); !strings.Contains(step, "oc get pods") {
		t.Fatalf("currentStep = %q, want the last event of the running run", step)
	}
	// AGENT_META supplies display fields; unlisted agents get sane defaults.
	if got := byName["main"]["title"]; got != "Podling" {
		t.Fatalf("main title = %v, want Podling from AGENT_META", got)
	}
	if got := byName["security"]["title"]; got != "Security" {
		t.Fatalf("security title = %v, want the humanized default", got)
	}
	if got := byName["security"]["emoji"]; got != "🤖" {
		t.Fatalf("security emoji = %v, want the default", got)
	}

	data := body["data"].(map[string]any)
	if data["ok"] != true {
		t.Fatalf("data.ok = %v, want true", data["ok"])
	}
}

func TestHandleRunsAttachesHandoffEdges(t *testing.T) {
	s := testServer(t, fixtureRoot(t))
	_, body := getJSON(t, s, "GET", "/api/runs", s.handleRuns)

	runs := body["runs"].([]any)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	var parent, child map[string]any
	for _, r := range runs {
		m := r.(map[string]any)
		switch m["sessionId"] {
		case "sess-parent":
			parent = m
		case "sess-child":
			child = m
		}
	}
	if parent == nil || child == nil {
		t.Fatal("both fixture runs should be present")
	}
	if parent["handoffsOut"].(float64) != 1 {
		t.Fatalf("parent handoffsOut = %v, want 1", parent["handoffsOut"])
	}
	spawnedBy, ok := child["spawnedBy"].(map[string]any)
	if !ok {
		t.Fatal("child run should carry a spawnedBy edge")
	}
	if spawnedBy["fromAgent"] != "main" {
		t.Fatalf("spawnedBy.fromAgent = %v, want main", spawnedBy["fromAgent"])
	}
}

func TestHandleRunsFilters(t *testing.T) {
	s := testServer(t, fixtureRoot(t))

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"by agent", "?agent=security", 1},
		{"by outcome", "?outcome=running", 1},
		{"by prompt substring, case-insensitive", "?q=GATEWAY", 1},
		{"combined filters that exclude each other", "?agent=main&outcome=running", 0},
		{"since the future", "?since=2099-01-01T00:00:00Z", 0},
		{"limit caps the page but not the total", "?limit=1", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, body := getJSON(t, s, "GET", "/api/runs"+tc.query, s.handleRuns)
			runs := body["runs"].([]any)
			if len(runs) != tc.want {
				t.Fatalf("runs = %d, want %d", len(runs), tc.want)
			}
		})
	}

	_, body := getJSON(t, s, "GET", "/api/runs?limit=1", s.handleRuns)
	if body["total"].(float64) != 2 {
		t.Fatalf("total = %v, want 2 (the unpaged count)", body["total"])
	}
}

func TestHandleRunDetailReturnsTrajectoryWithLineage(t *testing.T) {
	s := testServer(t, fixtureRoot(t))
	req := httptest.NewRequest("GET", "/api/runs/security/sess-child", nil)
	rec := httptest.NewRecorder()
	s.handleRunDetail(rec, req, "security", "sess-child")

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body["source"] != "trajectory" {
		t.Fatalf("source = %v, want trajectory", body["source"])
	}
	if body["parent"] == nil {
		t.Fatal("the child session should carry its parent handoff edge")
	}
	events := body["events"].([]any)
	if len(events) == 0 {
		t.Fatal("events should not be empty")
	}
	first := events[0].(map[string]any)
	for _, k := range []string{"ts", "seq", "type", "summary", "data"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("event is missing %q — the SPA replay view depends on it", k)
		}
	}
}

func TestHandleRunDetailFallsBackToTranscript(t *testing.T) {
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{"cli": {}})
	addFile(t, root, "cli", "sess-cli.jsonl",
		`{"type":"message","timestamp":"`+iso(now)+`","message":{"role":"user","content":"hello"}}`+"\n"+
			`{"type":"message","timestamp":"`+iso(now)+`","message":{"role":"assistant","content":"hi"}}`+"\n", now)
	s := testServer(t, root)

	req := httptest.NewRequest("GET", "/api/runs/cli/sess-cli", nil)
	rec := httptest.NewRecorder()
	s.handleRunDetail(rec, req, "cli", "sess-cli")

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["source"] != "transcript" {
		t.Fatalf("source = %v, want transcript", body["source"])
	}
	events := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 messages", len(events))
	}
	if role := events[0].(map[string]any)["role"]; role != "user" {
		t.Fatalf("first message role = %v, want user", role)
	}
}

func TestHandleRunDetailMissingSessionIs404(t *testing.T) {
	s := testServer(t, fixtureRoot(t))
	req := httptest.NewRequest("GET", "/api/runs/main/nope", nil)
	rec := httptest.NewRecorder()
	s.handleRunDetail(rec, req, "main", "nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleRunTailReturnsOnlyNewEvents(t *testing.T) {
	s := testServer(t, fixtureRoot(t))

	req := httptest.NewRequest("GET", "/api/runs/main/sess-parent/tail?after=0", nil)
	rec := httptest.NewRecorder()
	s.handleRunTail(rec, req, "main", "sess-parent")
	var full map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &full)
	total := int(full["total"].(float64))

	req = httptest.NewRequest("GET", "/api/runs/main/sess-parent/tail?after=3", nil)
	rec = httptest.NewRecorder()
	s.handleRunTail(rec, req, "main", "sess-parent")
	var tail map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &tail)

	if int(tail["total"].(float64)) != total {
		t.Fatalf("tail total = %v, want the full count %d", tail["total"], total)
	}
	if got := len(tail["events"].([]any)); got != total-3 {
		t.Fatalf("tail events = %d, want %d (only what follows the offset)", got, total-3)
	}
}

func TestHandleMemoryAndHandoffs(t *testing.T) {
	s := testServer(t, fixtureRoot(t))

	_, mem := getJSON(t, s, "GET", "/api/memory", s.handleMemory)
	writes := mem["writes"].([]any)
	if len(writes) != 1 {
		t.Fatalf("memory writes = %d, want 1", len(writes))
	}
	if p := writes[0].(map[string]any)["notePath"]; p != "memory-map/tasks/gateway.md" {
		t.Fatalf("notePath = %v", p)
	}

	_, ho := getJSON(t, s, "GET", "/api/handoffs", s.handleHandoffs)
	if len(ho["handoffs"].([]any)) != 1 {
		t.Fatalf("handoffs = %v, want 1", ho["handoffs"])
	}
}

func TestHandleHealthReportsDisabledGatewayExplicitly(t *testing.T) {
	s := testServer(t, fixtureRoot(t)) // no GATEWAY_URL configured
	_, body := getJSON(t, s, "GET", "/api/health", s.handleHealth)

	gw := body["gateway"].(map[string]any)
	if gw["status"] != "disabled" {
		t.Fatalf("gateway status = %v, want disabled when no URL is set", gw["status"])
	}
	if body["staleAfterMs"].(float64) != float64(staleAfter.Milliseconds()) {
		t.Fatalf("staleAfterMs = %v, want %d", body["staleAfterMs"], staleAfter.Milliseconds())
	}
	if body["hostname"] != "test-host" {
		t.Fatalf("hostname = %v", body["hostname"])
	}
}

func TestUnreadableDataDirSurfacesInEveryListResponse(t *testing.T) {
	s := testServer(t, t.TempDir()+"/missing")

	for name, h := range map[string]func(http.ResponseWriter, *http.Request){
		"agents": s.handleAgents,
		"runs":   s.handleRuns,
		"memory": s.handleMemory,
	} {
		t.Run(name, func(t *testing.T) {
			_, body := getJSON(t, s, "GET", "/api/"+name, h)
			data := body["data"].(map[string]any)
			if data["ok"] != false {
				t.Fatalf("data.ok = %v, want false so the UI can show the danger banner", data["ok"])
			}
			if data["error"] == "" {
				t.Fatal("data.error should explain why the scan failed")
			}
		})
	}
}

func TestHandleMetricsEmitsPrometheusText(t *testing.T) {
	s := testServer(t, fixtureRoot(t))
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q, want Prometheus text format", ct)
	}
	out := rec.Body.String()
	for _, want := range []string{
		"# HELP agent_console_runs_total",
		"# TYPE agent_console_runs_total gauge",
		`agent_console_runs_total{agent="main",outcome="ok"} 1`,
		`agent_console_active_runs{agent="security"} 1`,
		"agent_console_data_ok 1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics output missing %q\n---\n%s", want, out)
		}
	}
}

func TestMetricsReportDataNotOkWhenScanFails(t *testing.T) {
	s := testServer(t, t.TempDir()+"/missing")
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "agent_console_data_ok 0") {
		t.Fatal("a failed scan must be visible in metrics as data_ok 0")
	}
}

func TestEscapeLabelQuotesAreSafe(t *testing.T) {
	snap := &Snapshot{OK: true, Runs: []Run{{
		Agent: `we"ird`, Outcome: "ok", Model: "m", StartedAt: "", LastEventAt: "",
	}}}
	out := renderMetrics(snap)
	if !strings.Contains(out, `agent="we\"ird"`) {
		t.Fatalf("label quotes must be escaped, got:\n%s", out)
	}
}

func TestClampInt(t *testing.T) {
	cases := []struct {
		raw           string
		def, min, max int
		want          int
	}{
		{"", 50, 1, 500, 50},
		{"abc", 50, 1, 500, 50},
		{"0", 50, 1, 500, 1},
		{"9999", 50, 1, 500, 500},
		{"-5", 0, 0, 100, 0},
		{"25", 50, 1, 500, 25},
	}
	for _, tc := range cases {
		if got := clampInt(tc.raw, tc.def, tc.min, tc.max); got != tc.want {
			t.Fatalf("clampInt(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestParseAgentMetaIgnoresInvalidJSON(t *testing.T) {
	if got := parseAgentMeta("not json"); len(got) != 0 {
		t.Fatalf("invalid AGENT_META should yield an empty map, got %v", got)
	}
	got := parseAgentMeta(`{"main":{"emoji":"🌱","title":"Podling","desc":"Coordinator"}}`)
	if got["main"].Title != "Podling" {
		t.Fatalf("parsed meta = %+v", got)
	}
}
