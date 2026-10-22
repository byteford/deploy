package manifest

import (
	"fmt"
	"os"
	"path/filepath"
)

// Resolver is the one place a directory/node NAME turns into a real
// filesystem path. graph, tf, orchestrate, and report all take one and
// use it instead of building paths themselves — in single-repo mode
// (m == nil, no .deploy-repos found) both methods just fall back to
// today's filepath.Join(repoRoot, name), so nothing about existing
// single-repo behavior changes.
type Resolver struct {
	m *Manifest // nil in single-repo mode
}

// NewResolver loads the manifest at repoRoot if present, or returns a
// Resolver that behaves as single-repo mode if not.
func NewResolver(repoRoot string) (*Resolver, error) {
	m, err := Load(repoRoot)
	if err != nil {
		return nil, err
	}
	return &Resolver{m: m}, nil
}

// Manifest returns the loaded manifest, or nil in single-repo mode —
// for callers (graph.Discover, the sync command) that need the full
// entry list, not just path resolution.
func (r *Resolver) Manifest() *Manifest {
	return r.m
}

// Dir resolves name to its checkout/orchestration-metadata directory —
// where .deploy-deps, .manual-check, and .manual-before live. This is
// the repo's own root even when its actual terraform root is nested
// elsewhere (see WorkDir) — those files describe the repo as a
// deployable unit, not its Terraform configuration specifically.
func (r *Resolver) Dir(repoRoot, name string) string {
	if r.m != nil {
		if e, ok := r.m.Entries[name]; ok {
			return e.LocalPath
		}
	}
	return filepath.Join(repoRoot, name)
}

// WorkDir resolves name to where terraform should actually be invoked —
// Dir, plus the entry's TFRoot subpath if the manifest sets one. This
// is what tf.Runner uses; WriteBackendStub also needs this specifically
// (not Dir), since the backend stub is a real .tf file terraform must
// find in its actual working directory, not just a repo-level marker.
func (r *Resolver) WorkDir(repoRoot, name string) string {
	if r.m != nil {
		if e, ok := r.m.Entries[name]; ok {
			return e.WorkDir()
		}
	}
	return filepath.Join(repoRoot, name)
}

// NotSyncedHint returns a human-readable explanation if name is a
// git-managed manifest entry whose local checkout doesn't exist yet —
// i.e. "you forgot to run sync" — or "" if that's not the situation
// (single-repo mode, a local-path entry, or the checkout already
// exists). Callers use this to surface a clear, specific message
// instead of letting a raw "no such file or directory" from deep
// inside terraform-exec be the only signal something's missing.
func (r *Resolver) NotSyncedHint(repoRoot, name string) string {
	if r.m == nil {
		return ""
	}
	e, ok := r.m.Entries[name]
	if !ok || !e.IsGit {
		return ""
	}
	if _, err := os.Stat(e.LocalPath); err == nil {
		return ""
	}
	return fmt.Sprintf("%q hasn't been synced yet — run \"homelab-deploy sync\" first", name)
}
