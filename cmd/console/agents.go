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
// from the home-server agent-monitor server. Display names come from the
// Claw's own openclaw.json, so the console reports what the Claw actually
// calls its agents rather than what a deployment-time map claims.

package main

import (
	"encoding/json"
	"time"
)

// activityActiveWindow: transcript activity within this window marks an agent
// 'active' even without trajectory runs (CLI-harness backends emit no
// trajectory sidecars).
const activityActiveWindow = 5 * time.Minute

// AgentMeta is one agent's display identity as its own Claw configures it.
type AgentMeta struct {
	Emoji string `json:"emoji"`
	Title string `json:"title"`
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
	Model       string  `json:"model"`
	Provider    string  `json:"provider"`
}

// resolveMeta looks an agent up in its Claw's configured identities. An agent
// the config does not name is shown by its raw ID: an honest label, where an
// invented prettier one would misidentify it.
func resolveMeta(name string, meta map[string]AgentMeta) AgentMeta {
	m := meta[name]
	if m.Title == "" {
		m.Title = name
	}
	if m.Emoji == "" {
		m.Emoji = "🤖"
	}
	return m
}

// parseAgentIdentities extracts each agent's display identity from a Claw's
// own openclaw.json. Unreadable or unparseable config degrades to raw agent
// IDs rather than failing the scan.
func parseAgentIdentities(raw []byte) map[string]AgentMeta {
	out := map[string]AgentMeta{}
	if len(raw) == 0 {
		return out
	}
	var cfg struct {
		Agents struct {
			List []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Identity struct {
					Name  string `json:"name"`
					Emoji string `json:"emoji"`
				} `json:"identity"`
			} `json:"list"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return out
	}
	for _, a := range cfg.Agents.List {
		if a.ID == "" {
			continue
		}
		title := a.Identity.Name
		if title == "" {
			title = a.Name
		}
		out[a.ID] = AgentMeta{Title: title, Emoji: a.Identity.Emoji}
	}
	return out
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
				if step := lastEventOfRun(snap, name, latest); step != "" {
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

		meta := resolveMeta(name, snap.AgentIdentities)
		views = append(views, AgentView{
			Name: name, Status: status, CurrentStep: currentStep,
			LastRunAt: lastRunAt, RunCount: len(runs),
			Emoji: meta.Emoji, Title: meta.Title,
			Model: model, Provider: provider,
		})
	}
	return views
}

// lastEventOfRun reports what a running run is doing. The summary is captured
// when the run is derived, so neither the full event list nor another read of
// the session is needed here.
func lastEventOfRun(_ *Snapshot, _ string, latest *Run) string {
	return latest.CurrentStep
}
