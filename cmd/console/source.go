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

// Where session bytes come from. The store is written against this interface
// so the transport can change without touching parsing or the API: today it
// is pod exec (and a local directory for development), and an OpenClaw
// gateway that served its own trajectories would slot in as a third
// implementation.

package main

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// maxSessionFileBytes caps any single session file pulled in a batch, so one
// pathological file cannot exhaust the console.
const maxSessionFileBytes = 256 << 20

// maxNoteBytes caps one memory note; notes are prose, not logs.
const maxNoteBytes = 4 << 20

// safeNotePathRE guards paths interpolated into a tar argument. Notes live in
// nested directories, so slashes are allowed but traversal is not.
var safeNotePathRE = regexp.MustCompile(`^[\w.-]+(?:/[\w.-]+)*$`)

// parseNoteIndex turns absolute find output into home-relative notes.
func parseNoteIndex(out, home string) []memoryNote {
	var notes []memoryNote
	prefix := strings.TrimSuffix(home, "/") + "/"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		rel := strings.TrimPrefix(parts[0], prefix)
		if rel == parts[0] || strings.Contains(rel, "..") {
			continue
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		secs, frac, _ := strings.Cut(parts[2], ".")
		sec, err := strconv.ParseInt(secs, 10, 64)
		if err != nil {
			continue
		}
		ms := sec * 1000
		if len(frac) >= 3 {
			if f, err := strconv.ParseInt(frac[:3], 10, 64); err == nil {
				ms += f
			}
		}
		notes = append(notes, memoryNote{Path: rel, Size: size, ModTime: ms})
	}
	return notes
}

// sessionFile is one file in an agent's sessions directory. Size and ModTime
// let the store skip re-reading append-only files it already parsed, which
// matters when every read is a round trip into a pod.
type sessionFile struct {
	Agent   string
	Name    string
	Size    int64
	ModTime int64 // unix millis
}

// sessionSource lists and reads one Claw's agent session files.
type sessionSource interface {
	// index lists the Claw's agents and every session file this console can
	// read. Agents are listed separately from their files because a Claw may
	// run an agent backend whose on-disk layout this console does not parse —
	// the agent still exists, and saying so beats implying it does not.
	index(ctx context.Context) (agents []string, files []sessionFile, err error)
	// read returns one session file's bytes.
	read(ctx context.Context, agent, name string) ([]byte, error)
	// readMany fetches several files at once. Sources where a read is a
	// network round trip implement this as a single request; the cost of a
	// scan is dominated by round trips, not bytes. Files that cannot be read
	// are omitted rather than failing the batch.
	readMany(ctx context.Context, files []sessionFile) (map[string][]byte, error)
	// memoryNotes lists the Claw's durable memory notes with their sizes and
	// modification times. These are read from disk rather than inferred from
	// tool calls: OpenClaw's consolidation and wiki synthesis write notes
	// directly, so a tool-call-derived feed sees only a fraction of them.
	memoryNotes(ctx context.Context) ([]memoryNote, error)
	// readNotes fetches note contents by their index paths.
	readNotes(ctx context.Context, notes []memoryNote) (map[string][]byte, error)
	// describe names the source for logs and error messages.
	describe() string
}

// memoryNote is one durable note on disk. Path is relative to the Claw home,
// e.g. "workspace/memory/2026-07-26.md" or "stitch/memory/dreaming/deep/x.md".
type memoryNote struct {
	Path    string
	Size    int64
	ModTime int64 // unix millis
}

// batchKey identifies a file within a readMany result.
func batchKey(agent, name string) string { return agent + "/" + name }

/* ------------------------------------------------------------ filesystem */

// dirSource reads a directory laid out the way OpenClaw writes one. Used by
// `make console-run-local` and by the tests, so the parsing path can be
// exercised without a cluster.
type dirSource struct{ root string }

func (d dirSource) describe() string { return d.root }

func (d dirSource) index(_ context.Context) ([]string, []sessionFile, error) {
	dirs, err := os.ReadDir(d.root)
	if err != nil {
		return nil, nil, err
	}
	var agents []string
	var out []sessionFile
	for _, a := range dirs {
		if !a.IsDir() {
			continue
		}
		agents = append(agents, a.Name())
		entries, err := os.ReadDir(filepath.Join(d.root, a.Name(), "sessions"))
		if err != nil {
			continue // an agent directory without sessions/ is not an error
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, sessionFile{
				Agent: a.Name(), Name: e.Name(),
				Size: info.Size(), ModTime: info.ModTime().UnixMilli(),
			})
		}
	}
	return agents, out, nil
}

