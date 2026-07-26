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

func TestParseTrajectoryCountsBadLinesInsteadOfDropping(t *testing.T) {
	text := strings.Join([]string{
		ev("session.started", evOpts{}),
		"{not json",
		ev("prompt.submitted", evOpts{Data: map[string]any{"prompt": "hi"}}),
		`{"noType":true}`,
		"",
	}, "\n")

	events, bad := parseTrajectory(text)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if bad != 2 {
		t.Fatalf("badLines = %d, want 2 (malformed JSON and a typeless object)", bad)
	}
}

func TestDeriveRunsOutcomes(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		events  []Event
		want    string
		wantErr bool
	}{
		{
			name: "finished cleanly is ok",
			events: parse(t,
				ev("session.started", evOpts{TS: iso(now.Add(-5 * time.Minute))}),
				ev("model.completed", evOpts{TS: iso(now.Add(-4 * time.Minute))}),
			),
			want: "ok",
		},
		{
			name: "tool.result error makes the finished run an error",
			events: parse(t,
				ev("session.started", evOpts{TS: iso(now.Add(-5 * time.Minute))}),
				ev("tool.result", evOpts{TS: iso(now.Add(-4 * time.Minute)), Data: map[string]any{"error": "boom"}}),
				ev("session.ended", evOpts{TS: iso(now.Add(-3 * time.Minute))}),
			),
			want: "error",
		},
		{
			name: "unfinished but recent is running",
			events: parse(t,
				ev("session.started", evOpts{TS: iso(now.Add(-1 * time.Minute))}),
				ev("tool.call", evOpts{TS: iso(now.Add(-30 * time.Second))}),
			),
			want: "running",
		},
		{
			name: "unfinished and older than the stale window is stale",
			events: parse(t,
				ev("session.started", evOpts{TS: iso(now.Add(-60 * time.Minute))}),
				ev("tool.call", evOpts{TS: iso(now.Add(-45 * time.Minute))}),
			),
			want: "stale",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runs := deriveRuns("main", "sess-1", tc.events, now)
			if len(runs) != 1 {
				t.Fatalf("runs = %d, want 1", len(runs))
			}
			if runs[0].Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", runs[0].Outcome, tc.want)
			}
		})
	}
}

func TestDeriveRunsSplitsMultiRunSessionsAndSumsTokens(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	events := parse(t,
		ev("session.started", evOpts{RunID: "run-a", TS: iso(now.Add(-30 * time.Minute))}),
		ev("prompt.submitted", evOpts{RunID: "run-a", TS: iso(now.Add(-29 * time.Minute)),
			Data: map[string]any{"prompt": "first task"}}),
		ev("model.completed", evOpts{RunID: "run-a", TS: iso(now.Add(-28 * time.Minute)),
			Data: map[string]any{"usage": map[string]any{"input": float64(100), "output": float64(20)}}}),
		ev("prompt.submitted", evOpts{RunID: "run-b", TS: iso(now.Add(-10 * time.Minute)),
			Data: map[string]any{"prompt": "second task"}}),
		ev("model.completed", evOpts{RunID: "run-b", TS: iso(now.Add(-9 * time.Minute)),
			Data: map[string]any{"usage": map[string]any{"input": float64(7), "output": float64(3), "total": float64(11)}}}),
	)

	runs := deriveRuns("main", "sess-1", events, now)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2 (one per runId)", len(runs))
	}
	// Newest first.
	if runs[0].RunID != "run-b" || runs[1].RunID != "run-a" {
		t.Fatalf("run order = %q,%q; want run-b,run-a (newest first)", runs[0].RunID, runs[1].RunID)
	}
	if runs[1].Tokens.Total != 120 {
		t.Fatalf("run-a total tokens = %d, want 120 (input+output when total is absent)", runs[1].Tokens.Total)
	}
	if runs[0].Tokens.Total != 11 {
		t.Fatalf("run-b total tokens = %d, want 11 (provider-supplied total wins)", runs[0].Tokens.Total)
	}
	if runs[1].Prompt != "first task" {
		t.Fatalf("run-a prompt = %q, want %q", runs[1].Prompt, "first task")
	}
}

