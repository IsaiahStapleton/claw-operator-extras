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
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanCollectsAgentsRunsAndIntegrityCounters(t *testing.T) {
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{
		"main": {"sess-1": {
			ev("session.started", evOpts{TS: iso(now.Add(-5 * time.Minute))}),
			ev("model.completed", evOpts{TS: iso(now.Add(-4 * time.Minute))}),
			"{broken",
		}},
		"security": {"sess-2": {
			ev("session.started", evOpts{TS: iso(now.Add(-3 * time.Minute))}),
		}},
	})

	snap := newStore(root, 0, nil).snapshot()
	if !snap.OK {
		t.Fatalf("snapshot not ok: %s", snap.Error)
	}
	if len(snap.Agents) != 2 {
		t.Fatalf("agents = %v, want 2", snap.Agents)
	}
	if len(snap.Runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(snap.Runs))
	}
	if snap.BadLines != 1 {
		t.Fatalf("badLines = %d, want 1 (counted, never hidden)", snap.BadLines)
	}
	if snap.ScannedFiles != 2 {
		t.Fatalf("scannedFiles = %d, want 2", snap.ScannedFiles)
	}
}

func TestScanReportsUnreadableDataDirRatherThanFabricating(t *testing.T) {
	snap := newStore(filepath.Join(t.TempDir(), "does-not-exist"), 0, nil).snapshot()
	if snap.OK {
		t.Fatal("snapshot should be not-ok when the data dir is unreadable")
	}
	if snap.Error == "" {
		t.Fatal("snapshot must carry an error string so the UI can surface it")
	}
	if len(snap.Runs) != 0 || len(snap.Agents) != 0 {
		t.Fatal("an unreadable data dir must yield nothing, not partial data")
	}
}

func TestScanSkipsExcludedAgents(t *testing.T) {
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{
		"main":  {"sess-1": {ev("session.started", evOpts{TS: iso(now)})}},
		"noisy": {"sess-2": {ev("session.started", evOpts{TS: iso(now)})}},
	})

	snap := newStore(root, 0, []string{"noisy", ""}).snapshot()
	if len(snap.Agents) != 1 || snap.Agents[0] != "main" {
		t.Fatalf("agents = %v, want [main]", snap.Agents)
	}
}

func TestScanIgnoresAgentDirWithoutSessions(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	snap := newStore(root, 0, nil).snapshot()
	if !snap.OK {
		t.Fatalf("an agent dir without sessions/ is not an error, got %q", snap.Error)
	}
	if len(snap.Agents) != 0 {
		t.Fatalf("agents = %v, want none", snap.Agents)
	}
}

func TestTranscriptWithoutTrajectoryYieldsDerivedRun(t *testing.T) {
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{"cli": {}})
	transcript := `{"type":"session","timestamp":"` + iso(now.Add(-10*time.Minute)) + `"}
{"type":"message","timestamp":"` + iso(now.Add(-9*time.Minute)) + `","message":{"role":"user","content":"deploy the stack"}}
{"type":"message","timestamp":"` + iso(now.Add(-8*time.Minute)) + `","message":{"role":"assistant","content":"done"}}
`
	addFile(t, root, "cli", "sess-cli.jsonl", transcript, now)

	snap := newStore(root, 0, nil).snapshot()
	if len(snap.Runs) != 1 {
		t.Fatalf("runs = %d, want 1 derived from the transcript", len(snap.Runs))
	}
	run := snap.Runs[0]
	if run.Source != "transcript" {
		t.Fatalf("source = %q, want transcript", run.Source)
	}
	if run.Steps != 2 {
		t.Fatalf("steps = %d, want 2 (message count)", run.Steps)
	}
	if run.Prompt != "deploy the stack" {
		t.Fatalf("prompt = %q, want the first user message", run.Prompt)
	}
}

