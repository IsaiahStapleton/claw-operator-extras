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
			"arguments": map[string]any{"path": "workspace/memory/2026-07-26.md", "content": "# notes\nall clear"}}}),
		ev("tool.call", evOpts{TS: iso(now.Add(time.Second)), Data: map[string]any{
			"name":      "read",
			"arguments": map[string]any{"path": "workspace/memory/2026-07-26.md"}}}),
		ev("tool.call", evOpts{TS: iso(now.Add(2 * time.Second)), Data: map[string]any{
			"name":      "bash",
			"arguments": map[string]any{"command": "cat workspace/memory/2026-07-26.md"}}}),
		ev("tool.call", evOpts{TS: iso(now.Add(3 * time.Second)), Data: map[string]any{
			"name":      "bash",
			"arguments": map[string]any{"command": "echo done >> workspace/memory/2026-07-26.md"}}}),
		ev("tool.call", evOpts{TS: iso(now.Add(4 * time.Second)), Data: map[string]any{
			"name":      "write",
			"arguments": map[string]any{"path": "/etc/motd", "content": "not the vault"}}}),
	)}

	writes := extractMemoryWrites(sessions)
	if len(writes) != 2 {
		t.Fatalf("writes = %d, want 2 (the write tool and the appending bash command)", len(writes))
	}
	for _, w := range writes {
		if w.NotePath != "memory/2026-07-26.md" {
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
		ev("tool.result", evOpts{TS: iso(now), Data: map[string]any{"output": "workspace/memory/notes.md"}}),
	)}

	if writes := extractMemoryWrites(sessions); len(writes) != 0 {
		t.Fatalf("writes = %d, want 0 (reads and non-tool.call events are not writes)", len(writes))
	}
}

// The wiki and per-agent "dreaming" notes are memory too — all three of
// OpenClaw's stores live under a memory/ or wiki/ path segment.
func TestVaultPatternCoversEveryOpenClawMemoryStore(t *testing.T) {
	for _, path := range []string{
		"workspace/memory/2026-07-26.md", // shared daily notes
		"/home/node/.openclaw/workspace/memory/preferences.md",
		"/home/node/.openclaw/workspace/wiki/main/concepts/agents.md", // memory wiki
		"stitch/memory/dreaming/deep/2026-07-24.md",                   // per-agent consolidation
	} {
		if !vaultPathRE.MatchString(path) {
			t.Errorf("vault pattern should match %q — it is one of OpenClaw's memory stores", path)
		}
	}
	for _, path := range []string{"src/main.go", "workspace/AGENTS.md", "notes.txt"} {
		if vaultPathRE.MatchString(path) {
			t.Errorf("vault pattern should not match %q", path)
		}
	}
}

// A deployment that keeps its vault elsewhere can say so; the home-server
// convention is memory-map/.
func TestVaultPatternIsOverridable(t *testing.T) {
	re := compileVaultPattern(`memory-map/[\w\-./]*\.md`)
	if !re.MatchString("memory-map/tasks/audit.md") {
		t.Fatal("override pattern should match its own convention")
	}
	if got := compileVaultPattern("([unclosed"); got.String() != defaultVaultPattern {
		t.Fatal("an invalid override must fall back to the default, not panic")
	}
}

// Native memory tools mutate the store without naming a file.
func TestNativeMemoryToolCountsAsAWrite(t *testing.T) {
	now := time.Now().UTC()
	sessions := []Session{sessionOf("podling", "sess-1",
		ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
			"name":      "memory_store",
			"arguments": map[string]any{"title": "deployment decision", "content": "chose exec over PVC mounts"}}}),
		ev("tool.call", evOpts{TS: iso(now), Data: map[string]any{
			"name": "memory_search", "arguments": map[string]any{"query": "deployment"}}}),
	)}

	writes := extractMemoryWrites(sessions)
	if len(writes) != 1 {
		t.Fatalf("writes = %d, want 1 — memory_store writes, memory_search only reads", len(writes))
	}
	if writes[0].NotePath != "deployment decision" {
		t.Fatalf("notePath = %q, want the note title", writes[0].NotePath)
	}
	if writes[0].Content == "" {
		t.Fatal("the stored content should be captured")
	}
}

