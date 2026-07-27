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
)

// Agent memory lives at "<home>/<agent>/memory", so the index globs
// "<home>/*/memory" — which also matches "<home>/workspace/memory", already
// listed explicitly as the shared store. find walks it twice, so the same daily
// note arrives twice and the notes tree showed it twice.
func TestNoteIndexKeepsEachFileOnce(t *testing.T) {
	home := "/home/node/.openclaw"
	// What find actually prints when the explicit path and the glob overlap.
	out := strings.Join([]string{
		home + "/workspace/memory/2026-07-26.md\t120\t1769472000.5000000000",
		home + "/workspace/memory/projects/claw-operator.md\t80\t1769472001.0000000000",
		home + "/stitch/memory/dreaming/deep/2026-07-26.md\t40\t1769472002.0000000000",
		// The glob pass repeats everything under workspace/memory.
		home + "/workspace/memory/2026-07-26.md\t120\t1769472000.5000000000",
		home + "/workspace/memory/projects/claw-operator.md\t80\t1769472001.0000000000",
	}, "\n")

	notes := parseNoteIndex(out, home)
	if len(notes) != 3 {
		t.Fatalf("notes = %d, want 3 distinct files; got %+v", len(notes), notes)
	}
	seen := map[string]int{}
	for _, n := range notes {
		seen[n.Path]++
	}
	for p, n := range seen {
		if n != 1 {
			t.Fatalf("%q appears %d times", p, n)
		}
	}
	// The first sighting wins, so size and mtime still describe the file.
	for _, n := range notes {
		if n.Path == "workspace/memory/2026-07-26.md" && (n.Size != 120 || n.ModTime != 1769472000500) {
			t.Fatalf("note lost its metadata: %+v", n)
		}
	}
}