func TestTrajectorySidecarWinsOverTranscriptForTheSameSession(t *testing.T) {
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{
		"main": {"sess-1": {
			ev("session.started", evOpts{TS: iso(now.Add(-2 * time.Minute))}),
			ev("model.completed", evOpts{TS: iso(now.Add(-1 * time.Minute))}),
		}},
	})
	addFile(t, root, "main", "sess-1.jsonl",
		`{"type":"message","timestamp":"`+iso(now)+`","message":{"role":"user","content":"x"}}`+"\n", now)

	snap := newStore(root, 0, nil).snapshot()
	if len(snap.Runs) != 1 {
		t.Fatalf("runs = %d, want 1 — the transcript must not double-count", len(snap.Runs))
	}
	if snap.Runs[0].Source == "transcript" {
		t.Fatal("the trajectory sidecar should win when both exist")
	}
}

func TestActivityUsesTranscriptMtimeAndIgnoresSessionsJSON(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Minute)
	root := makeDataDir(t, map[string]map[string][]string{"cli": {}})
	// sessions.json is swept by the gateway for every agent at once, so its
	// mtime is not agent activity.
	addFile(t, root, "cli", "sessions.json", "{}\n", now)
	addFile(t, root, "cli", "sess-x.jsonl", `{"type":"message","message":{"role":"user","content":"hi"}}`+"\n", recent)

	snap := newStore(root, 0, nil).snapshot()
	got := snap.Activity["cli"]
	if got == 0 {
		t.Fatal("activity should be set from the transcript mtime")
	}
	if delta := got - recent.UnixMilli(); delta > 1500 || delta < -1500 {
		t.Fatalf("activity = %d, want ~%d (the transcript mtime, not sessions.json)", got, recent.UnixMilli())
	}
}

func TestSessionEventsRejectsPathTraversal(t *testing.T) {
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{
		"main": {"sess-1": {ev("session.started", evOpts{TS: iso(now)})}},
	})
	store := newStore(root, 0, nil)

	for _, tc := range []struct{ agent, session string }{
		{"../etc", "sess-1"},
		{"main", "../../secret"},
		{"main/..", "sess-1"},
		{"main", "sess 1"},
	} {
		if got := store.sessionEvents(tc.agent, tc.session, 0, 10); got != nil {
			t.Fatalf("sessionEvents(%q, %q) must be rejected", tc.agent, tc.session)
		}
	}

	if got := store.sessionEvents("main", "sess-1", 0, 10); got == nil {
		t.Fatal("a legitimate session must still be readable")
	}
}

func TestSessionEventsPaginates(t *testing.T) {
	now := time.Now().UTC()
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, ev("tool.call", evOpts{TS: iso(now.Add(time.Duration(i) * time.Second))}))
	}
	root := makeDataDir(t, map[string]map[string][]string{"main": {"sess-1": lines}})
	store := newStore(root, 0, nil)

	page := store.sessionEvents("main", "sess-1", 3, 4)
	if page.Total != 10 {
		t.Fatalf("total = %d, want 10 (the full count, not the page size)", page.Total)
	}
	if len(page.Events) != 4 {
		t.Fatalf("page size = %d, want 4", len(page.Events))
	}

	if past := store.sessionEvents("main", "sess-1", 50, 4); len(past.Events) != 0 || past.Total != 10 {
		t.Fatalf("an offset past the end should yield no events but keep the total")
	}
}

func TestSnapshotCachesWithinTTL(t *testing.T) {
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{
		"main": {"sess-1": {ev("session.started", evOpts{TS: iso(now)})}},
	})
	store := newStore(root, time.Minute, nil)
	first := store.snapshot()

	// A new agent appearing mid-TTL must not show up until the cache expires.
	makeDataDirInto(t, root, "late", "sess-2", ev("session.started", evOpts{TS: iso(now)}))
	if second := store.snapshot(); len(second.Agents) != len(first.Agents) {
		t.Fatalf("snapshot should be cached within the TTL, agents went %v -> %v", first.Agents, second.Agents)
	}

	store.cacheTTL = 0
	if third := store.snapshot(); len(third.Agents) != 2 {
		t.Fatalf("after the TTL expires the rescan should see both agents, got %v", third.Agents)
	}
}

func makeDataDirInto(t *testing.T, root, agent, sessionID, line string) {
	t.Helper()
	dir := filepath.Join(root, agent, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".trajectory.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
