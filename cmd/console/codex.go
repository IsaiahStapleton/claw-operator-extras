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
	"path"
	"sort"
	"strings"
	"time"
)

// codexSessionID extracts the session UUID from a codex session file path.
// Codex filenames follow the pattern rollout-<datetime>-<uuid>.jsonl; the
// UUID is the last 36 characters of the base name before the extension.
func codexSessionID(name string) string {
	base := path.Base(name)
	base = strings.TrimSuffix(base, ".jsonl")
	if len(base) >= 36 {
		return base[len(base)-36:]
	}
	return base
}

// safeCodexName validates that a codex session file path (relative to the
// agent directory) stays within the expected codex-home tree. Each segment
// is checked against safeNameRE individually so slashes are allowed between
// segments but shell metacharacters are not.
func safeCodexName(name string) bool {
	if !strings.HasPrefix(name, "agent/codex-home/sessions/") {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." || seg == "" {
			return false
		}
		if !safeNameRE.MatchString(seg) {
			return false
		}
	}
	return true
}

type codexTurn struct {
	runID    string
	start    string
	lastTS   string
	prompt   string
	model    string
	provider string
	messages int
	finished bool
	input    int64
	output   int64
	cache    int64
	lastStep string
}

// parseCodexSession derives runs and analysis events from a Codex CLI session
// file. Runs are built directly from the Codex event model rather than routed
// through deriveRuns, because the two formats have different granularity.
func parseCodexSession(agent, sessionID, text string, now time.Time) (string, []Run, []Event, int) {
	var model, provider string
	var currentTurn *codexTurn
	var turns []*codexTurn
	var events []Event
	var badLines int

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
		ts, _ := obj["timestamp"].(string)
		payload, _ := obj["payload"].(map[string]any)
		if typ == "" || payload == nil {
			if typ == "" {
				badLines++
			}
			continue
		}

		switch typ {
		case "session_meta":
			if id, ok := payload["session_id"].(string); ok && id != "" {
				sessionID = id
			}
			provider, _ = payload["model_provider"].(string)

		case "turn_context":
			model, _ = payload["model"].(string)
			if currentTurn != nil && currentTurn.model == "" {
				currentTurn.model = model
			}

		case "event_msg":
			subtype, _ := payload["type"].(string)
			switch subtype {
			case "task_started":
				turnID, _ := payload["turn_id"].(string)
				t := &codexTurn{
					runID: turnID, start: ts, lastTS: ts,
					model: model, provider: provider,
				}
				currentTurn = t
				turns = append(turns, t)

			case "user_message":
				if currentTurn == nil {
					continue
				}
				msg, _ := payload["message"].(string)
				if currentTurn.prompt == "" {
					currentTurn.prompt = msg
				}
				currentTurn.messages++
				currentTurn.lastTS = ts
				events = append(events, Event{
					Type: "prompt.submitted", TS: ts,
					RunID: currentTurn.runID,
					Data:  map[string]any{"prompt": msg},
				})

			case "agent_message":
				if currentTurn == nil {
					continue
				}
				msg, _ := payload["message"].(string)
				currentTurn.messages++
				currentTurn.lastTS = ts
				currentTurn.lastStep = clip(msg, 160)

			case "token_count":
				if currentTurn == nil {
					continue
				}
				info, _ := payload["info"].(map[string]any)
				if info == nil {
					continue
				}
				last, _ := info["last_token_usage"].(map[string]any)
				if last == nil {
					continue
				}
				currentTurn.input += jsonInt(last["input_tokens"])
				currentTurn.output += jsonInt(last["output_tokens"])
				currentTurn.cache += jsonInt(last["cached_input_tokens"])
				currentTurn.lastTS = ts

			case "task_complete":
				if currentTurn != nil {
					currentTurn.finished = true
					currentTurn.lastTS = ts
				}
			}

		case "response_item":
			if currentTurn == nil {
				continue
			}
			subtype, _ := payload["type"].(string)
			switch subtype {
			case "function_call":
				name, _ := payload["name"].(string)
				currentTurn.messages++
				currentTurn.lastTS = ts
				if name != "" {
					currentTurn.lastStep = clip(name, 160)
				}
			case "message":
				role, _ := payload["role"].(string)
				if role == "user" {
					var text string
					if content, ok := payload["content"].([]any); ok {
						for _, c := range content {
							if item, ok := c.(map[string]any); ok {
								if t, ok := item["text"].(string); ok {
									if text != "" {
										text += "\n"
									}
									text += t
								}
							}
						}
					}
					if currentTurn.prompt == "" && text != "" {
						currentTurn.prompt = text
					}
					currentTurn.messages++
					currentTurn.lastTS = ts
					events = append(events, Event{
						Type: "prompt.submitted", TS: ts,
						RunID: currentTurn.runID,
						Data:  map[string]any{"prompt": text},
					})
				}
			}
		}
	}

	var runs []Run
	for _, t := range turns {
		prompt := t.prompt
		if prompt == "" {
			prompt = "(no prompt recorded)"
		} else {
			prompt = clip(unwrapPromptEnvelope(prompt), 160)
		}
		outcome := "ok"
		if !t.finished {
			lastMs := tsMillis(t.lastTS)
			if lastMs != 0 && now.UnixMilli()-lastMs <= staleAfter.Milliseconds() {
				outcome = "running"
			} else {
				outcome = "stale"
			}
		}
		total := t.input + t.output
		step := t.lastStep
		if step == "" && t.finished {
			step = "session ended"
		}
		runs = append(runs, Run{
			Agent: agent, SessionID: sessionID, RunID: t.runID,
			StartedAt: t.start, LastEventAt: t.lastTS, Outcome: outcome,
			Steps: t.messages, Prompt: prompt,
			Model: t.model, Provider: t.provider,
			Source:      "codex",
			CurrentStep: step,
			Tokens: Tokens{
				Input: t.input, Output: t.output,
				Cache: t.cache, Total: total,
			},
		})
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return tsMillis(runs[i].StartedAt) > tsMillis(runs[j].StartedAt)
	})
	return sessionID, runs, events, badLines
}

