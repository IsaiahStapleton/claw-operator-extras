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
	"testing"
	"time"
)

func sessionOf(agent, sessionID string, lines ...string) Session {
	var events []Event
	for _, l := range lines {
		evs, _ := parseTrajectory(l)
		events = append(events, evs...)
	}
	return Session{Agent: agent, SessionID: sessionID, Events: events}
}

func TestFindHandoffsLinksSpawnToChildSessionStart(t *testing.T) {
	now := time.Now().UTC()
	spawn := ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
		"name": "spawn_agent", "arguments": map[string]any{"agent": "security"}}})
	childStart := ev("session.started", evOpts{TS: iso(now.Add(30 * time.Second))})

	edges := findHandoffs([]Session{
		sessionOf("main", "sess-parent", spawn),
		sessionOf("security", "sess-child", childStart),
	}, []string{"main", "security"})

	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].FromAgent != "main" || edges[0].ToAgent != "security" {
		t.Fatalf("edge = %s -> %s, want main -> security", edges[0].FromAgent, edges[0].ToAgent)
	}
	if edges[0].ToSessionID != "sess-child" {
		t.Fatalf("toSessionId = %q, want sess-child", edges[0].ToSessionID)
	}
}

func TestFindHandoffsIgnoresStartsOutsideTheWindow(t *testing.T) {
	now := time.Now().UTC()
	spawn := ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
		"name": "spawn_agent", "arguments": map[string]any{"agent": "security"}}})
	// Well past the 120s correlation window.
	lateStart := ev("session.started", evOpts{TS: iso(now.Add(10 * time.Minute))})

	edges := findHandoffs([]Session{
		sessionOf("main", "sess-parent", spawn),
		sessionOf("security", "sess-child", lateStart),
	}, []string{"main", "security"})

	if len(edges) != 0 {
		t.Fatalf("edges = %d, want 0 — a start outside the window is not a handoff", len(edges))
	}
}

func TestFindHandoffsGivesEachChildToTheEarliestSpawn(t *testing.T) {
	now := time.Now().UTC()
	firstSpawn := ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
		"name": "spawn_agent", "arguments": map[string]any{"agent": "security"}}})
	secondSpawn := ev("tool.call", evOpts{TS: iso(now.Add(10 * time.Second)), Data: map[string]any{
		"name": "spawn_agent", "arguments": map[string]any{"agent": "security"}}})
	childStart := ev("session.started", evOpts{TS: iso(now.Add(20 * time.Second))})

	edges := findHandoffs([]Session{
		sessionOf("main", "sess-a", firstSpawn),
		sessionOf("obs", "sess-b", secondSpawn),
		sessionOf("security", "sess-child", childStart),
	}, []string{"main", "obs", "security"})

	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1 — one child session can only be claimed once", len(edges))
	}
	if edges[0].FromSessionID != "sess-a" {
		t.Fatalf("claimed by %q, want sess-a (the earliest spawn)", edges[0].FromSessionID)
	}
}

func TestFindHandoffsResolvesTargetFromSerialisedArgsAndAskAgent(t *testing.T) {
	now := time.Now().UTC()

	t.Run("serialised args", func(t *testing.T) {
		spawn := ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
			"name": "handoff", "arguments": map[string]any{"note": "please page security now"}}})
		edges := findHandoffs([]Session{
			sessionOf("main", "sess-a", spawn),
			sessionOf("security", "sess-child", ev("session.started", evOpts{TS: iso(now.Add(time.Second))})),
		}, []string{"main", "security"})
		if len(edges) != 1 {
			t.Fatalf("edges = %d, want 1 (agent name found in serialised args)", len(edges))
		}
	})

	t.Run("bash ask-agent", func(t *testing.T) {
		spawn := ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
			"name": "bash", "arguments": map[string]any{"command": "ask-agent security 'run an audit'"}}})
		edges := findHandoffs([]Session{
			sessionOf("main", "sess-a", spawn),
			sessionOf("security", "sess-child", ev("session.started", evOpts{TS: iso(now.Add(time.Second))})),
		}, []string{"main", "security"})
		if len(edges) != 1 {
			t.Fatalf("edges = %d, want 1 (ask-agent in a bash command)", len(edges))
		}
	})
}

func TestFindHandoffsIgnoresSelfSpawnAndUnknownAgents(t *testing.T) {
	now := time.Now().UTC()
	selfSpawn := ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
		"name": "spawn_agent", "arguments": map[string]any{"agent": "main"}}})
	unknown := ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
		"name": "bash", "arguments": map[string]any{"command": "ask-agent ghost 'hi'"}}})

	edges := findHandoffs([]Session{
		sessionOf("main", "sess-a", selfSpawn, unknown),
		sessionOf("main", "sess-b", ev("session.started", evOpts{TS: iso(now.Add(time.Second))})),
	}, []string{"main"})

	if len(edges) != 0 {
		t.Fatalf("edges = %d, want 0 (self-spawn and unknown agents are not handoffs)", len(edges))
	}
}

