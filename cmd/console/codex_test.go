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
	"strings"
	"testing"
	"time"
)

func TestCodexSessionID(t *testing.T) {
	cases := map[string]string{
		"agent/codex-home/sessions/2026/07/23/rollout-2026-07-23T16-25-49-019f8fcc-27ff-72b1-9034-9a10dbc3ef57.jsonl": "019f8fcc-27ff-72b1-9034-9a10dbc3ef57",
		"rollout-abc.jsonl": "rollout-abc",
	}
	for input, want := range cases {
		if got := codexSessionID(input); got != want {
			t.Errorf("codexSessionID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSafeCodexName(t *testing.T) {
	cases := map[string]bool{
		"agent/codex-home/sessions/2026/07/23/rollout-abc.jsonl": true,
		"agent/codex-home/sessions/2026/08/06/file.jsonl":        true,
		"sessions/file.jsonl":                                    false,
		"agent/codex-home/sessions/../../etc/passwd":             false,
		"agent/codex-home/sessions/2026/07/23/file with space":   false,
		"": false,
	}
	for input, want := range cases {
		if got := safeCodexName(input); got != want {
			t.Errorf("safeCodexName(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestParseCodexSessionDerivesRunsAndTokens(t *testing.T) {
	now := time.Now().UTC()
	text := codexSessionLines("sess-abc", "turn-1", "deploy the stack", "gpt-5.6", "openai", now.Add(-5*time.Minute))

	resolvedID, runs, events, badLines := parseCodexSession("default", "fallback-id", text, now)
	if badLines != 0 {
		t.Fatalf("badLines = %d, want 0", badLines)
	}
	if resolvedID != "sess-abc" {
		t.Fatalf("resolvedID = %q, want sess-abc (from session_meta, not the fallback)", resolvedID)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	run := runs[0]
	if run.Agent != "default" {
		t.Fatalf("agent = %q, want default", run.Agent)
	}
	if run.SessionID != "sess-abc" {
		t.Fatalf("sessionID = %q, want sess-abc (from session_meta, not the fallback)", run.SessionID)
	}
	if run.RunID != "turn-1" {
		t.Fatalf("runID = %q, want turn-1", run.RunID)
	}
	if run.Outcome != "ok" {
		t.Fatalf("outcome = %q, want ok", run.Outcome)
	}
	if run.Source != "codex" {
		t.Fatalf("source = %q, want codex", run.Source)
	}
	if run.Model != "gpt-5.6" {
		t.Fatalf("model = %q, want gpt-5.6", run.Model)
	}
	if run.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", run.Provider)
	}
	if run.Tokens.Input != 100 || run.Tokens.Output != 50 || run.Tokens.Cache != 10 {
		t.Fatalf("tokens = %+v, want {Input:100 Output:50 Cache:10}", run.Tokens)
	}
	if run.Tokens.Total != 150 {
		t.Fatalf("total = %d, want 150 (input + output)", run.Tokens.Total)
	}
	if !strings.Contains(run.Prompt, "deploy the stack") {
		t.Fatalf("prompt = %q, want it to contain the user message", run.Prompt)
	}

	if len(events) != 1 || events[0].Type != "prompt.submitted" {
		t.Fatalf("events = %v, want one prompt.submitted for cross-session analysis", events)
	}
}

func TestParseCodexSessionUnfinishedRunMarkedRunning(t *testing.T) {
	now := time.Now().UTC()
	lines := []string{
		`{"timestamp":"` + iso(now.Add(-30*time.Second)) + `","type":"session_meta","payload":{"session_id":"s1","model_provider":"openai"}}`,
		`{"timestamp":"` + iso(now.Add(-30*time.Second)) + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"timestamp":"` + iso(now.Add(-20*time.Second)) + `","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
		`{"timestamp":"` + iso(now.Add(-10*time.Second)) + `","type":"event_msg","payload":{"type":"agent_message","message":"thinking..."}}`,
	}
	_, runs, _, _ := parseCodexSession("main", "s1", strings.Join(lines, "\n"), now)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if runs[0].Outcome != "running" {
		t.Fatalf("outcome = %q, want running (recent, not finished)", runs[0].Outcome)
	}
}

func TestParseCodexEventsForReplay(t *testing.T) {
	now := time.Now().UTC()
	text := codexSessionLines("sess-abc", "turn-1", "deploy", "gpt-5.6", "openai", now)

	events, sessionID, badLines := parseCodexEvents(text)
	if badLines != 0 {
		t.Fatalf("badLines = %d, want 0", badLines)
	}
	if sessionID != "sess-abc" {
		t.Fatalf("sessionID = %q, want sess-abc", sessionID)
	}
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	want := []string{"session.started", "prompt.submitted", "model.completed", "session.ended"}
	if len(types) != len(want) {
		t.Fatalf("event types = %v, want %v", types, want)
	}
	for i, w := range want {
		if types[i] != w {
			t.Fatalf("event[%d].Type = %q, want %q", i, types[i], w)
		}
	}
}

func TestScanDiscoversCodexSessionFiles(t *testing.T) {
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{
		"main": {"sess-1": {
			ev("session.started", evOpts{TS: iso(now.Add(-5 * time.Minute))}),
			ev("model.completed", evOpts{TS: iso(now.Add(-4 * time.Minute))}),
		}},
	})

	content := codexSessionLines("codex-sess-1", "turn-1", "hello world", "gpt-5.6", "openai", now.Add(-3*time.Minute))
	addCodexSession(t, root, "main", "rollout-2026-01-01T00-00-00-codex-sess-1xxxxxxxxxxxxxxxx.jsonl", content, now.Add(-3*time.Minute))

	snap := newStore(root, 0, nil).snapshot()
	if !snap.OK {
		t.Fatalf("snapshot not ok: %s", snap.Error)
	}
	if len(snap.Runs) != 2 {
		t.Fatalf("runs = %d, want 2 (1 trajectory + 1 codex)", len(snap.Runs))
	}
	var codexRun *Run
	for i := range snap.Runs {
		if snap.Runs[i].Source == "codex" {
			codexRun = &snap.Runs[i]
		}
	}
	if codexRun == nil {
		t.Fatal("no codex run found in snapshot")
	}
	if codexRun.SessionID != "codex-sess-1" {
		t.Fatalf("codex run sessionID = %q, want codex-sess-1", codexRun.SessionID)
	}
	if !strings.Contains(codexRun.Prompt, "hello world") {
		t.Fatalf("codex run prompt = %q, want it to contain the user message", codexRun.Prompt)
	}

	store := newStore(root, 0, nil)
	_ = store.snapshot()
	detail := store.sessionEvents("main", "codex-sess-1", 0, 100)
	if detail == nil {
		t.Fatal("sessionEvents must find the codex session by its resolved ID")
	}
	if detail.Source != "codex" {
		t.Fatalf("source = %q, want codex", detail.Source)
	}
}

func TestCodexActivityUpdatesAgentTimeline(t *testing.T) {
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{"bot": {}})

	content := codexSessionLines("s1", "t1", "hi", "gpt-5.6", "openai", now.Add(-time.Minute))
	addCodexSession(t, root, "bot", "rollout-test-s1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx.jsonl", content, now.Add(-time.Minute))

	snap := newStore(root, 0, nil).snapshot()
	activity := snap.Activity["bot"]
	if activity == 0 {
		t.Fatal("codex session should register as agent activity")
	}
}

func TestParseIndexOutputHandlesCodexLines(t *testing.T) {
	out := "A\tdefault\n" +
		"F\tdefault/sessions/s1.trajectory.jsonl\t10\t1700000000.123\n" +
		"X\tdefault/agent/codex-home/sessions/2026/07/23/rollout-abc.jsonl\t5000\t1700000001.456\n"
	agents, files, _ := parseIndexOutput(out)
	if len(agents) != 1 || agents[0] != "default" {
		t.Fatalf("agents = %v, want [default]", agents)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2 (1 old + 1 codex)", len(files))
	}
	old := files[0]
	if old.Name != "s1.trajectory.jsonl" || old.Codex {
		t.Fatalf("old file = %+v, want Name=s1.trajectory.jsonl Codex=false", old)
	}
	codex := files[1]
	if !codex.Codex || codex.Agent != "default" {
		t.Fatalf("codex file = %+v, want Codex=true Agent=default", codex)
	}
	if codex.Name != "agent/codex-home/sessions/2026/07/23/rollout-abc.jsonl" {
		t.Fatalf("codex name = %q, want the full sub-path from agent dir", codex.Name)
	}
	if codex.Size != 5000 {
		t.Fatalf("codex size = %d, want 5000", codex.Size)
	}
}

func TestParseIndexOutputRejectsUnsafeCodexPaths(t *testing.T) {
	out := "X\tdefault/agent/codex-home/sessions/../../etc/passwd\t100\t1700000000.000\n" +
		"X\t../escape/agent/codex-home/sessions/2026/07/23/file.jsonl\t100\t1700000000.000\n" +
		"X\tdefault/agent/codex-home/sessions/2026/07/23/rollout-ok.jsonl\t100\t1700000000.000\n"
	_, files, _ := parseIndexOutput(out)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1 (only the safe path should survive)", len(files))
	}
	if files[0].Name != "agent/codex-home/sessions/2026/07/23/rollout-ok.jsonl" {
		t.Fatalf("surviving file = %+v, want the safe path", files[0])
	}
}