func TestDeriveRunsToleratesMissingTimestamps(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	stamped := iso(now.Add(-2 * time.Minute))
	events := parse(t,
		ev("tool.call", evOpts{TS: "-"}), // unparseable ts sorts to the front
		ev("session.started", evOpts{TS: stamped}),
		ev("model.completed", evOpts{TS: stamped}),
	)

	runs := deriveRuns("main", "sess-1", events, now)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if runs[0].StartedAt == "" || runs[0].LastEventAt == "" {
		t.Fatalf("startedAt/lastEventAt must fall back to a real timestamp, got %q/%q",
			runs[0].StartedAt, runs[0].LastEventAt)
	}
}

func TestEventSummary(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		want  string
	}{
		{
			name: "tool.call prefers the command argument",
			event: Event{Type: "tool.call", Data: map[string]any{
				"name": "bash", "arguments": map[string]any{"command": "docker compose ps"}}},
			want: "bash: docker compose ps",
		},
		{
			name: "tool.call falls back to args key",
			event: Event{Type: "tool.call", Data: map[string]any{
				"name": "read", "args": map[string]any{"path": "/etc/hosts"}}},
			want: "read: /etc/hosts",
		},
		{
			name:  "session.started names provider and model",
			event: Event{Type: "session.started", Provider: "anthropic", ModelID: "claude-opus-4-8"},
			want:  "session started (anthropic/claude-opus-4-8)",
		},
		{
			name:  "unknown types fall back to the type name",
			event: Event{Type: "custom.thing"},
			want:  "custom.thing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventSummary(tc.event); got != tc.want {
				t.Fatalf("eventSummary = %q, want %q", got, tc.want)
			}
		})
	}
}

// OpenClaw rewrites oversized events, replacing the payload with a size-limit
// marker. The console must say the payload was dropped at the source rather
// than implying the data is simply missing. Shape observed in production.
func TestTruncatedEventsAreExplainedNotHidden(t *testing.T) {
	truncated := map[string]any{
		"truncated":     true,
		"originalBytes": float64(341660),
		"limitBytes":    float64(262144),
		"reason":        "trajectory-event-size-limit",
		"droppedFields": []any{"prompt", "systemPrompt", "messages", "imagesCount"},
	}

	prompt := promptText(Event{Type: "prompt.submitted", Data: truncated})
	if !strings.Contains(prompt, "dropped by the trajectory size limit") {
		t.Fatalf("prompt = %q, want an explanation of why it is missing", prompt)
	}
	if !strings.Contains(prompt, "341660") {
		t.Fatalf("prompt = %q, should report the original event size", prompt)
	}

	summary := eventSummary(Event{Type: "model.completed", Data: truncated})
	if !strings.Contains(summary, "truncated") {
		t.Fatalf("summary = %q, want the truncation surfaced", summary)
	}

	if !isTruncated(Event{Data: truncated}) {
		t.Fatal("isTruncated should detect the marker")
	}
	if isTruncated(Event{Data: map[string]any{"prompt": "hi"}}) {
		t.Fatal("a normal event must not be reported as truncated")
	}
}

func TestSnapshotCountsTruncatedEvents(t *testing.T) {
	now := time.Now().UTC()
	root := makeDataDir(t, map[string]map[string][]string{
		"main": {"sess-1": {
			ev("session.started", evOpts{TS: iso(now.Add(-2 * time.Minute))}),
			ev("prompt.submitted", evOpts{TS: iso(now.Add(-2 * time.Minute)), Data: map[string]any{
				"truncated": true, "reason": "trajectory-event-size-limit",
				"droppedFields": []any{"prompt"}}}),
			ev("model.completed", evOpts{TS: iso(now.Add(-1 * time.Minute))}),
		}},
	})

	snap := newStore(root, 0, nil).snapshot()
	if snap.TruncatedEvents != 1 {
		t.Fatalf("truncatedEvents = %d, want 1 so the UI can surface it", snap.TruncatedEvents)
	}
	if snap.BadLines != 0 {
		t.Fatal("a truncated event is well-formed JSON — it must not count as a bad line")
	}
}

func TestPromptTextFallsBackWhenNoFieldMatches(t *testing.T) {
	if got := promptText(Event{Data: map[string]any{"unrelated": "x"}}); got != "(prompt unavailable)" {
		t.Fatalf("promptText = %q, want the explicit unavailable marker", got)
	}
}

// parse turns fixture lines into events, failing the test on any bad line.
func parse(t *testing.T, lines ...string) []Event {
	t.Helper()
	events, bad := parseTrajectory(strings.Join(lines, "\n"))
	if bad != 0 {
		t.Fatalf("fixture produced %d bad lines", bad)
	}
	return events
}
