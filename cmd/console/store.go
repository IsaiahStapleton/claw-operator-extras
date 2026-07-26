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

// Reads <dataDir>/<agent>/sessions/*.trajectory.jsonl and plain transcript
// .jsonl files with a short cache. Synchronous reads are fine: files are
// local and snapshots are cached.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Session keeps one session's parsed trajectory events for cross-session
// analysis (handoffs, memory writes).
type Session struct {
	Agent     string
	SessionID string
	Events    []Event
}

// Snapshot is one cached scan of the data directory.
type Snapshot struct {
	OK              bool
	Error           string
	Agents          []string
	Runs            []Run
	Sessions        []Session
	Activity        map[string]int64 // agent -> last transcript mtime (ms), 0 if none
	BadLines        int
	ScannedFiles    int
	UnreadableFiles int
	// TruncatedEvents counts events whose payload the runtime dropped for
	// exceeding the trajectory size limit. Surfaced, never silently absorbed.
	TruncatedEvents int
}

// Store scans the agent data directory and caches the result briefly.
type Store struct {
	dataDir  string
	cacheTTL time.Duration
	excluded map[string]bool
	now      func() time.Time

	mu      sync.Mutex
	cached  *Snapshot
	cachedT time.Time
}

func newStore(dataDir string, cacheTTL time.Duration, excludeAgents []string) *Store {
	ex := map[string]bool{}
	for _, a := range excludeAgents {
		if a = strings.TrimSpace(a); a != "" {
			ex[a] = true
		}
	}
	return &Store{dataDir: dataDir, cacheTTL: cacheTTL, excluded: ex, now: time.Now}
}

func (s *Store) snapshot() *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && s.now().Sub(s.cachedT) <= s.cacheTTL {
		return s.cached
	}
	snap := s.scan()
	s.cached, s.cachedT = snap, s.now()
	return snap
}

func (s *Store) scan() *Snapshot {
	snap := &Snapshot{OK: true, Activity: map[string]int64{}}
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		snap.OK = false
		snap.Error = "data dir unreadable: " + errCode(err)
		return snap
	}
	now := s.now()
	for _, dirent := range entries {
		if !dirent.IsDir() {
			continue
		}
		agent := dirent.Name()
		if s.excluded[agent] {
			continue
		}
		sessDir := filepath.Join(s.dataDir, agent, "sessions")
		sessEntries, err := os.ReadDir(sessDir)
		if err != nil {
			continue // an agent dir without sessions/ is not an error
		}
		snap.Agents = append(snap.Agents, agent)

		// Transcript mtimes are the backend-agnostic activity signal:
		// CLI-harness backends don't emit trajectory sidecars, so
		// trajectories alone undercount agents that are alive.
		// sessions.json is deliberately excluded — the gateway sweeps every
		// registered agent's store at once, which is not agent activity.
		var lastActivity int64
		trajectoryIDs := map[string]bool{}
		for _, f := range sessEntries {
			name := f.Name()
			if !strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".trajectory.jsonl") {
				continue
			}
			if info, err := f.Info(); err == nil {
				if m := info.ModTime().UnixMilli(); m > lastActivity {
					lastActivity = m
				}
			}
		}
		snap.Activity[agent] = lastActivity

		for _, f := range sessEntries {
			name := f.Name()
			if !strings.HasSuffix(name, ".trajectory.jsonl") {
				continue
			}
			sessionID := strings.TrimSuffix(name, ".trajectory.jsonl")
			trajectoryIDs[sessionID] = true
			text, err := os.ReadFile(filepath.Join(sessDir, name))
			if err != nil {
				snap.UnreadableFiles++
				continue
			}
			snap.ScannedFiles++
			events, bad := parseTrajectory(string(text))
			snap.BadLines += bad
			for _, e := range events {
				if isTruncated(e) {
					snap.TruncatedEvents++
				}
			}
			snap.Sessions = append(snap.Sessions, Session{Agent: agent, SessionID: sessionID, Events: events})
			runs := deriveRuns(agent, sessionID, events, now)
			// A prompt the runtime dropped for size often survives in the
			// plain transcript, which is written separately and is not
			// subject to the trajectory event limit.
			recoverTruncatedPrompts(filepath.Join(sessDir, sessionID+".jsonl"), runs)
			snap.Runs = append(snap.Runs, runs...)
		}

		// Plain transcripts without a trajectory sidecar are sessions from
		// CLI-harness backends; derive a lightweight run for each.
		for _, f := range sessEntries {
			name := f.Name()
			if !strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".trajectory.jsonl") || name == "sessions.json" {
				continue
			}
			sessionID := strings.TrimSuffix(name, ".jsonl")
			if trajectoryIDs[sessionID] {
				continue
			}
			text, err := os.ReadFile(filepath.Join(sessDir, name))
			if err != nil {
				continue
			}
			snap.ScannedFiles++
			if run, ok := deriveTranscriptRun(agent, sessionID, string(text)); ok {
				snap.Runs = append(snap.Runs, run)
			}
		}
	}
	sort.SliceStable(snap.Runs, func(i, j int) bool {
		return tsMillis(snap.Runs[i].StartedAt) > tsMillis(snap.Runs[j].StartedAt)
	})
	return snap
}

