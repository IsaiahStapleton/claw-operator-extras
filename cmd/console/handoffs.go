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

// Handoffs are inferred, not declared: a spawn-ish tool.call in agent X,
// then a session.started in agent Y shortly after => edge X->Y.

package main

import (
	"encoding/json"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
)

const handoffWindowMillis = 120_000

var (
	spawnNameRE = regexp.MustCompile(`(?i)spawn|handoff`)
	askAgentRE  = regexp.MustCompile(`\bask-agent\s+([\w-]+)`)
	// OpenClaw stamps delegated prompts with the originating session, e.g.
	// "sourceSession=agent:default:dashboard:c2a2bab7-…". That is a declared
	// parent, so it outranks the timing heuristic below.
	sourceSessionRE = regexp.MustCompile(`sourceSession=agent:([^\s:]+):(?:[^\s:]+:)*([0-9a-fA-F-]{36})`)
)

// declaredHandoffs reads parent links that the runtime states outright, rather
// than inferring them from timing. Cross-agent links only: a session
// delegating to itself is not a fleet-level handoff.
func declaredHandoffs(sessions []Session, knownAgents []string) []Handoff {
	known := map[string]bool{}
	for _, a := range knownAgents {
		known[a] = true
	}
	edges := []Handoff{}
	seen := map[string]bool{}
	for _, s := range sessions {
		for _, e := range s.Events {
			if e.Type != "prompt.submitted" {
				continue
			}
			prompt, _ := e.Data["prompt"].(string)
			if prompt == "" {
				continue
			}
			m := sourceSessionRE.FindStringSubmatch(prompt)
			if m == nil {
				continue
			}
			fromAgent, fromSession := m[1], m[2]
			if fromAgent == s.Agent || !known[fromAgent] || fromSession == s.SessionID {
				continue
			}
			key := fromSession + "->" + s.SessionID
			if seen[key] {
				continue
			}
			seen[key] = true
			edges = append(edges, Handoff{
				FromAgent: fromAgent, FromSessionID: fromSession,
				ToAgent: s.Agent, ToSessionID: s.SessionID, TS: e.TS,
			})
		}
	}
	return edges
}

// Handoff is one inferred agent-to-agent spawn edge.
type Handoff struct {
	FromAgent     string `json:"fromAgent"`
	FromSessionID string `json:"fromSessionId"`
	FromRunID     string `json:"fromRunId"`
	ToAgent       string `json:"toAgent"`
	ToSessionID   string `json:"toSessionId"`
	TS            string `json:"ts"`
}

// spawnTarget resolves the target agent of a spawn-like tool.call, or "".
func spawnTarget(e Event, knownAgents []string) string {
	name, _ := e.Data["name"].(string)
	arg := toolArgs(e.Data)
	if spawnNameRE.MatchString(name) {
		// 1. Try structured fields first.
		for _, field := range []string{"agent", "target", "subagent"} {
			if s, ok := arg[field].(string); ok {
				return s
			}
		}
		// 2. Fallback: scan serialised text with word-boundary regexes;
		// pick the earliest match.
		b, _ := json.Marshal(arg)
		text := string(b)
		best, bestIdx := "", -1
		for _, a := range knownAgents {
			re, err := regexp.Compile(`\b` + regexp.QuoteMeta(a) + `\b`)
			if err != nil {
				continue
			}
			if loc := re.FindStringIndex(text); loc != nil && (bestIdx < 0 || loc[0] < bestIdx) {
				bestIdx, best = loc[0], a
			}
		}
		return best
	}
	if name == "bash" {
		if cmd, ok := arg["command"].(string); ok {
			if m := askAgentRE.FindStringSubmatch(cmd); m != nil {
				for _, a := range knownAgents {
					if a == m[1] {
						return m[1]
					}
				}
			}
		}
	}
	return ""
}

// findHandoffs returns every agent-to-agent edge: the ones the runtime
// declares outright, plus ones inferred from a spawn-like tool call followed
// by a session start. Declared edges win — a child session already claimed by
// a declared parent is not re-attributed by the heuristic.
func findHandoffs(sessions []Session, knownAgents []string) []Handoff {
	declared := declaredHandoffs(sessions, knownAgents)
	claimed := map[string]bool{}
	for _, e := range declared {
		claimed[e.ToSessionID] = true
	}
	return append(declared, inferHandoffs(sessions, knownAgents, claimed)...)
}

