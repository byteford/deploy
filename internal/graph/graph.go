// Package graph discovers deployable directories — either by walking
// services/*, config/*, infra/* inside one repo (single-repo mode), or
// from a .deploy-repos manifest (multi-repo mode, internal/manifest) —
// and topologically sorts them by their .deploy-deps files.
package graph

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"homelab-deploy/internal/manifest"
)

// Node is one deployable directory and the directories it depends on.
type Node struct {
	Dir       string   // e.g. "services/palworld", relative to repo root
	DependsOn []string // other Dir values, must all exist in the graph
}

// Graph is the full dependency graph plus a few precomputed conveniences.
type Graph struct {
	Nodes map[string]*Node
	Order []string // topological order, filled by Sort
}

// roots mirrors the bash script's `services/*/ config/*/ infra/*/` globs
// — used in single-repo mode.
var roots = []string{"services", "config", "infra"}

// Discover builds a Graph either by walking the standard root
// directories (single-repo mode), or from a .deploy-repos manifest's
// entries if one exists at repoRoot (multi-repo mode — see
// internal/manifest). Either way, each node's DependsOn comes from a
// .deploy-deps file in that node's own directory: repoRoot/name in
// single-repo mode, or the manifest entry's own LocalPath in multi-repo
// mode, which may be a custom path entirely unrelated to repoRoot.
func Discover(repoRoot string) (*Graph, error) {
	if manifest.Exists(repoRoot) {
		return discoverFromManifest(repoRoot)
	}
	return discoverFromDirs(repoRoot)
}

func discoverFromDirs(repoRoot string) (*Graph, error) {
	g := &Graph{Nodes: map[string]*Node{}}

	for _, root := range roots {
		entries, err := os.ReadDir(filepath.Join(repoRoot, root))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", root, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.ToSlash(filepath.Join(root, e.Name()))
			deps, err := readDeps(filepath.Join(repoRoot, dir, ".deploy-deps"))
			if err != nil {
				return nil, err
			}
			g.Nodes[dir] = &Node{Dir: dir, DependsOn: deps}
		}
	}
	return g, nil
}

func discoverFromManifest(repoRoot string) (*Graph, error) {
	m, err := manifest.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	g := &Graph{Nodes: map[string]*Node{}}
	for _, name := range m.Order {
		e := m.Entries[name]

		// A not-yet-synced GIT entry's LocalPath legitimately doesn't
		// exist yet — that's expected before `sync` has run, and
		// readDeps treats a missing .deploy-deps as "no dependencies"
		// rather than an error precisely so discovery still works then
		// (e.g. for `graph`/`sync` themselves). A LOCAL entry has no
		// such excuse: it's meant to already exist, always, so a
		// missing path is almost certainly a real mistake — most often
		// a leading "/" turning an intended repo-relative path
		// ("services/vault") into an absolute filesystem path
		// ("/services/vault") that doesn't exist anywhere. Left
		// unchecked, this fails exactly like "no dependencies declared"
		// would — silently, uniformly across every entry, with nothing
		// to point at what's actually wrong. Fail loudly instead.
		if !e.IsGit {
			if info, statErr := os.Stat(e.LocalPath); statErr != nil || !info.IsDir() {
				return nil, fmt.Errorf(
					"%s: local entry %q points at %q, which doesn't exist — "+
						"if you meant a path relative to %s, remove the leading \"/\" "+
						"(a leading \"/\" makes it absolute from the filesystem root, not relative to anything)",
					manifest.FileName, name, e.LocalPath, repoRoot)
			}
		}

		deps, err := readDeps(filepath.Join(e.LocalPath, ".deploy-deps"))
		if err != nil {
			return nil, err
		}
		g.Nodes[name] = &Node{Dir: name, DependsOn: deps}
	}
	return g, nil
}

func readDeps(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()

	var deps []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		deps = append(deps, filepath.ToSlash(line))
	}
	return deps, sc.Err()
}

// Sort computes a topological order over the graph, erroring on cycles or
// dependencies pointing at directories that weren't discovered. Ties are
// broken alphabetically so output is stable across runs.
func (g *Graph) Sort() error {
	const (
		white = iota // unvisited
		gray         // in progress (on the current DFS stack)
		black        // done
	)
	color := map[string]int{}
	var order []string

	// Deterministic iteration order for reproducible output.
	names := make([]string, 0, len(g.Nodes))
	for name := range g.Nodes {
		names = append(names, name)
	}
	sort.Strings(names)

	var visit func(name string) error
	visit = func(name string) error {
		switch color[name] {
		case black:
			return nil
		case gray:
			return fmt.Errorf("circular dependency detected involving %s", name)
		}
		node, ok := g.Nodes[name]
		if !ok {
			return fmt.Errorf("unknown directory referenced: %s", name)
		}
		color[name] = gray
		deps := append([]string(nil), node.DependsOn...)
		sort.Strings(deps)
		for _, dep := range deps {
			if _, ok := g.Nodes[dep]; !ok {
				return fmt.Errorf("%s depends on unknown directory: %s", name, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[name] = black
		order = append(order, name)
		return nil
	}

	for _, name := range names {
		if err := visit(name); err != nil {
			return err
		}
	}
	g.Order = order
	return nil
}