// readMany on a local directory is just repeated reads: there is no round
// trip to amortize.
func (d dirSource) readMany(ctx context.Context, files []sessionFile) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, f := range files {
		if body, err := d.read(ctx, f.Agent, f.Name); err == nil {
			out[batchKey(f.Agent, f.Name)] = body
		}
	}
	return out, nil
}

// memoryNotes on a local directory looks beside the agents dir, matching the
// layout in a Claw pod.
func (d dirSource) memoryNotes(_ context.Context) ([]memoryNote, error) {
	home := filepath.Dir(strings.TrimSuffix(d.root, string(filepath.Separator)))
	var out []memoryNote
	for _, sub := range []string{"workspace/memory", "workspace/wiki"} {
		out = append(out, walkNotes(home, sub)...)
	}
	if agents, err := os.ReadDir(d.root); err == nil {
		for _, a := range agents {
			if a.IsDir() {
				out = append(out, walkNotes(home, filepath.Base(d.root)+"/"+a.Name()+"/memory")...)
			}
		}
	}
	return out, nil
}

func walkNotes(home, sub string) []memoryNote {
	var out []memoryNote
	root := filepath.Join(home, sub)
	_ = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(home, p)
		if err != nil {
			return nil
		}
		out = append(out, memoryNote{Path: filepath.ToSlash(rel), Size: info.Size(), ModTime: info.ModTime().UnixMilli()})
		return nil
	})
	return out
}

func (d dirSource) readNotes(_ context.Context, notes []memoryNote) (map[string][]byte, error) {
	home := filepath.Dir(strings.TrimSuffix(d.root, string(filepath.Separator)))
	out := map[string][]byte{}
	for _, n := range notes {
		if !safeNotePathRE.MatchString(n.Path) {
			continue
		}
		if body, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(n.Path))); err == nil {
			out[n.Path] = body
		}
	}
	return out, nil
}

func (d dirSource) read(_ context.Context, agent, name string) ([]byte, error) {
	if !safeNameRE.MatchString(agent) || !safeNameRE.MatchString(name) {
		return nil, os.ErrNotExist
	}
	file := filepath.Join(d.root, agent, "sessions", name)
	root, err := filepath.Abs(d.root)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(file)
	if err != nil || !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(file)
}

/* ------------------------------------------------------------------ exec */

// execSource reads a Claw's session files by running read-only commands in
// its pod. Every call carries the logged-in user's identity so the API server
// enforces access.
type execSource struct {
	srv       *server
	identity  userIdentity
	namespace string
	pod       string
	container string
	agentsDir string
}

func (e execSource) describe() string {
	return e.namespace + "/" + e.pod + ":" + e.agentsDir
}

// index runs one `find` per refresh rather than one stat per file: the round
// trip, not the bytes, is what costs here.
func (e execSource) index(ctx context.Context) ([]string, []sessionFile, error) {
	dir := shellQuote(e.agentsDir)
	// Two labelled sections in one round trip: the agent directories, then the
	// session files this console knows how to read. %P is the path relative to
	// the search root, giving "<agent>/sessions/<file>".
	script := "find " + dir + " -mindepth 1 -maxdepth 1 -type d -printf 'A\\t%P\\n' 2>/dev/null; " +
		"find " + dir + " -mindepth 3 -maxdepth 3 -path '*/sessions/*' -type f -name '*.jsonl' " +
		"-printf 'F\\t%P\\t%s\\t%T@\\n' 2>/dev/null; true"

	res, err := e.srv.execInPod(ctx, e.identity, e.namespace, e.pod, e.container,
		[]string{"sh", "-c", script})
	if err != nil {
		return nil, nil, err
	}
	agents, files := parseIndexOutput(string(res.Stdout))
	return agents, files, nil
}

// readMany streams one tar containing every requested file, turning what
// would be N round trips into one. This is the difference between a scan that
// takes a minute and one that takes a few seconds.
func (e execSource) readMany(ctx context.Context, files []sessionFile) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(files) == 0 {
		return out, nil
	}

	// Paths are relative to the agents dir so the tar entry names come back as
	// "<agent>/sessions/<file>", which maps straight onto the batch key.
	args := []string{"tar", "cf", "-", "-C", e.agentsDir}
	wanted := map[string]bool{}
	for _, f := range files {
		if !safeNameRE.MatchString(f.Agent) || !safeNameRE.MatchString(f.Name) {
			continue
		}
		rel := f.Agent + "/sessions/" + f.Name
		args = append(args, rel)
		wanted[rel] = true
	}
	if len(wanted) == 0 {
		return out, nil
	}

	err := e.srv.execStream(ctx, e.identity, e.namespace, e.pod, e.container, args,
		func(r io.Reader) error {
			tr := tar.NewReader(r)
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					// A partial tar still yields the entries already read;
					// the store treats missing files as unreadable and says so.
					return nil
				}
				if hdr.Typeflag != tar.TypeReg || !wanted[hdr.Name] {
					continue
				}
				body, err := io.ReadAll(io.LimitReader(tr, maxSessionFileBytes))
				if err != nil {
					continue
				}
				agent, rest, ok := strings.Cut(hdr.Name, "/sessions/")
				if !ok {
					continue
				}
				out[batchKey(agent, rest)] = body
			}
		})
	if err != nil {
		return out, err
	}
	return out, nil
}

