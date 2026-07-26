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

// Pure functions over openclaw-trajectory v1 event streams. No I/O here.
// Ported from the home-server agent-monitor Node implementation.

package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// staleAfter marks an unfinished run with no event for this long as stale.
const staleAfter = 10 * time.Minute

// Event is one parsed trajectory JSONL line. Data holds the free-form
// provider payload untouched so replay can show it verbatim.
type Event struct {
	Type     string         `json:"type"`
	TS       string         `json:"ts"`
	Seq      float64        `json:"seq"`
	RunID    string         `json:"runId"`
	ModelID  string         `json:"modelId"`
	Provider string         `json:"provider"`
	Data     map[string]any `json:"data"`
}

// Tokens aggregates model.completed usage for a run.
type Tokens struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Total  int64 `json:"total"`
}

// Run is the per-runId summary derived from a session's events.
type Run struct {
	Agent       string `json:"agent"`
	SessionID   string `json:"sessionId"`
	RunID       string `json:"runId"`
	StartedAt   string `json:"startedAt"`
	LastEventAt string `json:"lastEventAt"`
	Outcome     string `json:"outcome"`
	Steps       int    `json:"steps"`
	Tokens      Tokens `json:"tokens"`
	Prompt      string `json:"prompt"`
	Model       string `json:"model"`
	Provider    string `json:"provider"`
	Source      string `json:"source,omitempty"`
}

// parseTrajectory tolerantly parses JSONL. Audit principle: never silently
// drop a record — unparseable lines are counted and surfaced to the UI.
func parseTrajectory(text string) (events []Event, badLines int) {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil || obj == nil {
			badLines++
			continue
		}
		typ, _ := obj["type"].(string)
		if typ == "" {
			badLines++
			continue
		}
		e := Event{Type: typ}
		e.TS, _ = obj["ts"].(string)
		e.Seq, _ = obj["seq"].(float64)
		e.RunID, _ = obj["runId"].(string)
		e.ModelID, _ = obj["modelId"].(string)
		e.Provider, _ = obj["provider"].(string)
		if d, ok := obj["data"].(map[string]any); ok {
			e.Data = d
		}
		events = append(events, e)
	}
	return events, badLines
}

// tsMillis parses a timestamp string to Unix milliseconds. Returns 0 for
// missing/unparseable values so sort order stays deterministic.
func tsMillis(ts string) int64 {
	if ts == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

func sortEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		ti, tj := tsMillis(events[i].TS), tsMillis(events[j].TS)
		if ti != tj {
			return ti < tj
		}
		return events[i].Seq < events[j].Seq
	})
}

// isTruncated reports whether the runtime replaced this event's payload with a
// size-limit marker. OpenClaw drops oversized fields rather than writing the
// whole event, so the data is missing by design, not by corruption.
func isTruncated(e Event) bool {
	t, _ := e.Data["truncated"].(bool)
	return t
}

