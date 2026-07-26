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
	"context"
	"encoding/json"
	"fmt"
	"os"

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
	// SkippedSessions counts sessions past the per-agent cap. Reading is a
	// round trip into a pod, so old sessions are bounded — but the UI says how
	// many were left out rather than presenting a partial view as complete.
	SkippedSessions int
}

const (
	// scanTimeout bounds one refresh. Exec round trips can hang on a pod that
	// is being rescheduled; the console reports staleness instead of wedging.
	scanTimeout = 60 * time.Second
	// maxSessionsPerAgent caps how many of an agent's newest sessions are
	// parsed per refresh.
	maxSessionsPerAgent = 200
)

// parsedSession is one session file already parsed, retained so an
// append-only file is not re-read on every refresh. Reads may be round trips
// into a pod, so the identity check (size + mtime) is what keeps the console
// cheap once it is warm.
type parsedSession struct {
	size      int64
	modTime   int64
	events    []Event // filtered: only what the cross-session analyses need
	runs      []Run   // derived while the full events were still in hand
	badLines  int
	truncated int
}

// Store turns a sessionSource into cached snapshots.
type Store struct {
	source   sessionSource
	cacheTTL time.Duration
	excluded map[string]bool
	now      func() time.Time

	mu      sync.Mutex
	cached  *Snapshot
	cachedT time.Time
	parsed  map[string]parsedSession // "<agent>/<file>" -> parsed content
}

func newStoreFromSource(src sessionSource, cacheTTL time.Duration, excludeAgents []string) *Store {
	ex := map[string]bool{}
	for _, a := range excludeAgents {
		if a = strings.TrimSpace(a); a != "" {
			ex[a] = true
		}
	}
	return &Store{source: src, cacheTTL: cacheTTL, excluded: ex, now: time.Now,
		parsed: map[string]parsedSession{}}
}