// promptMatchWindow bounds how far a transcript message may sit from a run's
// start and still be considered that run's prompt. The two are written within
// moments of each other, so a wide window would risk attaching the wrong
// prompt in a session that contains several runs.
const promptMatchWindow = 2 * time.Minute

// userMessage is one timestamped user turn recovered from a transcript.
type userMessage struct {
	ms   int64
	text string
}

// recoverTruncatedPrompts fills in prompts the trajectory lost to its size
// limit, reading them from the session's plain transcript and matching by
// timestamp. Runs keep the truncation marker when nothing matches, so a miss
// degrades to the honest message rather than to a wrong prompt.
func recoverTruncatedPrompts(transcriptPath string, runs []Run) {
	need := false
	for i := range runs {
		if runs[i].PromptTruncated {
			need = true
			break
		}
	}
	if !need {
		return // do not touch the filesystem for sessions that are intact
	}
	msgs := readUserMessages(transcriptPath)
	if len(msgs) == 0 {
		return
	}
	for i := range runs {
		if !runs[i].PromptTruncated {
			continue
		}
		// Anchor on the prompt event, not the run's first event: context
		// compilation can put minutes between the two.
		anchor := tsMillis(runs[i].promptAt)
		if anchor == 0 {
			anchor = tsMillis(runs[i].StartedAt)
		}
		if anchor == 0 {
			continue
		}
		best, bestDelta := "", int64(-1)
		for _, m := range msgs {
			if m.ms == 0 {
				continue
			}
			delta := m.ms - anchor
			if delta < 0 {
				delta = -delta
			}
			if delta <= promptMatchWindow.Milliseconds() && (bestDelta < 0 || delta < bestDelta) {
				best, bestDelta = m.text, delta
			}
		}
		if best != "" {
			runs[i].Prompt = clip(best, 160)
			runs[i].PromptSource = "transcript"
		}
	}
}

// readUserMessages pulls timestamped user turns out of a transcript. Content
// is either a plain string or a block list, depending on the backend.
func readUserMessages(path string) []userMessage {
	text, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []userMessage
	for _, raw := range strings.Split(string(text), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if typ, _ := obj["type"].(string); typ != "message" {
			continue
		}
		msg, _ := obj["message"].(map[string]any)
		if msg == nil {
			continue
		}
		if role, _ := msg["role"].(string); role != "user" {
			continue
		}
		body := textOfContent(msg["content"])
		if body == "" {
			continue
		}
		ts := obj["timestamp"]
		if ts == nil {
			ts = msg["timestamp"]
		}
		iso, _ := anyToISO(ts)
		out = append(out, userMessage{ms: tsMillis(iso), text: body})
	}
	return out
}