// OpenClaw stamps delegated prompts with the originating session. That is a
// declared parent and must produce an edge without relying on timing.
// Marker shape observed in production.
func TestDeclaredSourceSessionProducesHandoff(t *testing.T) {
	now := time.Now().UTC()
	prompt := "[Inter-session message] sourceSession=agent:default:dashboard:c2a2bab7-e980-44fc-8254-35d3ff032725 " +
		"sourceChannel=dashboard\n\nRun the daily triage."

	edges := findHandoffs([]Session{
		sessionOf("default", "c2a2bab7-e980-44fc-8254-35d3ff032725",
			ev("session.started", evOpts{TS: iso(now.Add(-2 * time.Hour))})),
		sessionOf("stitch", "0f0b5adc-67af-4753-bede-3b848db84e31",
			ev("prompt.submitted", evOpts{TS: iso(now), Data: map[string]any{"prompt": prompt}})),
	}, []string{"default", "stitch"})

	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1 declared edge", len(edges))
	}
	e := edges[0]
	if e.FromAgent != "default" || e.ToAgent != "stitch" {
		t.Fatalf("edge = %s -> %s, want default -> stitch", e.FromAgent, e.ToAgent)
	}
	if e.FromSessionID != "c2a2bab7-e980-44fc-8254-35d3ff032725" {
		t.Fatalf("fromSessionId = %q, want the declared source session", e.FromSessionID)
	}
	if e.ToSessionID != "0f0b5adc-67af-4753-bede-3b848db84e31" {
		t.Fatalf("toSessionId = %q", e.ToSessionID)
	}
}

func TestDeclaredHandoffsIgnoreSelfDelegationAndUnknownAgents(t *testing.T) {
	now := time.Now().UTC()
	selfSub := "sourceSession=agent:stitch:subagent:1b79fb14-d666-4b9a-aa57-d80e60a26faf"
	unknown := "sourceSession=agent:ghost:dashboard:c2a2bab7-e980-44fc-8254-35d3ff032725"

	edges := findHandoffs([]Session{
		sessionOf("stitch", "sess-a", ev("prompt.submitted", evOpts{TS: iso(now),
			Data: map[string]any{"prompt": selfSub}})),
		sessionOf("stitch", "sess-b", ev("prompt.submitted", evOpts{TS: iso(now),
			Data: map[string]any{"prompt": unknown}})),
	}, []string{"stitch"})

	if len(edges) != 0 {
		t.Fatalf("edges = %v, want none (self-delegation and unknown agents are not fleet handoffs)", edges)
	}
}

// A declared parent is authoritative: the timing heuristic must not also claim
// the same child and produce a duplicate edge.
func TestDeclaredHandoffSuppressesInferredDuplicate(t *testing.T) {
	now := time.Now().UTC()
	prompt := "sourceSession=agent:default:dashboard:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	edges := findHandoffs([]Session{
		sessionOf("main", "sess-parent", ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
			"name": "spawn_agent", "arguments": map[string]any{"agent": "stitch"}}})),
		sessionOf("stitch", "sess-child",
			ev("session.started", evOpts{TS: iso(now.Add(10 * time.Second))}),
			ev("prompt.submitted", evOpts{TS: iso(now.Add(11 * time.Second)),
				Data: map[string]any{"prompt": prompt}})),
	}, []string{"main", "stitch", "default"})

	if len(edges) != 1 {
		t.Fatalf("edges = %d, want exactly 1 — the declared parent wins", len(edges))
	}
	if edges[0].FromAgent != "default" {
		t.Fatalf("fromAgent = %q, want the declared parent 'default', not the inferred 'main'", edges[0].FromAgent)
	}
}

func TestExtractMemoryWritesDetectsToolWritesAndSkipsReads(t *testing.T) {
	now := time.Now().UTC()
	sessions := []Session{sessionOf("librarian", "sess-1",
		ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
			"name":      "write",
			"arguments": map[string]any{"path": "memory-map/tasks/audit.md", "content": "# audit\nall clear"}}}),
		ev("tool.call", evOpts{TS: iso(now.Add(time.Second)), Data: map[string]any{
			"name":      "read",
			"arguments": map[string]any{"path": "memory-map/tasks/audit.md"}}}),
		ev("tool.call", evOpts{TS: iso(now.Add(2 * time.Second)), Data: map[string]any{
			"name":      "bash",
			"arguments": map[string]any{"command": "cat memory-map/tasks/audit.md"}}}),
		ev("tool.call", evOpts{TS: iso(now.Add(3 * time.Second)), Data: map[string]any{
			"name":      "bash",
			"arguments": map[string]any{"command": "echo done >> memory-map/tasks/audit.md"}}}),
		ev("tool.call", evOpts{TS: iso(now.Add(4 * time.Second)), Data: map[string]any{
			"name":      "write",
			"arguments": map[string]any{"path": "/etc/motd", "content": "not the vault"}}}),
	)}

	writes := extractMemoryWrites(sessions)
	if len(writes) != 2 {
		t.Fatalf("writes = %d, want 2 (the write tool and the appending bash command)", len(writes))
	}
	for _, w := range writes {
		if w.NotePath != "memory-map/tasks/audit.md" {
			t.Fatalf("notePath = %q, want the vault path", w.NotePath)
		}
		if w.Agent != "librarian" {
			t.Fatalf("agent = %q, want librarian", w.Agent)
		}
	}
	// Newest first.
	if writes[0].Tool != "bash" {
		t.Fatalf("first write tool = %q, want bash (newest first)", writes[0].Tool)
	}
}

func TestExtractMemoryWritesIgnoresNonVaultAndNonWriteTools(t *testing.T) {
	now := time.Now().UTC()
	sessions := []Session{sessionOf("main", "sess-1",
		ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
			"name": "grep", "arguments": map[string]any{"path": "memory-map/notes.md"}}}),
		ev("tool.result", evOpts{TS: iso(now), Data: map[string]any{"output": "memory-map/notes.md"}}),
	)}

	if writes := extractMemoryWrites(sessions); len(writes) != 0 {
		t.Fatalf("writes = %d, want 0 (reads and non-tool.call events are not writes)", len(writes))
	}
}
