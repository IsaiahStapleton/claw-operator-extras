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
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObserveReportsWhatWasAppended(t *testing.T) {
	w := newMemoryWatcher("")
	now := time.Now()

	notes := []memoryNote{{Path: "workspace/memory/2026-07-26.md", Size: 10,
		ModTime: now.Add(-48 * time.Hour).UnixMilli()}}
	bodies := map[string][]byte{"workspace/memory/2026-07-26.md": []byte("## Notes\n- first entry\n")}

	// The first observation of a vault establishes a baseline and reports
	// nothing: there is no previous content to diff against, and announcing
	// every pre-existing note as a write would be false.
	if got := w.observe(notes, bodies, now); len(got) != 0 {
		t.Fatalf("first observation produced %d events, want 0", len(got))
	}

	later := now.Add(2 * time.Minute)
	notes[0].Size, notes[0].ModTime = 40, later.UnixMilli()
	bodies[notes[0].Path] = []byte("## Notes\n- first entry\n- second entry\n")

	events := w.observe(notes, bodies, later)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.Added != 1 || len(e.AddedLines) != 1 || !strings.Contains(e.AddedLines[0], "second entry") {
		t.Fatalf("event = %+v, want the one appended line", e)
	}
	if e.Created {
		t.Fatal("a change to a known note is not a creation")
	}
	if e.NotePath != "workspace/memory/2026-07-26.md" {
		t.Fatalf("notePath = %q", e.NotePath)
	}
}

// Every event in the feed must carry a real diff. A note the watcher has never
// seen is recorded silently rather than reported with no added lines.
func TestObserveNeverReportsAnEventWithoutADiff(t *testing.T) {
	w := newMemoryWatcher("")
	now := time.Now()
	path := "workspace/memory/2026-07-26.md"
	notes := []memoryNote{{Path: path, Size: 10, ModTime: now.Add(-10 * time.Minute).UnixMilli()}}
	bodies := map[string][]byte{path: []byte("- something\n")}

	for _, e := range w.observe(notes, bodies, now) {
		if e.Added == 0 && e.Removed == 0 {
			t.Fatalf("event with no diff reached the feed: %+v", e)
		}
	}
	if w.watchingFrom() == "" {
		t.Fatal("watchingSince should be set so the UI can say how far back it knows")
	}
}

// A note that appears after the baseline is a genuine write, and its whole
// content is genuinely new, so it is reported with every line as an addition.
func TestObserveReportsANewNoteInFull(t *testing.T) {
	w := newMemoryWatcher("")
	now := time.Now()
	first := []memoryNote{{Path: "workspace/memory/a.md", Size: 2, ModTime: now.UnixMilli()}}
	w.observe(first, map[string][]byte{"workspace/memory/a.md": []byte("x\n")}, now)

	later := now.Add(time.Minute)
	notes := append(first, memoryNote{Path: "stitch/memory/dreaming/deep/b.md",
		Size: 20, ModTime: later.UnixMilli()})
	bodies := map[string][]byte{"stitch/memory/dreaming/deep/b.md": []byte("- one\n\n- two\n")}

	events := w.observe(notes, bodies, later)
	if len(events) != 1 {
		t.Fatalf("events = %d, want the new note reported", len(events))
	}
	e := events[0]
	if !e.Created {
		t.Fatal("a note that did not exist before is a creation")
	}
	if e.Added != 2 || len(e.AddedLines) != 2 {
		t.Fatalf("event = %+v, want both non-blank lines as additions", e)
	}
	if e.Agent != "stitch" {
		t.Fatalf("agent = %q, want the write attributed to stitch", e.Agent)
	}
}

func TestObserveIgnoresUnchangedNotes(t *testing.T) {
	w := newMemoryWatcher("")
	now := time.Now()
	notes := []memoryNote{{Path: "workspace/memory/a.md", Size: 3, ModTime: now.UnixMilli()}}
	bodies := map[string][]byte{"workspace/memory/a.md": []byte("x\n")}
	w.observe(notes, bodies, now)
	if got := w.observe(notes, bodies, now); len(got) != 0 {
		t.Fatalf("unchanged note produced %d events", len(got))
	}
	if pending := w.pending(notes); len(pending) != 0 {
		t.Fatalf("unchanged note is still pending a read: %+v", pending)
	}
}

// pending is what keeps the poll cheap: only notes whose size or mtime moved
// are worth pulling across an exec channel.
func TestPendingSelectsOnlyChangedNotes(t *testing.T) {
	w := newMemoryWatcher("")
	now := time.Now()
	a := memoryNote{Path: "workspace/memory/a.md", Size: 3, ModTime: now.UnixMilli()}
	b := memoryNote{Path: "workspace/memory/b.md", Size: 3, ModTime: now.UnixMilli()}
	if got := w.pending([]memoryNote{a, b}); len(got) != 2 {
		t.Fatalf("pending on a cold watcher = %d, want both", len(got))
	}
	w.observe([]memoryNote{a, b}, map[string][]byte{a.Path: []byte("x\n"), b.Path: []byte("y\n")}, now)

	b.Size, b.ModTime = 6, now.Add(time.Minute).UnixMilli()
	got := w.pending([]memoryNote{a, b})
	if len(got) != 1 || got[0].Path != b.Path {
		t.Fatalf("pending = %+v, want only the changed note", got)
	}
}

// The whole point of persisting: a write that happens while the console is down
// still shows its real lines on the next read, instead of the record silently
// starting over.
func TestObservationsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.json")
	now := time.Now()
	note := memoryNote{Path: "workspace/memory/2026-07-26.md", Size: 10, ModTime: now.UnixMilli()}

	first := newMemoryWatcher(path)
	first.observe([]memoryNote{note}, map[string][]byte{note.Path: []byte("## Notes\n- one\n")}, now)

	// A fresh process, as after a redeploy. The note changed while it was down.
	later := now.Add(time.Hour)
	note.Size, note.ModTime = 30, later.UnixMilli()
	restarted := newMemoryWatcher(path)
	if !restarted.persistent() {
		t.Fatal("a watcher with a state file is persistent")
	}
	pending := restarted.pending([]memoryNote{note})
	if len(pending) != 1 {
		t.Fatalf("pending after restart = %d, want the changed note", len(pending))
	}
	events := restarted.observe([]memoryNote{note},
		map[string][]byte{note.Path: []byte("## Notes\n- one\n- two\n")}, later)
	if len(events) != 1 {
		t.Fatalf("events after restart = %d, want the write that happened while down", len(events))
	}
	if events[0].Added != 1 || !strings.Contains(events[0].AddedLines[0], "two") {
		t.Fatalf("event = %+v, want the appended line recovered across the restart", events[0])
	}
	if events[0].Created {
		t.Fatal("the note existed before the restart; it was edited, not created")
	}
	// And the history itself survives, not just the baseline.
	if got := newMemoryWatcher(path).recent(10); len(got) != 1 {
		t.Fatalf("recent after a second restart = %d, want the retained event", len(got))
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