// newStore keeps the local-directory constructor the tests and
// `make console-run-local` use.
func newStore(dataDir string, cacheTTL time.Duration, excludeAgents []string) *Store {
	return newStoreFromSource(dirSource{root: dataDir}, cacheTTL, excludeAgents)
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
	ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
	defer cancel()

	snap := &Snapshot{OK: true, Activity: map[string]int64{}}
	agentDirs, files, err := s.source.index(ctx)
	if err != nil {
		snap.OK = false
		snap.Error = "agent data unreadable: " + errCode(err)
		return snap
	}
	now := s.now()

	// Group the flat index by agent so each agent is summarized independently.
	// Agents with no readable sessions still appear: a Claw may run a backend
	// whose on-disk layout this console does not parse, and reporting the
	// agent with zero runs is truthful where omitting it is not.
	byAgent := map[string][]sessionFile{}
	for _, name := range agentDirs {
		if s.excluded[name] {
			continue
		}
		byAgent[name] = nil
	}
	for _, f := range files {
		if s.excluded[f.Agent] {
			continue
		}
		byAgent[f.Agent] = append(byAgent[f.Agent], f)
	}

	// First pass: decide which files this refresh must actually read. Reads may
	// be round trips into a pod, so they are gathered and issued as one batch
	// rather than one at a time.
	fresh := map[string]bool{}
	var toFetch []sessionFile
	plan := map[string][]sessionFile{} // agent -> trajectories to parse
	transcriptsFor := map[string]map[string]sessionFile{}

	for agent, entries := range byAgent {
		snap.Agents = append(snap.Agents, agent)

		// Transcript mtimes are the backend-agnostic activity signal:
		// CLI-harness backends emit no trajectory sidecar, so trajectories
		// alone undercount agents that are alive. sessions.json is excluded —
		// the gateway sweeps every registered agent's store at once, which is
		// not activity by this agent.
		var lastActivity int64
		trajectoryIDs := map[string]bool{}
		transcripts := map[string]sessionFile{}
		var trajectories []sessionFile
		for _, f := range entries {
			switch {
			case strings.HasSuffix(f.Name, ".trajectory.jsonl"):
				trajectoryIDs[strings.TrimSuffix(f.Name, ".trajectory.jsonl")] = true
				trajectories = append(trajectories, f)
			case f.Name == "sessions.json" || !strings.HasSuffix(f.Name, ".jsonl"):
				// not a session record
			default:
				transcripts[strings.TrimSuffix(f.Name, ".jsonl")] = f
				if f.ModTime > lastActivity {
					lastActivity = f.ModTime
				}
			}
		}
		snap.Activity[agent] = lastActivity

		// Newest first, so a cap keeps the sessions that matter.
		sort.SliceStable(trajectories, func(i, j int) bool {
			return trajectories[i].ModTime > trajectories[j].ModTime
		})
		if len(trajectories) > maxSessionsPerAgent {
			snap.SkippedSessions += len(trajectories) - maxSessionsPerAgent
			trajectories = trajectories[:maxSessionsPerAgent]
		}

		plan[agent] = trajectories
		transcriptsFor[agent] = transcripts

		for _, f := range trajectories {
			key := batchKey(agent, f.Name)
			fresh[key] = true
			if cached, ok := s.parsed[key]; !ok || cached.size != f.Size || cached.modTime != f.ModTime {
				toFetch = append(toFetch, f)
			}
		}
		// Transcripts are needed for sessions with no trajectory at all, and
		// for recovering prompts the trajectory truncated.
		for sessionID, f := range transcripts {
			if !trajectoryIDs[sessionID] {
				toFetch = append(toFetch, f)
				continue
			}
			if cached, ok := s.parsed[batchKey(agent, sessionID+".trajectory.jsonl")]; ok {
				if hasTruncatedPrompt(cached.events) {
					toFetch = append(toFetch, f)
				}
			} else {
				toFetch = append(toFetch, f) // unparsed yet; may need recovery
			}
		}
	}

	bodies, err := s.source.readMany(ctx, toFetch)
	if err != nil && len(bodies) == 0 {
		snap.OK = false
		snap.Error = "agent data unreadable: " + errCode(err)
		return snap
	}

	for agent, trajectories := range plan {
		transcripts := transcriptsFor[agent]
		for _, f := range trajectories {
			sessionID := strings.TrimSuffix(f.Name, ".trajectory.jsonl")
			key := batchKey(agent, f.Name)

			cached, ok := s.parsed[key]
			if !ok || cached.size != f.Size || cached.modTime != f.ModTime {
				text, got := bodies[key]
				if !got {
					snap.UnreadableFiles++
					continue
				}
				events, bad := parseTrajectory(string(text))
				truncated := 0
				for _, e := range events {
					if isTruncated(e) {
						truncated++
					}
				}
				cached = parsedSession{size: f.Size, modTime: f.ModTime,
					events: retainForAnalysis(events), badLines: bad, truncated: truncated,
					runs: deriveRuns(agent, sessionID, events, now)}
				s.parsed[key] = cached
			}

			snap.ScannedFiles++
			snap.BadLines += cached.badLines
			snap.TruncatedEvents += cached.truncated
			snap.Sessions = append(snap.Sessions, Session{Agent: agent, SessionID: sessionID, Events: cached.events})

			runs := append([]Run(nil), cached.runs...)
			// A prompt the runtime dropped for size often survives in the
			// plain transcript, which is written separately and is not
			// subject to the trajectory event limit.
			if t, ok := transcripts[sessionID]; ok && needsPromptRecovery(runs) {
				if body, got := bodies[batchKey(agent, t.Name)]; got {
					applyTranscriptPrompts(readUserMessagesFrom(body), runs)
				}
			}
			snap.Runs = append(snap.Runs, runs...)
		}

		// Plain transcripts with no trajectory sidecar are sessions from
		// CLI-harness backends; derive a lightweight run for each.
		for sessionID, f := range transcripts {
			if _, hasTrajectory := s.parsed[batchKey(agent, sessionID+".trajectory.jsonl")]; hasTrajectory {
				continue
			}
			body, got := bodies[batchKey(agent, f.Name)]
			if !got {
				continue
			}
			snap.ScannedFiles++
			if run, ok := deriveTranscriptRun(agent, sessionID, string(body)); ok {
				snap.Runs = append(snap.Runs, run)
			}
		}
	}

	// Drop cache entries for files that no longer exist so a long-lived
	// console does not grow without bound.
	for key := range s.parsed {
		if !fresh[key] {
			delete(s.parsed, key)
		}
	}

	sort.Strings(snap.Agents)
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
// hasTruncatedPrompt reports whether a cached session contains a prompt the
// runtime dropped, which is what makes its transcript worth fetching.
func hasTruncatedPrompt(events []Event) bool {
	for _, e := range events {
		if e.Type == "prompt.submitted" && isTruncated(e) {
			return true
		}
	}
	return false
}

func needsPromptRecovery(runs []Run) bool {
	for i := range runs {
		if runs[i].PromptTruncated {
			return true
		}
	}
	return false
}

// applyTranscriptPrompts fills in prompts the trajectory lost, matching each
// truncated run to the transcript message closest to it in time.
func applyTranscriptPrompts(msgs []userMessage, runs []Run) {
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
	return readUserMessagesFrom(text)
}

func readUserMessagesFrom(text []byte) []userMessage {
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

// readSessionFile reads one session file through the source. Name validation
// lives in each source implementation, since what counts as an escape differs
// between a filesystem path and an exec argument.
func (s *Store) readSessionFile(agent, sessionID, suffix string) (string, bool) {
	if !safeNameRE.MatchString(agent) || !safeNameRE.MatchString(sessionID) {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
	defer cancel()
	body, err := s.source.read(ctx, agent, sessionID+suffix)
	if err != nil {
		return "", false
	}
	return string(body), true
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

// memoryFeed lists the Claw's durable notes newest first, with content for the
// page being shown. Attribution comes from tool calls where one recorded the
// write; notes written by background consolidation have none, and are reported
// with the agent inferred from their path rather than left out.
func (s *Store) memoryFeed(ctx context.Context, limit int) ([]MemoryWrite, *Snapshot, error) {
	snap := s.snapshot()

	notes, err := s.source.memoryNotes(ctx)
	if err != nil {
		return nil, snap, err
	}
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].ModTime > notes[j].ModTime })
	if len(notes) > limit {
		notes = notes[:limit]
	}

	// Tool calls that did record a write tell us which agent and session made
	// it; index them by note path so the file listing can be enriched.
	attribution := map[string]MemoryWrite{}
	for _, w := range extractMemoryWrites(snap.Sessions) {
		if _, seen := attribution[w.NotePath]; !seen {
			attribution[w.NotePath] = w
		}
	}

	bodies, _ := s.source.readNotes(ctx, notes)

	out := make([]MemoryWrite, 0, len(notes))
	for _, n := range notes {
		entry := MemoryWrite{
			TS:       time.UnixMilli(n.ModTime).UTC().Format(time.RFC3339Nano),
			Agent:    agentOfNotePath(n.Path),
			Tool:     "file",
			NotePath: n.Path,
			Content:  clipBytes(string(bodies[n.Path]), 4000),
		}
		// Prefer a recorded tool call's agent and session when one exists.
		for path, w := range attribution {
			if strings.HasSuffix(n.Path, path) {
				entry.Agent, entry.SessionID, entry.RunID, entry.Tool = w.Agent, w.SessionID, w.RunID, w.Tool
				break
			}
		}
		out = append(out, entry)
	}
	return out, snap, nil
}

// agentOfNotePath attributes a note to the agent whose directory holds it.
// Shared stores under workspace/ belong to no single agent.
func agentOfNotePath(p string) string {
	if strings.HasPrefix(p, "workspace/") {
		return ""
	}
	// "<agentsDirName>/<agent>/memory/..." or "<agent>/memory/..."
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if seg == "memory" && i > 0 {
			return parts[i-1]
		}
	}
	return ""
}

// wikiPages reads every wiki page with its frontmatter. Pages are fetched in
// one batch, the same way sessions are, because each read is a round trip.
func (s *Store) wikiPages(ctx context.Context) ([]WikiPage, error) {
	notes, err := s.source.memoryNotes(ctx)
	if err != nil {
		return nil, err
	}
	var wiki []memoryNote
	for _, n := range notes {
		if strings.Contains(n.Path, "/wiki/") {
			wiki = append(wiki, n)
		}
	}
	bodies, err := s.source.readNotes(ctx, wiki)
	if err != nil && len(bodies) == 0 {
		return nil, err
	}
	pages := make([]WikiPage, 0, len(wiki))
	for _, n := range wiki {
		pages = append(pages, parseWikiPage(n.Path, bodies[n.Path], n.Size, n.ModTime))
	}
	return pages, nil
}
