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

func TestObserveNotesReportsWhatWasAppended(t *testing.T) {
	s := &Store{now: time.Now}
	now := time.Now()

	// Old enough that its first sighting is bookkeeping rather than news.
	notes := []memoryNote{{Path: "workspace/memory/2026-07-26.md", Size: 10,
		ModTime: now.Add(-48 * time.Hour).UnixMilli()}}
	bodies := map[string][]byte{"workspace/memory/2026-07-26.md": []byte("## Notes\n- first entry\n")}

	// A first sighting of an old note is not an observed write: reporting it
	// would announce the entire vault as freshly written on startup.
	if got := s.observeNotes(notes, bodies, now); len(got) != 0 {
		t.Fatalf("first sighting of an old note produced %d events, want 0", len(got))
	}

	later := now.Add(2 * time.Minute)
	notes[0].Size, notes[0].ModTime = 40, later.UnixMilli()
	bodies[notes[0].Path] = []byte("## Notes\n- first entry\n- second entry\n")

	events := s.observeNotes(notes, bodies, later)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.Added != 1 || len(e.AddedLines) != 1 || !strings.Contains(e.AddedLines[0], "second entry") {
		t.Fatalf("event = %+v, want the one appended line", e)
	}
	if e.FirstSeen {
		t.Fatal("a change to a known note is not a first sighting")
	}
	if e.NotePath != "workspace/memory/2026-07-26.md" {
		t.Fatalf("notePath = %q", e.NotePath)
	}
}

func TestObserveNotesAttributesPerAgentNotes(t *testing.T) {
	s := &Store{now: time.Now}
	now := time.Now()
	path := "stitch/memory/dreaming/deep/2026-07-26.md"
	notes := []memoryNote{{Path: path, Size: 5, ModTime: now.UnixMilli()}}
	bodies := map[string][]byte{path: []byte("a\n")}
	s.observeNotes(notes, bodies, now)

	notes[0].ModTime = now.Add(time.Minute).UnixMilli()
	bodies[path] = []byte("a\nb\n")
	events := s.observeNotes(notes, bodies, now.Add(time.Minute))
	if len(events) != 1 || events[0].Agent != "stitch" {
		t.Fatalf("events = %+v, want the write attributed to stitch", events)
	}
}

func TestObserveNotesIgnoresUnchangedNotes(t *testing.T) {
	s := &Store{now: time.Now}
	now := time.Now()
	notes := []memoryNote{{Path: "workspace/memory/a.md", Size: 3, ModTime: now.UnixMilli()}}
	bodies := map[string][]byte{"workspace/memory/a.md": []byte("x\n")}
	s.observeNotes(notes, bodies, now)
	if got := s.observeNotes(notes, bodies, now); len(got) != 0 {
		t.Fatalf("unchanged note produced %d events", len(got))
	}
}

func TestDiffLinesCountsRemovals(t *testing.T) {
	added, removed := diffLines([]string{"a", "b", "c"}, []string{"a", "c", "d"})
	if len(added) != 1 || added[0] != "d" {
		t.Fatalf("added = %v, want [d]", added)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (b is gone)", removed)
	}
}

// A note touched just before watching began is real recent activity. It is
// reported, but without added lines, because the previous content was never
// seen and inventing a diff would be a lie.
func TestColdStartReportsRecentlyTouchedNotesWithoutADiff(t *testing.T) {
	s := &Store{now: time.Now}
	now := time.Now()
	path := "workspace/memory/2026-07-26.md"
	notes := []memoryNote{{Path: path, Size: 10, ModTime: now.Add(-10 * time.Minute).UnixMilli()}}
	bodies := map[string][]byte{path: []byte("- something\n")}

	events := s.observeNotes(notes, bodies, now)
	if len(events) != 1 {
		t.Fatalf("events = %d, want the recent note surfaced on a cold start", len(events))
	}
	if !events[0].FirstSeen {
		t.Fatal("it must be marked first-seen so the UI does not claim to know what changed")
	}
	if events[0].Added != 0 || len(events[0].AddedLines) != 0 {
		t.Fatalf("event = %+v, want no diff claimed for content never previously seen", events[0])
	}
	if s.watchingFrom() == "" {
		t.Fatal("watchingSince should be set so the UI can say how far back it knows")
	}
}
