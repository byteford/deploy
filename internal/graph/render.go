package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"homelab-deploy/internal/manifest"
)

// LayoutNode is one directory positioned for a dependency-graph
// visualization: Depth is its column (0 = no dependencies; otherwise one
// more than the deepest dependency), so drawing left-to-right by Depth
// naturally shows apply order.
type LayoutNode struct {
	Dir        string
	DependsOn  []string
	Depth      int
	ManualStep bool // has a .manual-before file
	NotSynced  bool // a git-managed multi-repo entry that hasn't been cloned yet
}

// Layout computes a LayoutNode for every directory in g, in g.Order
// (must already be sorted — call g.Sort() first). resolver may be nil
// (single-repo mode); repoRoot+resolver are only needed to check for
// .manual-before, same as RenderText/RenderDot.
func Layout(g *Graph, resolver *manifest.Resolver, repoRoot string) []LayoutNode {
	depthOf := map[string]int{}
	var depth func(dir string) int
	depth = func(dir string) int {
		if d, ok := depthOf[dir]; ok {
			return d
		}
		max := -1
		for _, dep := range g.Nodes[dir].DependsOn {
			if d := depth(dep); d > max {
				max = d
			}
		}
		d := max + 1
		depthOf[dir] = d
		return d
	}

	nodes := make([]LayoutNode, 0, len(g.Order))
	for _, dir := range g.Order {
		_, manualErr := os.Stat(filepath.Join(resolveDir(resolver, repoRoot, dir), ".manual-before"))
		nodes = append(nodes, LayoutNode{
			Dir:        dir,
			DependsOn:  g.Nodes[dir].DependsOn,
			Depth:      depth(dir),
			ManualStep: manualErr == nil,
			NotSynced:  notSynced(resolver, repoRoot, dir),
		})
	}
	return nodes
}

// notSynced reports whether dir is a git-managed multi-repo entry that
// hasn't been cloned yet — nil resolver (single-repo mode) or a
// local-path/already-cloned entry always returns false.
func notSynced(resolver *manifest.Resolver, repoRoot, dir string) bool {
	if resolver == nil {
		return false
	}
	return resolver.NotSyncedHint(repoRoot, dir) != ""
}

// RenderText prints the apply order, one line per directory, with its
// dependencies and whether it needs a manual step first — same shape as
// the bash script's `graph` (no arg) output.
func RenderText(g *Graph, resolver *manifest.Resolver, repoRoot string) string {
	var b strings.Builder
	b.WriteString("Apply order:\n")
	for i, dir := range g.Order {
		node := g.Nodes[dir]
		marker := ""
		if _, err := os.Stat(filepath.Join(resolveDir(resolver, repoRoot, dir), ".manual-before")); err == nil {
			marker = " (manual step required first)"
		}
		if notSynced(resolver, repoRoot, dir) {
			marker += " (not synced — run `homelab-deploy sync`)"
		}
		depsDisplay := ""
		if len(node.DependsOn) > 0 {
			depsDisplay = fmt.Sprintf(" [depends on: %s]", strings.Join(node.DependsOn, ", "))
		}
		fmt.Fprintf(&b, "  %2d. %s%s%s\n", i+1, dir, depsDisplay, marker)
	}
	return b.String()
}

// RenderDot emits Graphviz DOT, pipeable to `dot -Tpng`.
func RenderDot(g *Graph, resolver *manifest.Resolver, repoRoot string) string {
	var b strings.Builder
	b.WriteString("digraph deploy {\n  rankdir=LR;\n  node [shape=box, style=filled, fillcolor=lightgray];\n")
	for _, dir := range g.Order {
		attrs := ""
		if _, err := os.Stat(filepath.Join(resolveDir(resolver, repoRoot, dir), ".manual-before")); err == nil {
			attrs = ", fillcolor=lightyellow, peripheries=2"
		}
		if notSynced(resolver, repoRoot, dir) {
			attrs += `, fillcolor=lightcoral, style="filled,dashed"`
		}
		fmt.Fprintf(&b, "  %q [label=%q%s];\n", dir, dir, attrs)
	}
	for _, dir := range g.Order {
		for _, dep := range g.Nodes[dir].DependsOn {
			fmt.Fprintf(&b, "  %q -> %q;\n", dep, dir)
		}
	}
	b.WriteString("}\n")
	return b.String()
}
