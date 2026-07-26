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

// Shared fixtures: build a fake agent data directory whose layout mirrors the
// real one (<root>/<agent>/sessions/<sessionId>.trajectory.jsonl).

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var fixtureSeq int

// evOpts are the fields a test wants to override on a synthetic event.
type evOpts struct {
	TS      string
	RunID   string
	Seq     int
	Model   string
	Provide string
	Data    map[string]any
}

// ev builds one trajectory event line, mirroring the openclaw-trajectory
// schemaVersion 1 shape.
func ev(typ string, o evOpts) string {
	fixtureSeq++
	obj := map[string]any{
		"traceSchema":   "openclaw-trajectory",
		"schemaVersion": 1,
		"source":        "runtime",
		"type":          typ,
		"ts":            orDefault(o.TS, time.Now().UTC().Format(time.RFC3339Nano)),
		"seq":           orDefaultInt(o.Seq, fixtureSeq),
		"sessionId":     "sess-1",
		"runId":         orDefault(o.RunID, "run-1"),
		"sessionKey":    "agent:main:main",
		"provider":      orDefault(o.Provide, "openai"),
		"modelId":       orDefault(o.Model, "gpt-5.5"),
		"data":          orDefaultMap(o.Data),
	}
	b, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDefaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func orDefaultMap(v map[string]any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

// makeDataDir builds a Claw-home-shaped tree and returns its agents dir, so
// the memory stores that sit beside it (workspace/memory, workspace/wiki) are
// reachable exactly as they are in a pod.
func makeDataDir(t *testing.T, agents map[string]map[string][]string) string {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "agents")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for agent, sessions := range agents {
		dir := filepath.Join(root, agent, "sessions")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		for sessionID, lines := range sessions {
			var text string
			for _, l := range lines {
				text += l + "\n"
			}
			file := filepath.Join(dir, sessionID+".trajectory.jsonl")
			if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
	}
	return root
}

// addFile writes an arbitrary file beside the trajectories with an explicit
// mtime — the activity signal for backends that write no trajectory sidecar.
func addFile(t *testing.T, root, agent, name, content string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, agent, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chtimes(file, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return file
}

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// addNote writes a durable memory note beside the agents dir, mirroring
// OpenClaw's layout: <claw-home>/workspace/memory/..., wiki, or an agent's own
// memory directory.
func addNote(t *testing.T, agentsDir, relPath, content string, mtime time.Time) {
	t.Helper()
	home := filepath.Dir(agentsDir)
	file := filepath.Join(home, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chtimes(file, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}
