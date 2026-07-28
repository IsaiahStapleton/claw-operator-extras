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
// Edges come from two places. OpenClaw's pages declare structure in YAML
// frontmatter — typed relationships with weight and confidence, and provenance
// sourceIds — which is richer than a link, but there are very few of them. The
// bulk of the structure is ordinary markdown links in the page bodies, which is
// what Obsidian draws its graph from. Reading only the frontmatter produced a
// field of disconnected dots that did not match the same vault in Obsidian, so
// the graph reads both.

package main

import (
	"path"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Links in the body are what Obsidian draws its graph from, and they vastly
// outnumber the frontmatter relationships — 395 markdown links against 2
// declared relations in a real wiki. Building the graph from frontmatter alone
// produced a field of disconnected dots that did not match what the same vault
// looks like in Obsidian.
var (
	mdLinkRE   = regexp.MustCompile(`\]\(([^)\s]+\.md)(?:#[^)]*)?\)`)
	wikiLinkRE = regexp.MustCompile(`\[\[([^\]|#]+)(?:[|#][^\]]*)?\]\]`)
)

// extractLinks resolves every outgoing link in a page body to a wiki-relative
// path, so an edge can be drawn to whichever page sits there.
func extractLinks(pagePath string, body []byte) []string {
	dir := path.Dir(pagePath)
	seen := map[string]bool{}
	var out []string
	add := func(target string) {
		target = strings.TrimSpace(target)
		if target == "" || strings.Contains(target, "://") {
			return
		}
		if !strings.HasSuffix(target, ".md") {
			target += ".md"
		}
		resolved := target
		if !strings.HasPrefix(target, "/") {
			resolved = path.Join(dir, target)
		}
		resolved = strings.TrimPrefix(path.Clean(resolved), "/")
		if resolved == pagePath || seen[resolved] {
			return
		}
		seen[resolved] = true
		out = append(out, resolved)
	}
	text := string(body)
	for _, m := range mdLinkRE.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range wikiLinkRE.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	return out
}

// Page types that make up the synthesized layer — what the wiki has actually
// concluded, as opposed to the raw memory it imported.
//
// Reports are excluded deliberately. They are generated dashboards over the
// wiki (lint results, claim health, stale pages) rather than knowledge, and
// they link to nothing, so every one of them rendered as an isolated dot.
var synthesizedTypes = map[string]bool{
	"concept": true, "entity": true, "synthesis": true,
}

// Vault plumbing: instructions to the agent and documentation of the wiki
// itself. Real files, but not things the wiki knows.
var plumbingPages = map[string]bool{
	"AGENTS.md": true, "WIKI.md": true, "inbox.md": true,
}

// Directories whose index page is a table of contents for one page type.
// Those indexes say only "these pages are concepts", which the graph already
// says with colour, so drawing them adds a hub that carries no information.
// The wiki's root index is a different thing and is kept: it links across
// types, and is what ties otherwise separate clusters together.
var sectionDirs = map[string]bool{
	"concepts": true, "entities": true, "syntheses": true,
	"sources": true, "reports": true, "claims": true,
}

// graphExcluded reports whether a page is wiki machinery rather than content.
//
// The quarantine directory is the one that matters: OpenClaw parks superseded
// copies under .openclaw-wiki/quarantine/ when it retypes a page, so a real
// vault contains both entities/stitch.md and a quarantined stitch.synthesis.md
// carrying the same title. Both became nodes, and the quarantined one had no
// edges — which is exactly what a duplicate orphan looks like on screen.
func graphExcluded(p WikiPage) bool {
	if p.PageType == "report" {
		return true
	}
	for _, seg := range strings.Split(p.Path, "/") {
		// .openclaw-wiki holds the quarantine and the machine cache.
		if strings.HasPrefix(seg, ".") {
			return true
		}
		// reports/index.md carries no pageType, so it needs the path check.
		if seg == "reports" {
			return true
		}
	}
	base := path.Base(p.Path)
	if base == "index.md" && sectionDirs[path.Base(path.Dir(p.Path))] {
		return true
	}
	return plumbingPages[base]
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
	Path string `json:"path"`
	// Key is this page's identity in the graph, and the only thing edges are
	// expressed in terms of. It exists because a page's id is optional — the
	// generated index pages carry no frontmatter at all — so a consumer that
	// keyed nodes by id alone collapsed every index page onto the empty string
	// and silently dropped each edge that referenced one.
	Key             string      `json:"key"`
	ID              string      `json:"id"`
	PageType        string      `json:"pageType"`
	Title           string      `json:"title"`
	Aliases         []string    `json:"aliases,omitempty"`
	PrivacyTier     string      `json:"privacyTier,omitempty"`
	LastRefreshedAt string      `json:"lastRefreshedAt,omitempty"`
	SourceIDs       []string    `json:"sourceIds,omitempty"`
	Relationships   []WikiRel   `json:"relationships,omitempty"`
	Claims          []WikiClaim `json:"claims,omitempty"`
	// Links are outgoing body links resolved to wiki-relative paths. These
	// carry the bulk of the graph's structure.
	Links []string `json:"links,omitempty"`
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
	page.Links = extractLinks(path, body)
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

// titleFromPath names a page that carries no title of its own. Generated index
// pages are all called index.md, so naming them by their own filename produced
// five nodes labelled "index"; they take the name of the section they index
// instead.
func titleFromPath(p string) string {
	base := strings.TrimSuffix(path.Base(p), ".md")
	if base == "index" {
		switch dir := path.Base(path.Dir(p)); dir {
		case "main", "wiki", ".", "/":
			return "Wiki index"
		default:
			base = dir
		}
	}
	return titleCase(strings.ReplaceAll(base, "-", " "))
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// nodeKey is a page's identity in the graph: its id when it declares one, its
// path otherwise, so a page without frontmatter can still take part.
func nodeKey(p WikiPage) string {
	if p.ID != "" {
		return p.ID
	}
	return p.Path
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
	// Machinery is dropped before anything else looks at it, so an excluded
	// page cannot become a node, an edge target, or a count.
	// Identity is stamped on every page here, once, so that nodes and edges can
	// never be keyed by two different rules.
	kept := make([]WikiPage, 0, len(pages))
	for _, p := range pages {
		if graphExcluded(p) {
			continue
		}
		p.Key = nodeKey(p)
		kept = append(kept, p)
	}
	pages = kept

	for _, p := range pages {
		if p.PageType != "" {
			graph.Counts[p.PageType]++
		}
	}

	for _, p := range pages {
		switch {
		case synthesizedTypes[p.PageType]:
			graph.Pages = append(graph.Pages, p)
		case p.PageType == "source" && p.ID != "":
			graph.Sources[p.ID] = p
		case p.PageType == "":
			// Generated index pages have no frontmatter but hold most of the
			// wiki's link structure; excluding them is what made the graph
			// look disconnected.
			p.PageType = "index"
			graph.Counts["index"]++
			graph.Pages = append(graph.Pages, p)
		}
	}

	known := map[string]bool{}
	for _, p := range graph.Pages {
		known[p.ID] = true
	}
	// Body links resolve by path, so index the whole wiki that way too — an
	// index page has no frontmatter id but is still a legitimate link target.
	byPath := map[string]WikiPage{}
	for _, p := range pages {
		byPath[p.Path] = p
	}
	seen := map[string]bool{}
	addEdge := func(from, to, kind string, weight, confidence float64) {
		if from == "" || to == "" || from == to {
			return
		}
		k := from + "\x00" + to + "\x00" + kind
		if seen[k] {
			return
		}
		seen[k] = true
		graph.Edges = append(graph.Edges, WikiEdge{
			From: from, To: to, Kind: kind, Weight: weight, Confidence: confidence,
		})
	}

	for _, p := range pages {
		from := p.Key
		if synthesizedTypes[p.PageType] {
			for _, rel := range p.Relationships {
				if known[rel.TargetID] {
					addEdge(from, rel.TargetID, rel.Kind, rel.Weight, rel.Confidence)
				}
			}
			for _, sid := range p.SourceIDs {
				if _, ok := graph.Sources[sid]; ok {
					addEdge(from, sid, "source", 0, 0)
				}
			}
		}
		// Body links, which is what an Obsidian graph of this vault shows.
		for _, target := range p.Links {
			tp, ok := byPath[target]
			if !ok {
				continue
			}
			addEdge(from, tp.Key, "link", 0, 0)
		}
	}
	return graph
}