// parseCodexEvents maps Codex CLI JSONL into Events for the replay viewer.
// Each meaningful event becomes an Event with a type that eventSummary knows
// how to render, so the replay UI works without codex-specific templates.
func parseCodexEvents(text string) ([]Event, string, int) {
	var events []Event
	var sessionID, currentRunID, model, provider string
	var badLines int
	var seq float64

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
		ts, _ := obj["timestamp"].(string)
		payload, _ := obj["payload"].(map[string]any)
		if typ == "" || payload == nil {
			if typ == "" {
				badLines++
			}
			continue
		}

		switch typ {
		case "session_meta":
			if id, ok := payload["session_id"].(string); ok {
				sessionID = id
			}
			provider, _ = payload["model_provider"].(string)
			seq++
			events = append(events, Event{
				Type: "session.started", TS: ts, Seq: seq,
				Provider: provider,
			})

		case "turn_context":
			model, _ = payload["model"].(string)

		case "event_msg":
			subtype, _ := payload["type"].(string)
			switch subtype {
			case "task_started":
				turnID, _ := payload["turn_id"].(string)
				if turnID != "" {
					currentRunID = turnID
				}

			case "user_message":
				msg, _ := payload["message"].(string)
				seq++
				events = append(events, Event{
					Type: "prompt.submitted", TS: ts, Seq: seq,
					RunID: currentRunID,
					Data:  map[string]any{"prompt": msg},
				})

			case "agent_message":
				msg, _ := payload["message"].(string)
				seq++
				events = append(events, Event{
					Type: "model.completed", TS: ts, Seq: seq,
					RunID: currentRunID, ModelID: model, Provider: provider,
					Data: map[string]any{
						"assistantTexts": []any{msg},
					},
				})

			case "task_complete":
				seq++
				events = append(events, Event{
					Type: "session.ended", TS: ts, Seq: seq,
					RunID: currentRunID,
				})
			}

		case "response_item":
			subtype, _ := payload["type"].(string)
			switch subtype {
			case "function_call":
				name, _ := payload["name"].(string)
				argsRaw, _ := payload["arguments"].(string)
				var args map[string]any
				if argsRaw != "" {
					_ = json.Unmarshal([]byte(argsRaw), &args)
				}
				if args == nil {
					args = map[string]any{}
				}
				seq++
				events = append(events, Event{
					Type: "tool.call", TS: ts, Seq: seq,
					RunID: currentRunID,
					Data:  map[string]any{"name": name, "arguments": args},
				})

			case "function_call_output":
				output, _ := payload["output"].(string)
				seq++
				events = append(events, Event{
					Type: "tool.result", TS: ts, Seq: seq,
					RunID: currentRunID,
					Data: map[string]any{"result": output},
				})

			case "message":
				role, _ := payload["role"].(string)
				var text string
				if content, ok := payload["content"].([]any); ok {
					for _, c := range content {
						if item, ok := c.(map[string]any); ok {
							if t, ok := item["text"].(string); ok {
								if text != "" {
									text += "\n"
								}
								text += t
							}
						}
					}
				}
				switch role {
				case "user":
					seq++
					events = append(events, Event{
						Type: "prompt.submitted", TS: ts, Seq: seq,
						RunID: currentRunID,
						Data:  map[string]any{"prompt": text},
					})
				case "assistant":
					seq++
					events = append(events, Event{
						Type: "model.completed", TS: ts, Seq: seq,
						RunID: currentRunID, ModelID: model, Provider: provider,
						Data: map[string]any{"assistantTexts": []any{text}},
					})
				}
			}
		}
	}
	return events, sessionID, badLines
}