// textOfContent flattens a message body to text, tolerating both the plain
// string form and the block-list form.
func textOfContent(v any) string {
	switch c := v.(type) {
	case string:
		return strings.TrimSpace(c)
	case []any:
		var parts []string
		for _, b := range c {
			blk, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := blk["type"].(string); t != "" && t != "text" {
				continue
			}
			if s, ok := blk["text"].(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, strings.TrimSpace(s))
			}
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}

// deriveTranscriptRun derives a lightweight run from a plain transcript
// (.jsonl) that has no trajectory sidecar.
func deriveTranscriptRun(agent, sessionID, text string) (Run, bool) {
	var startedAt, lastTS, prompt string
	messageCount := 0
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil || obj == nil {
			continue
		}
		typ, _ := obj["type"].(string)
		if typ == "session" {
			if ts, ok := obj["timestamp"].(string); ok && startedAt == "" {
				startedAt = ts
			}
		}
		if typ != "message" {
			continue
		}
		messageCount++
		msg, _ := obj["message"].(map[string]any)
		ts := obj["timestamp"]
		if ts == nil && msg != nil {
			ts = msg["timestamp"]
		}
		if iso, ok := anyToISO(ts); ok {
			if startedAt == "" {
				startedAt = iso
			}
			lastTS = iso
		}
		if prompt == "" && msg != nil {
			if role, _ := msg["role"].(string); role == "user" {
				if content, ok := msg["content"].(string); ok {
					prompt = clip(content, 120)
				}
			}
		}
	}
	if startedAt == "" || messageCount == 0 {
		return Run{}, false
	}
	if lastTS == "" {
		lastTS = startedAt
	}
	if prompt == "" {
		prompt = "(transcript)"
	}
	return Run{
		Agent: agent, SessionID: sessionID, RunID: "transcript-" + sessionID,
		StartedAt: startedAt, LastEventAt: lastTS, Outcome: "ok",
		Steps: messageCount, Prompt: prompt, Source: "transcript",
	}, true
}

// anyToISO normalizes a JSON timestamp (ISO string or epoch-millis number)
// to an ISO string.
func anyToISO(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		if ms := tsMillis(x); ms != 0 {
			return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano), true
		}
	case float64:
		return time.UnixMilli(int64(x)).UTC().Format(time.RFC3339Nano), true
	}
	return "", false
}

var safeNameRE = regexp.MustCompile(`^[\w.-]+$`)

// SessionDetail is the replay payload for one session file.
type SessionDetail struct {
	Total    int
	BadLines int
	Events   []Event
}

// sessionEvents reads the raw events of one session (uncached; replay is an
// explicit click). Returns nil if the session file doesn't exist.
func (s *Store) sessionEvents(agent, sessionID string, offset, limit int) *SessionDetail {
	text, ok := s.readSessionFile(agent, sessionID, ".trajectory.jsonl")
	if !ok {
		return nil
	}
	events, badLines := parseTrajectory(text)
	sortEvents(events)
	return &SessionDetail{Total: len(events), BadLines: badLines, Events: slicePage(events, offset, limit)}
}

// TranscriptMessage is one replay row for plain-transcript sessions.
type TranscriptMessage struct {
	TS      string `json:"ts"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Summary string `json:"summary"`
	Content string `json:"content"`
}

// sessionTranscript reads a plain transcript (.jsonl) for replay.
func (s *Store) sessionTranscript(agent, sessionID string, offset, limit int) ([]TranscriptMessage, int, bool) {
	text, ok := s.readSessionFile(agent, sessionID, ".jsonl")
	if !ok {
		return nil, 0, false
	}
	var messages []TranscriptMessage
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil || obj == nil {
			continue
		}
		typ, _ := obj["type"].(string)
		msg, _ := obj["message"].(map[string]any)
		if typ != "message" || msg == nil {
			continue
		}
		ts := obj["timestamp"]
		if ts == nil {
			ts = msg["timestamp"]
		}
		iso, _ := anyToISO(ts)
		role, _ := msg["role"].(string)
		if role == "" {
			role = "unknown"
		}
		content, ok := msg["content"].(string)
		if !ok {
			b, _ := json.Marshal(msg["content"])
			content = string(b)
		}
		messages = append(messages, TranscriptMessage{
			TS: iso, Type: "message", Role: role,
			Summary: clip(content, 200), Content: content,
		})
	}
	return slicePage(messages, offset, limit), len(messages), true
}

// readSessionFile validates names against path traversal and reads the file.
func (s *Store) readSessionFile(agent, sessionID, suffix string) (string, bool) {
	if !safeNameRE.MatchString(agent) || !safeNameRE.MatchString(sessionID) {
		return "", false
	}
	file := filepath.Join(s.dataDir, agent, "sessions", sessionID+suffix)
	root, err := filepath.Abs(s.dataDir)
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(file)
	if err != nil || !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", false
	}
	text, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	return string(text), true
}

func slicePage[T any](items []T, offset, limit int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

func errCode(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return fmt.Sprintf("%v", pe.Err)
	}
	return err.Error()
}
