package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture builds a throwaway repo layout under t.TempDir():
// dirs is a map of "services/a" -> deps (contents of .deploy-deps, one
// per line; nil/empty means no deps file at all).
func writeFixture(t *testing.T, dirs map[string][]string) string {
	t.Helper()
	root := t.TempDir()
	for dir, deps := range dirs {
		full := filepath.Join(root, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if len(deps) > 0 {
			content := ""
			for _, d := range deps {
				content += d + "\n"
			}
			if err := os.WriteFile(filepath.Join(full, ".deploy-deps"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func TestDiscoverAndSort_LinearChain(t *testing.T) {
	root := writeFixture(t, map[string][]string{
		"config/vault": nil,
		"services/a":   {"config/vault"},
		"services/b":   {"services/a"},
		"services/c":   nil,
	})

	g, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(g.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(g.Nodes))
	}
	if err := g.Sort(); err != nil {
		t.Fatalf("Sort: %v", err)
	}

	pos := map[string]int{}
	for i, d := range g.Order {
		pos[d] = i
	}
	if pos["config/vault"] >= pos["services/a"] {
		t.Errorf("config/vault must come before services/a: order=%v", g.Order)
	}
	if pos["services/a"] >= pos["services/b"] {
		t.Errorf("services/a must come before services/b: order=%v", g.Order)
	}
	if len(g.Order) != 4 {
		t.Errorf("expected 4 entries in order, got %d: %v", len(g.Order), g.Order)
	}
}

func TestSort_DeterministicWithNoDeps(t *testing.T) {
	root := writeFixture(t, map[string][]string{
		"services/z": nil,
		"services/a": nil,
		"services/m": nil,
	})
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}
	want := []string{"services/a", "services/m", "services/z"}
	if len(g.Order) != len(want) {
		t.Fatalf("got %v, want %v", g.Order, want)
	}
	for i := range want {
		if g.Order[i] != want[i] {
			t.Errorf("order[%d] = %s, want %s (full: %v)", i, g.Order[i], want[i], g.Order)
		}
	}
}

func TestSort_CycleDetected(t *testing.T) {
	root := writeFixture(t, map[string][]string{
		"services/a": {"services/b"},
		"services/b": {"services/a"},
	})
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err == nil {
		t.Fatal("expected a cycle error, got nil")
	}
}

func TestSort_UnknownDependency(t *testing.T) {
	root := writeFixture(t, map[string][]string{
		"services/a": {"services/does-not-exist"},
	})
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err == nil {
		t.Fatal("expected an unknown-dependency error, got nil")
	}
}

func TestDiscover_MissingRootsAreOK(t *testing.T) {
	// A repo with only services/, no config/ or infra/, must not error —
	// the bash version's globs also silently produce nothing for an
	// absent root.
	root := writeFixture(t, map[string][]string{
		"services/a": nil,
	})
	g, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover should not fail when config/ or infra/ are absent: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(g.Nodes))
	}
}

// writeManifestFixture builds a .deploy-repos manifest at a fresh temp
// root plus whatever local target directories entries map specifies
// (name -> real subdirectory to create under a separate "targets" root,
// or "" to deliberately NOT create anything, for testing a broken
// reference). deps, if non-nil for a name, becomes that target's
// .deploy-deps content.
func writeManifestFixture(t *testing.T, entries map[string]string, deps map[string]string) (repoRoot string) {
	t.Helper()
	repoRoot = t.TempDir()
	targets := t.TempDir()

	var lines []string
	for name, subdir := range entries {
		if subdir == "" {
			// Deliberately broken: reference a path that's never created.
			lines = append(lines, name+"=/this/path/does/not/exist/"+name)
			continue
		}
		full := filepath.Join(targets, subdir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if d, ok := deps[name]; ok {
			if err := os.WriteFile(filepath.Join(full, ".deploy-deps"), []byte(d+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		lines = append(lines, name+"="+full)
	}

	var content string
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".deploy-repos"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return repoRoot
}

func TestDiscover_LocalEntryWithMissingPathErrorsClearly(t *testing.T) {
	// Regression test: a local (non-git) manifest entry pointing at a
	// path that doesn't exist used to silently show up with zero
	// dependencies — indistinguishable from "this directory genuinely
	// has no deps" — instead of the clear config error it actually is.
	// The real-world case this came from: a leading "/" turning an
	// intended repo-relative path ("services/vault") into an absolute
	// filesystem path ("/services/vault") that doesn't exist anywhere.
	root := writeManifestFixture(t, map[string]string{"vault": ""}, nil)
	_, err := Discover(root)
	if err == nil {
		t.Fatal("expected an error for a local entry with a nonexistent path")
	}
	if !strings.Contains(err.Error(), "vault") || !strings.Contains(err.Error(), "leading") {
		t.Errorf("error should name the entry and hint at the leading-slash mistake, got: %v", err)
	}
}

func TestDiscover_LocalEntryWithValidPathWorksNormally(t *testing.T) {
	root := writeManifestFixture(t, map[string]string{"vault": "vault"}, nil)
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Nodes["vault"]; !ok {
		t.Fatal("expected a node named \"vault\"")
	}
}

func TestDiscover_LocalEntryPointingAtAFileNotDirectoryErrors(t *testing.T) {
	root := t.TempDir()
	targets := t.TempDir()
	filePath := filepath.Join(targets, "not-a-directory")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".deploy-repos"), []byte("vault="+filePath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Discover(root)
	if err == nil {
		t.Fatal("expected an error for a local entry pointing at a file, not a directory")
	}
}

func TestDiscover_GitEntryWithMissingPathDoesNotError(t *testing.T) {
	// The opposite of the local-entry case: a GIT entry's LocalPath
	// legitimately doesn't exist before `sync` has run — that's normal,
	// not a mistake, and must NOT be treated the same as a broken local
	// path. Discovery should still succeed (with zero deps for that
	// entry, since .deploy-deps genuinely can't be read yet).
	root := t.TempDir()
	content := "vault=git@github.com:acme/vault.git\n"
	if err := os.WriteFile(filepath.Join(root, ".deploy-repos"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Discover(root)
	if err != nil {
		t.Fatalf("a not-yet-synced git entry must not error, got: %v", err)
	}
	node, ok := g.Nodes["vault"]
	if !ok {
		t.Fatal("expected a node named \"vault\" even before syncing")
	}
	if len(node.DependsOn) != 0 {
		t.Errorf("expected no dependencies for a not-yet-synced entry, got %v", node.DependsOn)
	}
}

func TestDiscover_LocalEntryDependencyStillResolvesCorrectly(t *testing.T) {
	root := writeManifestFixture(t,
		map[string]string{"vault": "vault", "authentik": "authentik"},
		map[string]string{"authentik": "vault"},
	)
	g, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sort(); err != nil {
		t.Fatal(err)
	}
	authentik, ok := g.Nodes["authentik"]
	if !ok || len(authentik.DependsOn) != 1 || authentik.DependsOn[0] != "vault" {
		t.Fatalf("expected authentik to depend on vault, got %+v (ok=%v)", authentik, ok)
	}
}
