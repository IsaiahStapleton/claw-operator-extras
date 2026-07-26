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

// Detecting what an agent actually wrote to memory, and when.
//
// Nothing in OpenClaw records this. Consolidation and wiki synthesis write
// files directly, with no trajectory event and no commit, so the only way to
// know what changed is to watch. The console keeps the last content it saw for
// each note and diffs on change, which yields the added lines and the moment
// they appeared.
//
// The state is persisted, and that is what makes the feed useful rather than a
// curiosity. Held only in memory it was lost on every restart, and the baseline
// was re-taken in silence — so the page reported nothing had happened when in
// fact the console had simply forgotten. Written to disk, the first read after
// a restart diffs against the last content actually seen, so a write that
// happened while the console was down still appears with its real lines.
//
// The watcher is shared by every user who can read a Claw, because it describes
// the Claw and not the viewer. That is safe precisely because reaching it at
// all requires an impersonated read that the API server already authorized.
//
// Two honest limits remain. Detection advances only when someone loads the
// memory page — the console cannot poll on its own, since it holds no standing
// permission to read any Claw. And several writes to one note between two reads
// are reported as the one diff that spans them.

package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryWriteEvent is one observed change to a note, with the lines that
// appeared. Every event carries a real diff: a note seen for the first time
// establishes a baseline silently rather than being announced as a write.
type MemoryWriteEvent struct {
	TS         string   `json:"ts"`
	Agent      string   `json:"agent"`
	NotePath   string   `json:"notePath"`
	AddedLines []string `json:"addedLines"`
	Added      int      `json:"added"`
	Removed    int      `json:"removed"`
	// Truncated is how many added lines were withheld from AddedLines to keep
	// one bulk write from dominating the feed. Added still counts them all.
	Truncated int `json:"truncated,omitempty"`
	// Created marks a note that did not exist at the previous observation, so
	// the UI can say "new note" rather than implying an edit.
	Created bool `json:"created,omitempty"`
}

// noteSnapshot is the last content seen for a note.
type noteSnapshot struct {
	ModTime int64    `json:"modTime"`
	Size    int64    `json:"size"`
	Lines   []string `json:"lines"`
}

const (
	// maxWriteEvents bounds the retained history per Claw.
	maxWriteEvents = 500
	// maxWatchedBytes bounds the content held for diffing. A Claw's whole
	// memory tree is a few megabytes, so this is generous.
	maxWatchedBytes = 24 << 20
	// maxAddedLinesPerEvent keeps one bulk write from dominating the feed.
	// The full count is still reported.
	maxAddedLinesPerEvent = 40
)

// memoryWatcher observes one Claw's notes and remembers what it saw.
type memoryWatcher struct {
	mu sync.Mutex
	// path is where the state is persisted. Empty means memory only, which is
	// what local development and the tests use.
	path   string
	loaded bool
	since  time.Time
	notes  map[string]noteSnapshot
	events []MemoryWriteEvent
}

// watchState is the on-disk form.
type watchState struct {
	Since  time.Time               `json:"since"`
	Notes  map[string]noteSnapshot `json:"notes"`
	Events []MemoryWriteEvent      `json:"events"`
}

func newMemoryWatcher(path string) *memoryWatcher {
	return &memoryWatcher{path: path, notes: map[string]noteSnapshot{}}
}

// watcherPath returns the state file for one Claw, or "" when no state
// directory is configured. Keyed by Claw rather than by user: the observation
// is a property of the Claw, and every user who can reach it is authorized to
// read the same notes.
func watcherPath(stateDir, namespace, claw string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "watch-"+namespace+"--"+claw+".json")
}

// persistent reports whether the record survives a restart, so the UI can say
// which of the two it is rather than leaving the user to guess.
func (w *memoryWatcher) persistent() bool { return w.path != "" }

// load reads persisted state once. A missing file is the normal first run. A
// corrupt one is logged and discarded rather than failing the request: losing
// the history is recoverable, refusing to show the memory page is not.
func (w *memoryWatcher) load() {
	if w.loaded {
		return
	}
	w.loaded = true
	if w.path == "" {
		return
	}
	body, err := os.ReadFile(w.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("memory watch state unreadable (%s): %v", w.path, err)
		}
		return
	}
	var st watchState
	if err := json.Unmarshal(body, &st); err != nil {
		log.Printf("memory watch state discarded (%s): %v", w.path, err)
		return
	}
	if st.Notes != nil {
		w.notes = st.Notes
	}
	w.since, w.events = st.Since, st.Events
}

