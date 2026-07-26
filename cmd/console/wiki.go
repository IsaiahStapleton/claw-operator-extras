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

// The memory wiki as a knowledge graph.
//
// OpenClaw's wiki pages carry structured YAML frontmatter — a stable id, a
// page type, typed relationships with weight and confidence, provenance
// sourceIds, and claims with a support status. The graph is therefore
// declared, not inferred from link text, which makes it both cheaper and more
// truthful than scraping [[wikilinks]].

package main

import (
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Page types that make up the synthesized layer — what the wiki has actually
// concluded, as opposed to the raw memory it imported.
var synthesizedTypes = map[string]bool{
	"concept": true, "entity": true, "synthesis": true, "report": true,
}

// WikiRel is a typed edge the page declares to another page.
type WikiRel struct {
	TargetID    string  `yaml:"targetId" json:"targetId"`
	TargetTitle string  `yaml:"targetTitle" json:"targetTitle"`
	Kind        string  `yaml:"kind" json:"kind"`
	Weight      float64 `yaml:"weight" json:"weight"`
	Confidence  float64 `yaml:"confidence" json:"confidence"`
	Note        string  `yaml:"note" json:"note,omitempty"`
}

// WikiClaim is one assertion the page makes, with how well it is supported.
type WikiClaim struct {
	ID         string  `yaml:"id" json:"id,omitempty"`
	Text       string  `yaml:"text" json:"text"`
	Status     string  `yaml:"status" json:"status,omitempty"`
	Confidence float64 `yaml:"confidence" json:"confidence,omitempty"`
}

// wikiFrontmatter is the YAML block at the head of a wiki page.
type wikiFrontmatter struct {
	ID              string      `yaml:"id"`
	PageType        string      `yaml:"pageType"`
	Title           string      `yaml:"title"`
	Aliases         []string    `yaml:"aliases"`
	PrivacyTier     string      `yaml:"privacyTier"`
	LastRefreshedAt string      `yaml:"lastRefreshedAt"`
	SourceIDs       []string    `yaml:"sourceIds"`
	Relationships   []WikiRel   `yaml:"relationships"`
	Claims          []WikiClaim `yaml:"claims"`
}

// WikiPage is one page as the console reports it.
type WikiPage struct {
	Path            string      `json:"path"`
	ID              string      `json:"id"`
	PageType        string      `json:"pageType"`
	Title           string      `json:"title"`
	Aliases         []string    `json:"aliases,omitempty"`
	PrivacyTier     string      `json:"privacyTier,omitempty"`
	LastRefreshedAt string      `json:"lastRefreshedAt,omitempty"`
	SourceIDs       []string    `json:"sourceIds,omitempty"`
	Relationships   []WikiRel   `json:"relationships,omitempty"`
	Claims          []WikiClaim `json:"claims,omitempty"`
	// ModifiedAt is the file's mtime, which is the only timing available for a
	// page whose frontmatter omits lastRefreshedAt.
	ModifiedAt string `json:"modifiedAt"`
	Size       int64  `json:"size"`
}

// WikiGraph is the synthesized layer plus the sources it draws on. Sources are
// returned separately so the UI can keep them out of the graph until a node is
// expanded — there are typically an order of magnitude more of them, and they
// would otherwise drown the pages that carry meaning.
type WikiGraph struct {
	Pages   []WikiPage          `json:"pages"`
	Sources map[string]WikiPage `json:"sources"`
	Edges   []WikiEdge          `json:"edges"`
	Counts  map[string]int      `json:"counts"`
}

// WikiEdge is one link in the graph. Kind "source" marks provenance, which the
// UI reveals on expansion; everything else is a declared relationship.
type WikiEdge struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Kind       string  `json:"kind"`
	Weight     float64 `json:"weight,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// parseWikiPage reads a page's frontmatter. A page without frontmatter is
// still returned — the index pages have none, and omitting them would make the
// browser lie about what is in the wiki.
func parseWikiPage(path string, body []byte, size, modTime int64) WikiPage {
	page := WikiPage{
		Path: path, Size: size,
		ModifiedAt: time.UnixMilli(modTime).UTC().Format(time.RFC3339Nano),
	}
	if fm, ok := splitFrontmatter(body); ok {
		var meta wikiFrontmatter
		if err := yaml.Unmarshal(fm, &meta); err == nil {
			page.ID, page.PageType, page.Title = meta.ID, meta.PageType, meta.Title
			page.Aliases, page.PrivacyTier = meta.Aliases, meta.PrivacyTier
			page.LastRefreshedAt = meta.LastRefreshedAt
			page.SourceIDs, page.Relationships, page.Claims = meta.SourceIDs, meta.Relationships, meta.Claims
		}
	}
	if page.Title == "" {
		page.Title = titleFromPath(path)
	}
	return page
}

// splitFrontmatter returns the YAML block delimited by leading and trailing
// "---" lines.
func splitFrontmatter(body []byte) ([]byte, bool) {
	text := string(body)
	if !strings.HasPrefix(text, "---\n") {
		return nil, false
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, false
	}
	return []byte(rest[:end]), true
}

func titleFromPath(p string) string {
	base := p
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".md")
	return strings.ReplaceAll(base, "-", " ")
}

// buildWikiGraph splits pages into the synthesized layer and its sources, and
// resolves declared relationships into edges. An edge to a page that does not
// exist is dropped rather than rendered as a dangling node.
func buildWikiGraph(pages []WikiPage) WikiGraph {
	graph := WikiGraph{
		Pages:   []WikiPage{},
		Sources: map[string]WikiPage{},
		Edges:   []WikiEdge{},
		Counts:  map[string]int{},
	}
	byID := map[string]WikiPage{}
	for _, p := range pages {
		if p.PageType != "" {
			graph.Counts[p.PageType]++
		}
		if p.ID != "" {
			byID[p.ID] = p
		}
	}

	for _, p := range pages {
		switch {
		case synthesizedTypes[p.PageType]:
			graph.Pages = append(graph.Pages, p)
		case p.PageType == "source" && p.ID != "":
			graph.Sources[p.ID] = p
		}
	}

	known := map[string]bool{}
	for _, p := range graph.Pages {
		known[p.ID] = true
	}
	for _, p := range graph.Pages {
		for _, rel := range p.Relationships {
			// Declared relationships between synthesized pages are the graph
			// proper; a target that is a source is provenance, handled below.
			if !known[rel.TargetID] {
				continue
			}
			graph.Edges = append(graph.Edges, WikiEdge{
				From: p.ID, To: rel.TargetID, Kind: rel.Kind,
				Weight: rel.Weight, Confidence: rel.Confidence,
			})
		}
		for _, sid := range p.SourceIDs {
			if _, ok := graph.Sources[sid]; !ok {
				continue
			}
			graph.Edges = append(graph.Edges, WikiEdge{From: p.ID, To: sid, Kind: "source"})
		}
	}
	return graph
}
