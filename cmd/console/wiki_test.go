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
	// Every generated index is called index.md, so naming one after its own
	// file gives a graph full of nodes labelled "index". It takes the name of
	// the section it indexes.
	if p.Title != "Concepts" {
		t.Fatalf("title = %q, want the section the index belongs to", p.Title)
	}
	if root := parseWikiPage("workspace/wiki/main/index.md", nil, 0, 0); root.Title != "Wiki index" {
		t.Fatalf("root index title = %q", root.Title)
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
	}
	g := buildWikiGraph(pages)

	if len(g.Pages) != 2 {
		t.Fatalf("synthesized pages = %d, want 2 (concept, entity)", len(g.Pages))
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

// Edges are expressed in node keys, so every node must carry the key its edges
// name it by. A consumer that keyed on id instead saw all four index pages
// collapse onto the empty string and silently dropped every edge to them, which
// rendered them as orphans while the API still reported the edges.
func TestEveryNodeCarriesTheKeyItsEdgesUse(t *testing.T) {
	pages := []WikiPage{
		{Path: "wiki/main/entities/stitch.md", ID: "entity.stitch", PageType: "entity", Title: "Stitch"},
		{Path: "wiki/main/syntheses/a.md", ID: "synthesis.a", PageType: "synthesis", Title: "A"},
		{Path: "wiki/main/index.md", Title: "Wiki index",
			Links: []string{"wiki/main/entities/stitch.md", "wiki/main/syntheses/a.md"}},
	}
	g := buildWikiGraph(pages)

	keys := map[string]bool{}
	for _, p := range g.Pages {
		if p.Key == "" {
			t.Fatalf("page %q has no key; its edges cannot resolve to it", p.Path)
		}
		if keys[p.Key] {
			t.Fatalf("key %q is used by two nodes", p.Key)
		}
		keys[p.Key] = true
	}
	if len(g.Edges) != 2 {
		t.Fatalf("edges = %+v, want both root-index links", g.Edges)
	}
	for _, e := range g.Edges {
		if !keys[e.From] || !keys[e.To] {
			t.Fatalf("edge %+v names a key no node carries; it would be dropped when drawn", e)
		}
	}
	// And nothing is left floating once the edges resolve.
	deg := map[string]int{}
	for _, e := range g.Edges {
		deg[e.From]++
		deg[e.To]++
	}
	for _, p := range g.Pages {
		if deg[p.Key] == 0 {
			t.Fatalf("%q is an orphan though it is linked", p.Title)
		}
	}
}

// The graph must show what the wiki knows, not how it is maintained. Reports
// are generated dashboards that link to nothing, vault plumbing is instructions
// to the agent, and the quarantine holds superseded copies OpenClaw parks there
// when it retypes a page — which is why a real vault produced two "Stitch"
// nodes, one of them an orphan.
func TestGraphExcludesWikiMachinery(t *testing.T) {
	pages := []WikiPage{
		{Path: "workspace/wiki/main/entities/stitch.md", ID: "entity.stitch", PageType: "entity", Title: "Stitch"},
		{Path: "workspace/wiki/main/.openclaw-wiki/quarantine/retyped-stitch/stitch.synthesis.md",
			ID: "synthesis.stitch", PageType: "synthesis", Title: "Stitch"},
		{Path: "workspace/wiki/main/reports/lint.md", ID: "report.lint", PageType: "report", Title: "Lint Report"},
		{Path: "workspace/wiki/main/reports/index.md", Title: "Reports"},
		{Path: "workspace/wiki/main/AGENTS.md", Title: "Agent guide"},
		{Path: "workspace/wiki/main/WIKI.md", Title: "Memory Wiki"},
		{Path: "workspace/wiki/main/inbox.md", Title: "Inbox"},
		// A per-type index says only "these pages are entities", which the
		// graph already says with colour.
		{Path: "workspace/wiki/main/entities/index.md", Title: "Entities",
			Links: []string{"workspace/wiki/main/entities/stitch.md"}},
		// The root index links across types and is what keeps otherwise
		// separate clusters joined, so it stays.
		{Path: "workspace/wiki/main/index.md", Title: "Wiki index",
			Links: []string{"workspace/wiki/main/entities/stitch.md"}},
	}
	g := buildWikiGraph(pages)

	titles := map[string]int{}
	for _, p := range g.Pages {
		titles[p.Title]++
	}
	if titles["Stitch"] != 1 {
		t.Fatalf("Stitch appears %d times; the quarantined copy must not become a second node", titles["Stitch"])
	}
	if len(g.Pages) != 2 {
		t.Fatalf("pages = %+v, want only the entity and the root index", g.Pages)
	}
	for _, p := range g.Pages {
		if p.Title == "Entities" {
			t.Fatal("a per-type index adds a hub the colour already conveys")
		}
	}
	if g.Counts["report"] != 0 {
		t.Fatalf("counts = %v, want reports excluded entirely", g.Counts)
	}
	// The root index still carries its edge, so pruning machinery does not cost
	// the graph its structure.
	if len(g.Edges) != 1 || g.Edges[0].To != "entity.stitch" {
		t.Fatalf("edges = %+v, want the root index link to Stitch kept", g.Edges)
	}
}

// Obsidian builds its graph from body links, and so must this. Reading only
// frontmatter left almost every page disconnected, which is not what the same
// vault looks like in Obsidian.
func TestExtractLinksResolvesBodyLinks(t *testing.T) {
	body := []byte(`# Sources

- [Memory Bridge](bridge-stitch-abc.md)
- [Up one level](../index.md)
- [[stitch-operating-model]]
- [external](https://example.com/page.md)
- [anchored](claim-health.md#section)
`)
	links := extractLinks("workspace/wiki/main/sources/index.md", body)

	want := map[string]bool{
		"workspace/wiki/main/sources/bridge-stitch-abc.md":      true,
		"workspace/wiki/main/index.md":                          true,
		"workspace/wiki/main/sources/stitch-operating-model.md": true,
		"workspace/wiki/main/sources/claim-health.md":           true,
	}
	if len(links) != len(want) {
		t.Fatalf("links = %v, want %d entries", links, len(want))
	}
	for _, l := range links {
		if !want[l] {
			t.Fatalf("unexpected link %q (an external URL must not become a node)", l)
		}
	}
}

func TestBodyLinksConnectPagesIncludingIndexes(t *testing.T) {
	pages := []WikiPage{
		// An index page has no frontmatter at all, yet holds the wiki's
		// link structure; excluding it disconnected the graph.
		{Path: "wiki/main/index.md", Title: "index",
			Links: []string{"wiki/main/concepts/a.md"}},
		{Path: "wiki/main/concepts/a.md", ID: "concept.a", PageType: "concept", Title: "A",
			Links: []string{"wiki/main/index.md"}},
	}
	g := buildWikiGraph(pages)

	if len(g.Pages) != 2 {
		t.Fatalf("pages = %d, want the concept and the index", len(g.Pages))
	}
	if len(g.Edges) != 2 {
		t.Fatalf("edges = %+v, want a link each way", g.Edges)
	}
	for _, e := range g.Edges {
		if e.Kind != "link" {
			t.Fatalf("edge kind = %q, want link", e.Kind)
		}
	}
	if g.Counts["index"] != 1 {
		t.Fatalf("counts = %v, want the untyped page counted as an index", g.Counts)
	}
}