// inferHandoffs correlates spawn tool.calls with session.started events across
// ALL agents' sessions.
func inferHandoffs(sessions []Session, knownAgents []string, claimed map[string]bool) []Handoff {
	type start struct {
		agent, sessionID string
		ts               int64
	}
	var starts []start
	for _, s := range sessions {
		for _, e := range s.Events {
			if e.Type == "session.started" {
				starts = append(starts, start{s.Agent, s.SessionID, tsMillis(e.TS)})
			}
		}
	}
	// Collect all spawn tool.calls sorted by timestamp (earliest first) so
	// the first spawn always wins when two target the same child session.
	type spawnCall struct {
		session Session
		event   Event
		target  string
		ts      int64
	}
	var spawnCalls []spawnCall
	for _, s := range sessions {
		for _, e := range s.Events {
			if e.Type != "tool.call" {
				continue
			}
			target := spawnTarget(e, knownAgents)
			if target == "" || target == s.Agent {
				continue
			}
			spawnCalls = append(spawnCalls, spawnCall{s, e, target, tsMillis(e.TS)})
		}
	}
	sort.SliceStable(spawnCalls, func(i, j int) bool { return spawnCalls[i].ts < spawnCalls[j].ts })

	consumed := map[string]bool{}
	for id := range claimed {
		consumed[id] = true // a declared parent already owns this child
	}
	edges := []Handoff{}
	for _, sc := range spawnCalls {
		var child *start
		for i := range starts {
			c := &starts[i]
			if c.agent != sc.target || c.ts < sc.ts || c.ts-sc.ts > handoffWindowMillis || consumed[c.sessionID] {
				continue
			}
			if child == nil || c.ts < child.ts {
				child = c
			}
		}
		if child != nil {
			consumed[child.sessionID] = true
			edges = append(edges, Handoff{
				FromAgent: sc.session.Agent, FromSessionID: sc.session.SessionID, FromRunID: sc.event.RunID,
				ToAgent: child.agent, ToSessionID: child.sessionID, TS: sc.event.TS,
			})
		}
	}
	return edges
}

// memory.go equivalent: extraction of memory-vault writes from tool.calls.

// vaultPathRE matches where OpenClaw actually keeps durable notes: a memory/
// or wiki/ directory anywhere in the path, e.g. "memory/2026-07-23.md" or
// "/home/node/.openclaw/workspace/wiki/main/WIKI.md". Override with
// MEMORY_PATH_PATTERN for a deployment that keeps its vault elsewhere — the
// home-server convention, for instance, is "memory-map/".
var vaultPathRE = compileVaultPattern(os.Getenv("MEMORY_PATH_PATTERN"))

const defaultVaultPattern = `(?:^|/)(?:memory|wiki)/[\w\-./]*\.md`

func compileVaultPattern(pattern string) *regexp.Regexp {
	if pattern != "" {
		if re, err := regexp.Compile(pattern); err == nil {
			return re
		}
		log.Printf("MEMORY_PATH_PATTERN ignored (invalid regexp): %q", pattern)
	}
	return regexp.MustCompile(defaultVaultPattern)
}

// Native memory tools mutate the store directly rather than through a file, so
// they are writes even though no path appears in their arguments.
var nativeMemoryWriteTools = map[string]bool{
	"memory_store":  true,
	"memory_forget": true,
	"wiki_write":    true,
	"wiki_put":      true,
}

var (
	bashWriteRE    = regexp.MustCompile(`(>>?|\btee\b|\bsed\b.*-i|\bmv\b|\bcp\b)`)
	bashReadonlyRE = regexp.MustCompile(`^\s*(cat|less|head|tail|grep|ls|find|diff|rg)\b`)
	bashRedirectRE = regexp.MustCompile(`>>?|\btee\b`)
	writeTools     = map[string]bool{"write": true, "edit": true, "apply_patch": true, "create": true, "str_replace": true}
)

// MemoryWrite is one detected write to the shared memory vault.
type MemoryWrite struct {
	TS        string `json:"ts"`
	Agent     string `json:"agent"`
	SessionID string `json:"sessionId"`
	RunID     string `json:"runId"`
	Tool      string `json:"tool"`
	NotePath  string `json:"notePath"`
	Content   string `json:"content,omitempty"`
	// Size is the note's size on disk. The listing carries it instead of the
	// content so the memory page stays cheap to poll; content is fetched per
	// note when one is opened.
	Size int64 `json:"size,omitempty"`
}

