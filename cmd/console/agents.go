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

// Agent-level derivation: status, current step, and display metadata. Ported
// from the home-server agent-monitor server, with optional presentation
// metadata (emoji/title/desc) resolved from the AGENT_META config.

package main

import (
	"strings"
	"time"
	"unicode"
)

// activityActiveWindow: transcript activity within this window marks an agent
// 'active' even without trajectory runs (CLI-harness backends emit no
// trajectory sidecars).
const activityActiveWindow = 5 * time.Minute

// AgentMeta is optional presentation metadata for one agent, supplied via the
// AGENT_META env var. All fields are cosmetic; the console works without them.
type AgentMeta struct {
	Emoji string `json:"emoji"`
	Title string `json:"title"`
	Desc  string `json:"desc"`
}

// AgentView is the /api/agents row: live status plus resolved display fields.
type AgentView struct {
	Name        string  `json:"name"`
	Status      string  `json:"status"` // active | idle | stale
	CurrentStep *string `json:"currentStep"`
	LastRunAt   *string `json:"lastRunAt"`
	RunCount    int     `json:"runCount"`
	Emoji       string  `json:"emoji"`
	Title       string  `json:"title"`
	Desc        string  `json:"desc"`
	Model       string  `json:"model"`
	Provider    string  `json:"provider"`
}

// resolveMeta fills in emoji/title/desc, falling back to sensible defaults so
// the UI never renders blanks when AGENT_META omits an agent.
func resolveMeta(name string, meta map[string]AgentMeta) AgentMeta {
	m := meta[name]
	if m.Title == "" {
		m.Title = humanizeName(name)
	}
	if m.Emoji == "" {
		m.Emoji = "🤖"
	}
	return m
}

func humanizeName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		r := []rune(p)
		r[0] = unicode.ToUpper(r[0])
		parts[i] = string(r)
	}
	if len(parts) == 0 {
		return name
	}
	return strings.Join(parts, " ")
}

// buildAgentViews computes per-agent status/currentStep exactly like the Node
// server: latest run drives active/stale, transcript activity backstops both
// "last seen" and idle→active promotion.
func (s *server) buildAgentViews(snap *Snapshot, now time.Time) []AgentView {
	views := make([]AgentView, 0, len(snap.Agents))
	for _, name := range snap.Agents {
		var runs []Run
		for _, r := range snap.Runs {
			if r.Agent == name {
				runs = append(runs, r)
			}
		}
		var latest *Run
		if len(runs) > 0 {
			latest = &runs[0]
		}
		status := "idle"
		var currentStep *string
		var model, provider string
		if latest != nil {
			model, provider = latest.Model, latest.Provider
			switch latest.Outcome {
			case "running":
				status = "active"
				if step := s.currentStep(name, latest); step != "" {
					currentStep = &step
				}
			case "stale":
				status = "stale"
			}
		}

		activityMs := snap.Activity[name]
		var trajMs int64
		if latest != nil {
			trajMs = tsMillis(latest.LastEventAt)
		}
		lastSeenMs := trajMs
		if activityMs > lastSeenMs {
			lastSeenMs = activityMs
		}
		if status == "idle" && activityMs != 0 && now.UnixMilli()-activityMs <= activityActiveWindow.Milliseconds() {
			status = "active"
		}

		var lastRunAt *string
		if lastSeenMs != 0 {
			iso := time.UnixMilli(lastSeenMs).UTC().Format(time.RFC3339Nano)
			lastRunAt = &iso
		}

		meta := resolveMeta(name, s.agentMeta)
		views = append(views, AgentView{
			Name: name, Status: status, CurrentStep: currentStep,
			LastRunAt: lastRunAt, RunCount: len(runs),
			Emoji: meta.Emoji, Title: meta.Title, Desc: meta.Desc,
			Model: model, Provider: provider,
		})
	}
	return views
}

// currentStep resolves the last event of the currently-running run, mirroring
// the Node server's offset-safe lookup for multi-run sessions.
func (s *server) currentStep(agent string, latest *Run) string {
	detail := s.store.sessionEvents(agent, latest.SessionID, 0, 1000)
	if detail == nil {
		return ""
	}
	for i := len(detail.Events) - 1; i >= 0; i-- {
		if detail.Events[i].RunID == latest.RunID {
			return eventSummary(detail.Events[i])
		}
	}
	return ""
}
