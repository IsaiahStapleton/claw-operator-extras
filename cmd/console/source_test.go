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
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseIndexOutputDecodesAgentConfig(t *testing.T) {
	config := `{"agents":{"list":[{"id":"main","identity":{"name":"Shifty"}}]}}`
	out := "A\tmain\n" +
		"F\tmain/sessions/s1.trajectory.jsonl\t10\t1700000000.123\n" +
		"C\t" + base64.StdEncoding.EncodeToString([]byte(config)) + "\n"
	agents, files, got := parseIndexOutput(out)
	if len(agents) != 1 || agents[0] != "main" {
		t.Fatalf("agents = %v", agents)
	}
	if len(files) != 1 || files[0].Name != "s1.trajectory.jsonl" {
		t.Fatalf("files = %v", files)
	}
	if string(got) != config {
		t.Fatalf("config = %q, want the decoded openclaw.json", got)
	}
	if ids := parseAgentIdentities(got); ids["main"].Title != "Shifty" {
		t.Fatalf("identities = %v, want Shifty for main", ids)
	}

	// A missing file yields an empty C row; garbage never becomes config.
	if _, _, cfg := parseIndexOutput("C\t\n"); len(cfg) != 0 {
		t.Fatalf("empty C row should carry no config, got %q", cfg)
	}
	if _, _, cfg := parseIndexOutput("C\t!!!not-base64\n"); cfg != nil {
		t.Fatalf("undecodable C row should be ignored, got %q", cfg)
	}
}

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

func TestSafeNotePathRejectsTraversal(t *testing.T) {
	cases := map[string]bool{
		"workspace/memory/2026-07-29.md":   true,
		"stitch/memory/dreaming/deep/x.md": true,
		"../../etc/passwd":                 false,
		"workspace/../../../etc/passwd":    false,
		"a/../b.md":                        false,
		"/etc/passwd":                      false,
		"":                                 false,
	}
	for p, want := range cases {
		if got := safeNotePath(p); got != want {
			t.Errorf("safeNotePath(%q) = %v, want %v", p, got, want)
		}
	}
}