// extractMemoryWrites answers "what are my agents committing to memory?" —
// derived from tool.call events that WRITE under memory-map/. Reads are
// excluded; human edits are invisible here by design.
func extractMemoryWrites(sessions []Session) []MemoryWrite {
	writes := []MemoryWrite{}
	for _, s := range sessions {
		for _, e := range s.Events {
			if e.Type != "tool.call" {
				continue
			}
			name, _ := e.Data["name"].(string)
			lname := strings.ToLower(name)
			arg := toolArgs(e.Data)
			argJSON, _ := json.Marshal(arg)
			// The pattern anchors on a path separator, so the match carries a
			// leading slash that is not part of the note's identity.
			pathMatch := strings.TrimPrefix(vaultPathRE.FindString(string(argJSON)), "/")

			// A native memory tool is a write even with no path in its
			// arguments: it mutates the store itself.
			if nativeMemoryWriteTools[lname] {
				label := firstString(arg, "path", "key", "title", "id", "name")
				if label == "" {
					label = lname
				}
				writes = append(writes, MemoryWrite{
					TS: e.TS, Agent: s.Agent, SessionID: s.SessionID, RunID: e.RunID,
					Tool: name, NotePath: label,
					Content: clipBytes(firstString(arg, "content", "text", "value", "memory"), 2000),
				})
				continue
			}
			if pathMatch == "" {
				continue
			}

			var content string
			switch {
			case writeTools[lname]:
				content = firstString(arg, "content", "new_string", "text")
				if content == "" {
					content = clipBytes(string(argJSON), 2000)
				}
			case lname == "bash":
				cmd, _ := arg["command"].(string)
				if cmd == "" || !bashWriteRE.MatchString(cmd) {
					continue // no write token at all
				}
				if bashReadonlyRE.MatchString(cmd) && !bashRedirectRE.MatchString(cmd) {
					continue // pure reader
				}
				content = cmd
			default:
				continue // read/list/other tools touching the vault are not writes
			}

			tool := name
			if tool == "" {
				tool = "tool"
			}
			writes = append(writes, MemoryWrite{
				TS: e.TS, Agent: s.Agent, SessionID: s.SessionID, RunID: e.RunID,
				Tool: tool, NotePath: pathMatch, Content: content,
			})
		}
	}
	sort.SliceStable(writes, func(i, j int) bool { return tsMillis(writes[i].TS) > tsMillis(writes[j].TS) })
	return writes
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func clipBytes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

/* ------------------------------------------------------ session provenance */

// SessionOrigin is how one group of sessions came to exist: the agent that ran
// them, the trigger the runtime recorded, and the kind of session key it used.
type SessionOrigin struct {
	Agent    string `json:"agent"`
	Trigger  string `json:"trigger"`
	Kind     string `json:"kind"`
	Sessions int    `json:"sessions"`
	LastAt   string `json:"lastAt"`
}

// sessionKeyKind names the shape of a session key. OpenClaw writes
// "agent:<agent>:<kind>[:<id>]", where kind is "main", "cron", "subagent",
// "dashboard", or a caller-chosen label such as "stitch-daily-2026-07-26".
// A caller-chosen label is reported as "named", because the distinction that
// matters is whether the runtime named it or something outside did.
func sessionKeyKind(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) < 3 || parts[0] != "agent" {
		return "unknown"
	}
	switch parts[2] {
	case "main", "cron", "subagent", "dashboard":
		return parts[2]
	}
	return "named"
}

// sessionOrigins summarizes where a fleet's work comes from.
//
// This exists because handoff edges alone understate the fleet badly. OpenClaw
// stamps a parent onto a delegated prompt only on one delivery path, so a Claw
// whose agents genuinely collaborate can show two edges and hundreds of
// sessions with no recorded parent at all. Reporting how sessions were actually
// triggered says something true about that, where an inferred edge would only
// look like an answer.
func sessionOrigins(sessions []Session) []SessionOrigin {
	type acc struct {
		n    int
		last string
	}
	groups := map[SessionOrigin]*acc{}
	for _, s := range sessions {
		var trigger, key, ts string
		for _, e := range s.Events {
			if e.Type != "session.started" {
				continue
			}
			trigger, _ = e.Data["trigger"].(string)
			key, ts = e.SessionKey, e.TS
			break
		}
		if trigger == "" {
			trigger = "unrecorded"
		}
		g := SessionOrigin{Agent: s.Agent, Trigger: trigger, Kind: sessionKeyKind(key)}
		if groups[g] == nil {
			groups[g] = &acc{}
		}
		groups[g].n++
		if tsMillis(ts) > tsMillis(groups[g].last) {
			groups[g].last = ts
		}
	}
	out := make([]SessionOrigin, 0, len(groups))
	for g, a := range groups {
		g.Sessions, g.LastAt = a.n, a.last
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Agent < out[j].Agent
	})
	return out
}
