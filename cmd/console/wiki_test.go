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
	"testing"
	"time"
)

// Frontmatter shape taken from a real page in podling's wiki.
const conceptPage = `---
pageType: concept
id: concept.stitch-operating-model
title: Stitch Operating Model
aliases:
  - Stitch workflow
privacyTier: internal
lastRefreshedAt: "2026-07-23T00:49:00-04:00"
sourceIds:
  - source.bridge.workspace-c17d7387.memory-2026-07-22
relationships:
  - targetId: entity.stitch
    targetTitle: Stitch
    kind: governs
    weight: 1
    confidence: 1
claims:
  - id: stitch-operating-model
    text: Stitch uses immutable snapshots and fail-closed evidence gates.
    status: supported
---

# Stitch Operating Model
`

func TestParseWikiPageReadsDeclaredStructure(t *testing.T) {
	p := parseWikiPage("workspace/wiki/main/concepts/stitch-operating-model.md",
		[]byte(conceptPage), 1234, time.Now().UnixMilli())

	if p.ID != "concept.stitch-operating-model" || p.PageType != "concept" {
		t.Fatalf("id/type = %q/%q", p.ID, p.PageType)
	}
	if p.Title != "Stitch Operating Model" {
		t.Fatalf("title = %q", p.Title)
	}
	if p.LastRefreshedAt == "" {
		t.Fatal("lastRefreshedAt is the only per-page timing available; it must survive parsing")
	}
	if len(p.Relationships) != 1 || p.Relationships[0].Kind != "governs" || p.Relationships[0].TargetID != "entity.stitch" {
		t.Fatalf("relationships = %+v", p.Relationships)
	}
	if len(p.Claims) != 1 || p.Claims[0].Status != "supported" {
		t.Fatalf("claims = %+v", p.Claims)
	}
}

// A page with no frontmatter (the generated index pages have none) must still
// be listed, or the browser understates what is in the wiki.
func TestPageWithoutFrontmatterStillListed(t *testing.T) {
	p := parseWikiPage("workspace/wiki/main/concepts/index.md", []byte("# Concepts\n"), 10, time.Now().UnixMilli())
	if p.Title != "index" {
		t.Fatalf("title = %q, want a title derived from the path", p.Title)
	}
	if p.PageType != "" {
		t.Fatalf("pageType = %q, want empty rather than guessed", p.PageType)
	}
}

func TestGraphSeparatesSynthesizedLayerFromSources(t *testing.T) {
	pages := []WikiPage{
		{ID: "concept.a", PageType: "concept", Title: "A",
			Relationships: []WikiRel{{TargetID: "entity.b", Kind: "governs", Weight: 1}},
			SourceIDs:     []string{"source.one", "source.missing"}},
		{ID: "entity.b", PageType: "entity", Title: "B"},
		{ID: "source.one", PageType: "source", Title: "Bridge import"},
		{ID: "report.c", PageType: "report", Title: "Open Questions"},
	}
	g := buildWikiGraph(pages)

	if len(g.Pages) != 3 {
		t.Fatalf("synthesized pages = %d, want 3 (concept, entity, report)", len(g.Pages))
	}
	if len(g.Sources) != 1 {
		t.Fatalf("sources = %d, want 1 held back for expansion", len(g.Sources))
	}
	// One declared relationship plus one resolvable provenance edge; the
	// dangling source reference is dropped rather than drawn.
	if len(g.Edges) != 2 {
		t.Fatalf("edges = %+v, want the governs edge and one source edge", g.Edges)
	}
	kinds := map[string]bool{}
	for _, e := range g.Edges {
		kinds[e.Kind] = true
	}
	if !kinds["governs"] || !kinds["source"] {
		t.Fatalf("edge kinds = %v", kinds)
	}
	if g.Counts["source"] != 1 || g.Counts["concept"] != 1 {
		t.Fatalf("counts = %v", g.Counts)
	}
}
