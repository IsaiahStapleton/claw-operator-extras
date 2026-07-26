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
// The honest limit: this only knows about writes that happen while the console
// is watching. Nothing before it started is recoverable, and two writes inside
// one poll interval are reported as one.

package main

import (
	"strings"
	"time"
)

// MemoryWriteEvent is one observed change to a note.
type MemoryWriteEvent struct {
	TS         string   `json:"ts"`
	Agent      string   `json:"agent"`
	NotePath   string   `json:"notePath"`
	AddedLines []string `json:"addedLines"`
	Added      int      `json:"added"`
	Removed    int      `json:"removed"`
	// FirstSeen marks a note the console had never seen before. It is not
	// necessarily new — it may simply predate the console — so the UI can
	// avoid claiming an agent "just wrote" a file that is months old.
	FirstSeen bool `json:"firstSeen"`
}

// noteSnapshot is the last content seen for a note.
type noteSnapshot struct {
	modTime int64
	size    int64
	lines   []string
}

const (
	// recentOnFirstSight is how far back a note's mtime may be for the first
	// observation to report it. On a cold start there is no previous content
	// to diff against, but a recently touched note is still news worth showing
	// — flagged so the UI does not claim to know what changed. A day covers a
	// fleet whose consolidation runs on a daily cron, which is the cadence
	// these notes are actually written at.
	recentOnFirstSight = 24 * time.Hour
	// maxWriteEvents bounds the retained history per Claw.
	maxWriteEvents = 500
	// maxWatchedBytes bounds the content held for diffing. A Claw's whole
	// memory tree is a few megabytes, so this is generous.
	maxWatchedBytes = 24 << 20
	// maxAddedLinesPerEvent keeps one bulk import from dominating the feed.
	maxAddedLinesPerEvent = 40
)

// observeNotes diffs the current notes against what was last seen and records
// an event per change. Returns the events newly observed.
func (s *Store) observeNotes(notes []memoryNote, bodies map[string][]byte, now time.Time) []MemoryWriteEvent {
	if s.watched == nil {
		s.watched = map[string]noteSnapshot{}
	}
	cold := s.watchingSince.IsZero()
	if cold {
		s.watchingSince = now
	}
	var fresh []MemoryWriteEvent

	for _, n := range notes {
		body, ok := bodies[n.Path]
		if !ok {
			continue
		}
		lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
		prev, seen := s.watched[n.Path]

		if seen && prev.modTime == n.ModTime && prev.size == n.Size {
			continue // unchanged
		}

		event := MemoryWriteEvent{
			TS:        time.UnixMilli(n.ModTime).UTC().Format(time.RFC3339Nano),
			Agent:     agentOfNotePath(n.Path),
			NotePath:  n.Path,
			FirstSeen: !seen,
		}
		if seen {
			added, removed := diffLines(prev.lines, lines)
			event.Added, event.Removed = len(added), removed
			if len(added) > maxAddedLinesPerEvent {
				added = added[:maxAddedLinesPerEvent]
			}
			event.AddedLines = added
		} else {
			event.Added = len(lines)
		}

		s.watched[n.Path] = noteSnapshot{modTime: n.ModTime, size: n.Size, lines: lines}
		if seen {
			fresh = append(fresh, event)
			continue
		}
		// A first sighting of an old note is bookkeeping — reporting it would
		// announce the whole vault as freshly written. A note touched just
		// before watching began is different: it is real recent activity, and
		// worth showing even though its previous content is unknowable.
		if cold && now.Sub(time.UnixMilli(n.ModTime)) <= recentOnFirstSight {
			event.AddedLines = nil
			event.Added = 0
			fresh = append(fresh, event)
		}
	}

	s.trimWatched()
	s.writeEvents = append(s.writeEvents, fresh...)
	if len(s.writeEvents) > maxWriteEvents {
		s.writeEvents = s.writeEvents[len(s.writeEvents)-maxWriteEvents:]
	}
	return fresh
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
	nextSet := map[string]int{}
	for _, l := range next {
		t := strings.TrimSpace(l)
		nextSet[t]++
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

// trimWatched drops the oldest notes once the retained content exceeds the
// budget, so a long-lived console watching many Claws stays bounded.
func (s *Store) trimWatched() {
	total := 0
	for _, snap := range s.watched {
		for _, l := range snap.lines {
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
	all := make([]aged, 0, len(s.watched))
	for p, snap := range s.watched {
		all = append(all, aged{p, snap.modTime})
	}
	// Oldest first; those are least likely to change again.
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].mod < all[i].mod {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	for _, a := range all {
		if total <= maxWatchedBytes {
			break
		}
		for _, l := range s.watched[a.path].lines {
			total -= len(l) + 1
		}
		delete(s.watched, a.path)
	}
}

// watchingFrom reports when this store began watching, so the UI can say how
// far back its knowledge goes instead of implying nothing ever happened.
func (s *Store) watchingFrom() string {
	if s.watchingSince.IsZero() {
		return ""
	}
	return s.watchingSince.UTC().Format(time.RFC3339Nano)
}

// recentWrites returns observed writes, newest first.
func (s *Store) recentWrites(limit int) []MemoryWriteEvent {
	out := make([]MemoryWriteEvent, 0, len(s.writeEvents))
	for i := len(s.writeEvents) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.writeEvents[i])
	}
	return out
}