// memoryNotes lists notes across all three of OpenClaw's stores in one round
// trip: the shared workspace memory, the wiki, and each agent's own
// consolidation output.
func (e execSource) memoryNotes(ctx context.Context) ([]memoryNote, error) {
	home := shellQuote(e.clawHome())
	script := "find " + home + "/workspace/memory " + home + "/workspace/wiki " +
		home + "/*/memory -name '*.md' -type f -printf '%p\t%s\t%T@\n' 2>/dev/null; true"

	res, err := e.srv.execInPod(ctx, e.identity, e.namespace, e.pod, e.container,
		[]string{"sh", "-c", script})
	if err != nil {
		return nil, err
	}
	return parseNoteIndex(string(res.Stdout), e.clawHome()), nil
}

func (e execSource) readNotes(ctx context.Context, notes []memoryNote) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(notes) == 0 {
		return out, nil
	}
	args := []string{"tar", "cf", "-", "-C", e.clawHome()}
	wanted := map[string]bool{}
	for _, n := range notes {
		if !safeNotePathRE.MatchString(n.Path) {
			continue
		}
		args = append(args, n.Path)
		wanted[n.Path] = true
	}
	if len(wanted) == 0 {
		return out, nil
	}
	err := e.srv.execStream(ctx, e.identity, e.namespace, e.pod, e.container, args,
		func(r io.Reader) error {
			tr := tar.NewReader(r)
			for {
				hdr, err := tr.Next()
				if err != nil {
					return nil
				}
				if hdr.Typeflag != tar.TypeReg || !wanted[hdr.Name] {
					continue
				}
				body, err := io.ReadAll(io.LimitReader(tr, maxNoteBytes))
				if err != nil {
					continue
				}
				out[hdr.Name] = body
			}
		})
	return out, err
}

// clawHome is the OpenClaw home directory: the agents dir's parent.
func (e execSource) clawHome() string {
	return strings.TrimSuffix(path.Dir(e.agentsDir), "/")
}

func (e execSource) read(ctx context.Context, agent, name string) ([]byte, error) {
	if !safeNameRE.MatchString(agent) || !safeNameRE.MatchString(name) {
		return nil, os.ErrNotExist
	}
	path := e.agentsDir + "/" + agent + "/sessions/" + name
	res, err := e.srv.execInPod(ctx, e.identity, e.namespace, e.pod, e.container,
		[]string{"cat", path})
	if err != nil {
		return nil, err
	}
	return res.Stdout, nil
}

// parseIndexOutput reads the labelled find output, skipping any row it cannot
// make sense of rather than failing the whole index.
func parseIndexOutput(out string) ([]string, []sessionFile) {
	var agents []string
	var files []sessionFile
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) == 2 && parts[0] == "A" {
			if parts[1] != "" {
				agents = append(agents, parts[1])
			}
			continue
		}
		if len(parts) != 4 || parts[0] != "F" {
			continue
		}
		parts = parts[1:]
		rel := parts[0]
		agent, rest, ok := strings.Cut(rel, "/sessions/")
		if !ok || agent == "" || rest == "" || strings.Contains(rest, "/") {
			continue
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		// %T@ is seconds with a fractional part; milliseconds are enough.
		secs, frac, _ := strings.Cut(parts[2], ".")
		s, err := strconv.ParseInt(secs, 10, 64)
		if err != nil {
			continue
		}
		ms := s * 1000
		if len(frac) >= 3 {
			if f, err := strconv.ParseInt(frac[:3], 10, 64); err == nil {
				ms += f
			}
		}
		files = append(files, sessionFile{Agent: agent, Name: rest, Size: size, ModTime: ms})
	}
	return agents, files
}

// shellQuote makes a value safe to embed in the single `sh -c` string the
// index needs. Paths reaching here are operator-configured, not user input,
// but quoting keeps that from silently becoming load-bearing.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