// droppedFields lists the field names the runtime removed when truncating.
func droppedFields(e Event) []string {
	raw, _ := e.Data["droppedFields"].([]any)
	out := make([]string, 0, len(raw))
	for _, f := range raw {
		if s, ok := f.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// promptText extracts the prompt from a prompt.submitted event. Field names
// vary by provider; try the plausible ones, never fail. A truncated event says
// so explicitly — reporting it as merely "unavailable" would hide the reason.
func promptText(e Event) string {
	for _, k := range []string{"prompt", "text", "message", "input"} {
		if s, ok := e.Data[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if isTruncated(e) {
		if n := jsonInt(e.Data["originalBytes"]); n > 0 {
			return fmt.Sprintf("(prompt dropped by the trajectory size limit — original event was %d bytes)", n)
		}
		return "(prompt dropped by the trajectory size limit)"
	}
	return "(prompt unavailable)"
}

func isErrorEvent(e Event) bool {
	switch e.Type {
	case "model.completed":
		return truthy(e.Data["promptError"]) || truthy(e.Data["timedOut"]) || truthy(e.Data["aborted"])
	case "tool.result":
		return truthy(e.Data["error"]) || truthy(e.Data["isError"])
	}
	return false
}

// truthy mirrors JavaScript Boolean() for the JSON value types we see.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	default:
		return true
	}
}

// deriveRuns summarizes the parsed events of ONE session (any order
// tolerated) into one Run per runId, newest first.
func deriveRuns(agent, sessionID string, events []Event, now time.Time) []Run {
	sorted := make([]Event, len(events))
	copy(sorted, events)
	sortEvents(sorted)

	byRun := map[string][]Event{}
	var order []string
	for _, e := range sorted {
		id := e.RunID
		if id == "" {
			id = "unknown-run"
		}
		if _, ok := byRun[id]; !ok {
			order = append(order, id)
		}
		byRun[id] = append(byRun[id], e)
	}

	var runs []Run
	for _, runID := range order {
		evs := byRun[runID]
		first, last := evs[0], evs[len(evs)-1]
		finished, hasError := false, false
		for _, e := range evs {
			if e.Type == "model.completed" || e.Type == "session.ended" {
				finished = true
			}
			if isErrorEvent(e) {
				hasError = true
			}
		}
		var outcome string
		switch {
		case finished && hasError:
			outcome = "error"
		case finished:
			outcome = "ok"
		default:
			lastMs := tsMillis(last.TS)
			if lastMs != 0 && now.UnixMilli()-lastMs <= staleAfter.Milliseconds() {
				outcome = "running"
			} else {
				outcome = "stale"
			}
		}

		var tokens Tokens
		for _, e := range evs {
			if e.Type != "model.completed" {
				continue
			}
			u, ok := e.Data["usage"].(map[string]any)
			if !ok {
				continue
			}
			in, out := jsonInt(u["input"]), jsonInt(u["output"])
			tokens.Input += in
			tokens.Output += out
			// per-event total is advisory; fall back to input+output
			if t := jsonInt(u["total"]); t != 0 {
				tokens.Total += t
			} else {
				tokens.Total += in + out
			}
		}

		prompt := "(no prompt recorded)"
		for _, e := range evs {
			if e.Type == "prompt.submitted" {
				prompt = promptText(e)
				break
			}
		}
		// Prefer the first/last event with a valid ts so startedAt/lastEventAt
		// are never empty when a run mixes timestamped and un-timestamped events.
		firstTS, lastTS := first.TS, last.TS
		for _, e := range evs {
			if e.TS != "" {
				firstTS = e.TS
				break
			}
		}
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].TS != "" {
				lastTS = evs[i].TS
				break
			}
		}
		runs = append(runs, Run{
			Agent: agent, SessionID: sessionID, RunID: runID,
			StartedAt: firstTS, LastEventAt: lastTS, Outcome: outcome,
			Steps: len(evs), Tokens: tokens, Prompt: prompt,
			Model: first.ModelID, Provider: first.Provider,
		})
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return tsMillis(runs[i].StartedAt) > tsMillis(runs[j].StartedAt)
	})
	return runs
}

func jsonInt(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// ansiCSI matches real terminal color/cursor sequences. Agent tool output
// often carries them; they cannot render in HTML, so strip them for display.
// Text that merely contains the literal characters "" is left alone —
// that is genuine recorded content, not a control code.
var ansiCSI = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

func clip(s string, n int) string {
	s = ansiCSI.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		// Keep printable runes; whitespace is normalized just below.
		if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 {
			return r
		}
		return -1
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// eventSummary renders the one-line summary shown in agent cards and replay.
func eventSummary(e Event) string {
	d := e.Data
	switch e.Type {
	case "tool.call":
		arg := toolArgs(d)
		name, _ := d["name"].(string)
		if name == "" {
			name = "tool"
		}
		hint := ""
		for _, k := range []string{"command", "path", "file_path", "url"} {
			if s, ok := arg[k].(string); ok && s != "" {
				hint = s
				break
			}
		}
		if hint == "" {
			b, _ := json.Marshal(arg)
			hint = string(b)
		}
		return clip(name+": "+hint, 160)
	case "tool.result":
		if errv, ok := d["error"]; ok && truthy(errv) {
			return clip(fmt.Sprintf("result (error): %v", errv), 160)
		}
		return "result"
	case "model.completed":
		if isTruncated(e) {
			if f := droppedFields(e); len(f) > 0 {
				return clip("reply (event truncated by the size limit; dropped "+strings.Join(f, ", ")+")", 200)
			}
			return "reply (event truncated by the size limit)"
		}
		u, _ := d["usage"].(map[string]any)
		texts := ""
		if arr, ok := d["assistantTexts"].([]any); ok {
			var parts []string
			for _, t := range arr {
				if s, ok := t.(string); ok {
					parts = append(parts, s)
				}
			}
			texts = strings.Join(parts, " ")
		}
		return clip(fmt.Sprintf("reply (%d in / %d out tok) %s", jsonInt(u["input"]), jsonInt(u["output"]), texts), 200)
	case "prompt.submitted":
		return clip("prompt: "+promptText(e), 160)
	case "session.started":
		prov, model := e.Provider, e.ModelID
		if prov == "" {
			prov = "?"
		}
		if model == "" {
			model = "?"
		}
		return "session started (" + prov + "/" + model + ")"
	case "session.ended":
		return "session ended"
	}
	return clip(e.Type, 160)
}

// toolArgs returns the tool.call argument object under either key variant.
func toolArgs(d map[string]any) map[string]any {
	if a, ok := d["arguments"].(map[string]any); ok {
		return a
	}
	if a, ok := d["args"].(map[string]any); ok {
		return a
	}
	return map[string]any{}
}