// Handoff edges alone understate a fleet badly: OpenClaw stamps a parent onto a
// delegated prompt on one delivery path only, so a Claw whose agents genuinely
// collaborate reports a couple of edges against hundreds of sessions. The
// origin summary is what lets the page say where work actually came from
// instead of implying the agents worked alone.
func TestSessionOriginsGroupByAgentTriggerAndKind(t *testing.T) {
	mk := func(agent, key, trigger string) Session {
		return Session{Agent: agent, SessionID: agent + key, Events: []Event{{
			Type: "session.started", TS: "2026-07-26T10:00:00Z", SessionKey: key,
			Data: map[string]any{"trigger": trigger},
		}}}
	}
	origins := sessionOrigins([]Session{
		mk("default", "agent:default:cron:abc:run:def", "cron"),
		mk("default", "agent:default:cron:abc:run:ghi", "cron"),
		mk("default", "agent:default:main", "user"),
		mk("stitch", "agent:stitch:stitch-daily-2026-07-26", "user"),
		{Agent: "quill", SessionID: "no-start"},
	})

	got := map[string]SessionOrigin{}
	for _, o := range origins {
		got[o.Agent+"/"+o.Trigger+"/"+o.Kind] = o
	}
	if o := got["default/cron/cron"]; o.Sessions != 2 {
		t.Fatalf("cron sessions = %d, want 2; origins=%+v", o.Sessions, origins)
	}
	if _, ok := got["default/user/main"]; !ok {
		t.Fatalf("missing the main session; origins=%+v", origins)
	}
	// A caller-chosen session key is reported as "named": the distinction that
	// matters is whether the runtime named the session or something outside did.
	if _, ok := got["stitch/user/named"]; !ok {
		t.Fatalf("a caller-named session key should group as 'named'; origins=%+v", origins)
	}
	// A session whose start was never recorded is counted, not dropped.
	if _, ok := got["quill/unrecorded/unknown"]; !ok {
		t.Fatalf("a session with no session.started must still be reported; origins=%+v", origins)
	}
	// Biggest group first, so the page leads with where the work really comes from.
	if origins[0].Sessions < origins[len(origins)-1].Sessions {
		t.Fatalf("origins are not ordered by size: %+v", origins)
	}
}

// The session key a message is stamped with does not have to end in a uuid.
// Requiring one matched only the coordinator's dashboard sessions and dropped
// every message an agent sent from its own main session — so a chain of
// podling -> quill -> stitch rendered as podling talking to each of them and
// quill talking to nobody.
func TestHandoffFromANonUUIDSessionKey(t *testing.T) {
	now := time.Now().UTC()
	msg := func(from string) string {
		return "[Inter-session message] sourceSession=agent:" + from + ":main " +
			"sourceChannel=unknown sourceTool=sessions_send isUser=false\n\nAsk about project state."
	}
	quill := sessionOf("quill", "11111111-1111-1111-1111-111111111111",
		ev("session.started", evOpts{TS: iso(now.Add(-time.Hour))}))
	// The runtime names quill's session "agent:quill:main"; that is what the
	// stamp on stitch's prompt refers to.
	for i := range quill.Events {
		quill.Events[i].SessionKey = "agent:quill:main"
	}
	stitch := sessionOf("stitch", "22222222-2222-2222-2222-222222222222",
		ev("prompt.submitted", evOpts{TS: iso(now), Data: map[string]any{"prompt": msg("quill")}}))

	edges := findHandoffs([]Session{quill, stitch}, []string{"default", "quill", "stitch"})
	if len(edges) != 1 {
		t.Fatalf("edges = %+v, want quill -> stitch", edges)
	}
	e := edges[0]
	if e.FromAgent != "quill" || e.ToAgent != "stitch" {
		t.Fatalf("edge = %s -> %s, want quill -> stitch", e.FromAgent, e.ToAgent)
	}
	// The key resolves back to the session that holds it, so the edge links to
	// something the reader can open.
	if e.FromSessionID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("fromSessionId = %q, want quill's session", e.FromSessionID)
	}
	if e.Tool != "sessions_send" {
		t.Fatalf("tool = %q, want the tool that carried it", e.Tool)
	}
}

// Two agents exchanging several messages is a different fact from exchanging
// one, and the topology sizes a link by how many edges it carries.
func TestEachMessageIsItsOwnEdge(t *testing.T) {
	now := time.Now().UTC()
	msg := "[Inter-session message] sourceSession=agent:quill:main sourceTool=sessions_send\n\nping"
	quill := sessionOf("quill", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		ev("session.started", evOpts{TS: iso(now.Add(-time.Hour))}))
	for i := range quill.Events {
		quill.Events[i].SessionKey = "agent:quill:main"
	}
	stitch := sessionOf("stitch", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		ev("prompt.submitted", evOpts{TS: iso(now), Data: map[string]any{"prompt": msg}}),
		ev("prompt.submitted", evOpts{TS: iso(now.Add(time.Minute)), Data: map[string]any{"prompt": msg}}),
		ev("prompt.submitted", evOpts{TS: iso(now.Add(2 * time.Minute)), Data: map[string]any{"prompt": msg}}))

	edges := findHandoffs([]Session{quill, stitch}, []string{"quill", "stitch"})
	if len(edges) != 3 {
		t.Fatalf("edges = %d, want one per message", len(edges))
	}
}