// save writes the state atomically, so a crash mid-write cannot leave a
// truncated file that the next start would discard.
func (w *memoryWatcher) save() {
	if w.path == "" {
		return
	}
	body, err := json.Marshal(watchState{Since: w.since, Notes: w.notes, Events: w.events})
	if err != nil {
		log.Printf("memory watch state not saved: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		log.Printf("memory watch state not saved: %v", err)
		return
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		log.Printf("memory watch state not saved: %v", err)
		return
	}
	if err := os.Rename(tmp, w.path); err != nil {
		log.Printf("memory watch state not saved: %v", err)
		_ = os.Remove(tmp)
	}
}

// pending narrows a note index to the ones whose content the watcher must
// actually fetch: new notes, and notes whose size or mtime moved.
//
// This is what keeps the memory page cheap enough to poll. Reading every note
// on every refresh meant a tar of the entire vault every five seconds; reading
// only what changed means the steady state transfers nothing at all, and the
// full read happens once, when the baseline is established.
func (w *memoryWatcher) pending(notes []memoryNote) []memoryNote {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()
	var out []memoryNote
	for _, n := range notes {
		prev, seen := w.notes[n.Path]
		if !seen || prev.ModTime != n.ModTime || prev.Size != n.Size {
			out = append(out, n)
		}
	}
	return out
}

// observe diffs the current notes against what was last seen and records an
// event per change. Returns the events newly observed.
//
// The very first observation of a vault records a baseline and reports nothing.
// There is no previous content to diff against, and announcing several hundred
// pre-existing notes as writes would be a lie told at the exact moment the
// feed is meant to establish its credibility.
func (w *memoryWatcher) observe(notes []memoryNote, bodies map[string][]byte, now time.Time) []MemoryWriteEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()

	cold := w.since.IsZero()
	if cold {
		w.since = now
	}
	var fresh []MemoryWriteEvent

	for _, n := range notes {
		body, ok := bodies[n.Path]
		if !ok {
			continue
		}
		lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
		prev, seen := w.notes[n.Path]
		if seen && prev.ModTime == n.ModTime && prev.Size == n.Size {
			continue // unchanged
		}
		w.notes[n.Path] = noteSnapshot{ModTime: n.ModTime, Size: n.Size, Lines: lines}
		if cold {
			continue // baseline only
		}

		event := MemoryWriteEvent{
			TS:       time.UnixMilli(n.ModTime).UTC().Format(time.RFC3339Nano),
			Agent:    agentOfNotePath(n.Path),
			NotePath: n.Path,
			Created:  !seen,
		}
		// A note that did not exist last time is diffed against nothing, which
		// makes every one of its lines an addition — a real diff, not a guess.
		var added []string
		if seen {
			added, event.Removed = diffLines(prev.Lines, lines)
		} else {
			added = nonBlank(lines)
		}
		event.Added = len(added)
		if event.Added == 0 && event.Removed == 0 {
			continue // touched but identical; nothing to report
		}
		if len(added) > maxAddedLinesPerEvent {
			event.Truncated = len(added) - maxAddedLinesPerEvent
			added = added[:maxAddedLinesPerEvent]
		}
		event.AddedLines = added
		fresh = append(fresh, event)
	}

	w.trim()
	w.events = append(w.events, fresh...)
	if len(w.events) > maxWriteEvents {
		w.events = w.events[len(w.events)-maxWriteEvents:]
	}
	if len(fresh) > 0 || cold {
		w.save()
	}
	return fresh
}

func nonBlank(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// diffLines reports lines present in next but not prev, and how many were
// dropped. Notes are append-mostly, so a set difference tells the truth
// without the cost of a real edit-distance diff.
func diffLines(prev, next []string) ([]string, int) {
	prevCount := map[string]int{}
	for _, l := range prev {
		prevCount[strings.TrimSpace(l)]++
	}
	var added []string
	for _, l := range next {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if prevCount[t] > 0 {
			prevCount[t]--
			continue
		}
		added = append(added, l)
	}
	removed := 0
	for l, n := range prevCount {
		if l != "" {
			removed += n
		}
	}
	return added, removed
}

// trim drops the oldest notes once the retained content exceeds the budget, so
// a long-lived console watching many Claws stays bounded.
func (w *memoryWatcher) trim() {
	total := 0
	for _, snap := range w.notes {
		for _, l := range snap.Lines {
			total += len(l) + 1
		}
	}
	if total <= maxWatchedBytes {
		return
	}
	type aged struct {
		path string
		mod  int64
	}
	all := make([]aged, 0, len(w.notes))
	for p, snap := range w.notes {
		all = append(all, aged{p, snap.ModTime})
	}
	// Oldest first; those are least likely to change again.
	sort.SliceStable(all, func(i, j int) bool { return all[i].mod < all[j].mod })
	for _, a := range all {
		if total <= maxWatchedBytes {
			break
		}
		for _, l := range w.notes[a.path].Lines {
			total -= len(l) + 1
		}
		delete(w.notes, a.path)
	}
}

// watchingFrom reports when this Claw's notes were first observed, so the UI
// can say how far back its knowledge goes instead of implying nothing ever
// happened.
func (w *memoryWatcher) watchingFrom() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()
	if w.since.IsZero() {
		return ""
	}
	return w.since.UTC().Format(time.RFC3339Nano)
}

// recent returns observed writes, newest first.
func (w *memoryWatcher) recent(limit int) []MemoryWriteEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()
	out := make([]MemoryWriteEvent, 0, len(w.events))
	for i := len(w.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, w.events[i])
	}
	return out
}
