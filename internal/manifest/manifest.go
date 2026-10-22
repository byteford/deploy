// Package manifest supports multi-repo mode: instead of discovering
// deployable directories by walking services/config/infra inside one
// repo, a .deploy-repos file at the working directory's root lists
// named entries explicitly, each either a git repo to clone/sync or a
// local filesystem path to use as-is.
//
// Every other package (graph, tf, orchestrate, report, server) keeps
// using a plain directory-name string as a node's identity, completely
// unaware of any of this — Resolver is the one seam where that name
// gets turned into a real filesystem path, so single-repo mode (no
// .deploy-repos) and multi-repo mode share the same code everywhere
// else instead of branching throughout the codebase.
package manifest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FileName is the manifest itself, expected at the root of
// --working-dir. Its presence is what switches discovery into
// multi-repo mode — see graph.Discover.
const FileName = ".deploy-repos"

// CheckoutsDirName is where every GIT-managed entry gets cloned —
// <repoRoot>/CheckoutsDirName/<name> — instead of scattered directly
// at repoRoot's top level. One directory means one .gitignore line
// (`.deploy-repos-checkouts/`) covers every clone, rather than needing
// a per-repo entry or the orchestration repo's `git status` showing a
// pile of untracked directories after the first sync. Local-path
// entries are entirely unaffected — they already give you full control
// over where they live; this only applies to entries this tool itself
// clones.
const CheckoutsDirName = ".deploy-repos-checkouts"

// Entry is one line of the manifest, fully resolved: LocalPath is
// always absolute (a git entry's checkout path, or a local entry's
// given path resolved against repoRoot if it was relative).
type Entry struct {
	Name      string
	IsGit     bool
	URL       string // empty for a local-path entry
	Ref       string // optional; empty means the remote's default branch
	LocalPath string // always absolute
	TFRoot    string // optional subpath within LocalPath; "" means LocalPath itself
}

// WorkDir is where terraform actually runs for this entry — LocalPath
// plus TFRoot, if set.
func (e Entry) WorkDir() string {
	if e.TFRoot == "" {
		return e.LocalPath
	}
	return filepath.Join(e.LocalPath, e.TFRoot)
}

// Manifest indexes every entry by name.
type Manifest struct {
	Entries map[string]Entry
	Order   []string // file order, for stable iteration independent of map order
}

// Exists reports whether repoRoot is in multi-repo mode.
func Exists(repoRoot string) bool {
	_, err := os.Stat(filepath.Join(repoRoot, FileName))
	return err == nil
}

// scpLike matches SCP-style git remotes: user@host:path — e.g.
// git@github.com:acme/repo.git. Deliberately doesn't also match a
// Windows-style drive-letter local path (C:\...) since that colon isn't
// preceded by an "@host" portion.
var scpLike = regexp.MustCompile(`^[^@\s]+@[^:\s]+:\S+$`)

// looksLikeGitURL decides whether a manifest value is a git remote
// (clone/sync it) or a local filesystem path (use as-is, never touched
// by sync) — a scheme prefix (https://, http://, ssh://, git://,
// file://) or SCP-style user@host:path. Anything else, including a bare
// absolute or relative path, is local.
func looksLikeGitURL(v string) bool {
	if i := strings.Index(v, "://"); i > 0 && !strings.ContainsAny(v[:i], "/\\ ") {
		return true
	}
	return scpLike.MatchString(v)
}

// Load parses FileName at repoRoot. Returns (nil, nil) if the file
// doesn't exist — callers should check Exists first if they need to
// distinguish "not present" from "present but empty/erroring", but
// Load itself treats a missing file as "no manifest" rather than an
// error, matching how the .env loaders behave for a missing file.
func Load(repoRoot string) (*Manifest, error) {
	path := filepath.Join(repoRoot, FileName)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	m := &Manifest{Entries: map[string]Entry{}}
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		name, rest, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected name=url or name=path, got %q", path, lineNo, line)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("%s:%d: empty name in %q", path, lineNo, line)
		}
		if _, dup := m.Entries[name]; dup {
			return nil, fmt.Errorf("%s:%d: %q is defined more than once", path, lineNo, name)
		}

		// rest is "url[#ref] [tf-root-subpath]" or "path [tf-root-subpath]"
		// — whitespace separates an optional trailing tf-root field, since
		// git URLs and filesystem paths never contain literal spaces.
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return nil, fmt.Errorf("%s:%d: %q has no URL or path", path, lineNo, name)
		}
		location := fields[0]
		tfRoot := ""
		if len(fields) > 1 {
			tfRoot = filepath.Clean(fields[1])
		}
		if len(fields) > 2 {
			return nil, fmt.Errorf("%s:%d: too many fields after the URL/path in %q", path, lineNo, line)
		}

		entry := Entry{Name: name, TFRoot: tfRoot}
		if looksLikeGitURL(location) {
			url, ref, _ := strings.Cut(location, "#")
			entry.IsGit = true
			entry.URL = url
			entry.Ref = ref
			entry.LocalPath = filepath.Join(repoRoot, CheckoutsDirName, name)
		} else {
			if strings.Contains(location, "#") {
				return nil, fmt.Errorf("%s:%d: %q is a local path, not a git URL — a #ref only makes sense for a git URL", path, lineNo, name)
			}
			entry.LocalPath = location
			if !filepath.IsAbs(entry.LocalPath) {
				entry.LocalPath = filepath.Join(repoRoot, entry.LocalPath)
			}
		}

		m.Entries[name] = entry
		m.Order = append(m.Order, name)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return m, nil
}
