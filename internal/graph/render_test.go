package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homelab-deploy/internal/manifest"
)

func TestLayout_DepthAssignment(t *testing.T) {
	// vault (depth 0) <- a (depth 1) <- b (depth 2); c has no deps (depth 0)
	root := writeFixture(t, map[string][]string{
		"config/vault": nil,
		"services/a":   {"config/vault"},
		"services/b":   {"services/a"},
		"services/c":   nil,
	})
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}

	nodes := Layout(g, nil, root)
	depth := map[string]int{}
	for _, n := range nodes {
		depth[n.Dir] = n.Depth
	}

	if depth["config/vault"] != 0 {
		t.Errorf("config/vault depth = %d, want 0", depth["config/vault"])
	}
	if depth["services/a"] != 1 {
		t.Errorf("services/a depth = %d, want 1", depth["services/a"])
	}
	if depth["services/b"] != 2 {
		t.Errorf("services/b depth = %d, want 2", depth["services/b"])
	}
	if depth["services/c"] != 0 {
		t.Errorf("services/c depth = %d, want 0", depth["services/c"])
	}
}

func TestLayout_DepthTakesLongestPath(t *testing.T) {
	// a depends on both b (depth 0) and c (which depends on d, depth 1) —
	// a's depth must be 1 + the DEEPER dependency (c's), i.e. 2, not 1.
	root := writeFixture(t, map[string][]string{
		"services/d": nil,
		"services/c": {"services/d"},
		"services/b": nil,
		"services/a": {"services/b", "services/c"},
	})
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}

	nodes := Layout(g, nil, root)
	depth := map[string]int{}
	for _, n := range nodes {
		depth[n.Dir] = n.Depth
	}
	if depth["services/a"] != 2 {
		t.Errorf("services/a depth = %d, want 2 (must take the longer path through c->d)", depth["services/a"])
	}
}

func TestLayout_ManualStepDetected(t *testing.T) {
	root := writeFixture(t, map[string][]string{
		"services/a": nil,
		"services/b": nil,
	})
	if err := os.WriteFile(filepath.Join(root, "services/a", ".manual-before"), []byte("unseal vault"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}

	nodes := Layout(g, nil, root)
	manual := map[string]bool{}
	for _, n := range nodes {
		manual[n.Dir] = n.ManualStep
	}
	if !manual["services/a"] {
		t.Error("services/a should be flagged as needing a manual step")
	}
	if manual["services/b"] {
		t.Error("services/b should NOT be flagged as needing a manual step")
	}
}

func TestLayout_PreservesDependsOn(t *testing.T) {
	root := writeFixture(t, map[string][]string{
		"config/vault": nil,
		"services/a":   {"config/vault"},
	})
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}

	nodes := Layout(g, nil, root)
	for _, n := range nodes {
		if n.Dir == "services/a" {
			if len(n.DependsOn) != 1 || n.DependsOn[0] != "config/vault" {
				t.Errorf("services/a.DependsOn = %v, want [config/vault]", n.DependsOn)
			}
		}
	}
}

func TestRenderText_IncludesManualStepMarker(t *testing.T) {
	root := writeFixture(t, map[string][]string{"services/a": nil})
	if err := os.WriteFile(filepath.Join(root, "services/a", ".manual-before"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}

	out := RenderText(g, nil, root)
	if !containsAll(out, "services/a", "manual step required first") {
		t.Errorf("RenderText output missing expected content: %s", out)
	}
}

func TestRenderDot_ValidStructure(t *testing.T) {
	root := writeFixture(t, map[string][]string{
		"config/vault": nil,
		"services/a":   {"config/vault"},
	})
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}

	out := RenderDot(g, nil, root)
	if !containsAll(out, "digraph deploy", `"config/vault" -> "services/a"`) {
		t.Errorf("RenderDot output missing expected content: %s", out)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func TestRenderText_MarksNotSyncedGitEntry(t *testing.T) {
	root := t.TempDir()
	content := "vault=git@github.com:acme/vault.git\n"
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver, err := manifest.NewResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}

	out := RenderText(g, resolver, root)
	if !containsAll(out, "vault", "not synced", "homelab-deploy sync") {
		t.Errorf("RenderText should mark the not-yet-cloned entry, got:\n%s", out)
	}
}

func TestRenderText_NoMarkerOnceSynced(t *testing.T) {
	root := t.TempDir()
	content := "vault=git@github.com:acme/vault.git\n"
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, manifest.CheckoutsDirName, "vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolver, err := manifest.NewResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}

	out := RenderText(g, resolver, root)
	if strings.Contains(out, "not synced") {
		t.Errorf("should not mark an already-cloned entry as not synced, got:\n%s", out)
	}
}

func TestLayout_NotSyncedFlagSetCorrectly(t *testing.T) {
	root := t.TempDir()
	content := "vault=git@github.com:acme/vault.git\nlocal-thing=/opt/wherever\n"
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Only create local-thing's target, leave vault un-cloned. Note:
	// Discover itself would error on a missing LOCAL path (see
	// graph_test.go), so local-thing's target must exist for this test
	// to get past Discover at all.
	if err := os.MkdirAll("/opt/wherever", 0o755); err == nil {
		defer os.RemoveAll("/opt/wherever")
	} else {
		t.Skip("cannot create /opt/wherever in this environment")
	}

	resolver, err := manifest.NewResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}

	nodes := Layout(g, resolver, root)
	byDir := map[string]LayoutNode{}
	for _, n := range nodes {
		byDir[n.Dir] = n
	}
	if !byDir["vault"].NotSynced {
		t.Error("vault (git, not cloned) should have NotSynced = true")
	}
	if byDir["local-thing"].NotSynced {
		t.Error("local-thing (local path) should have NotSynced = false")
	}
}
