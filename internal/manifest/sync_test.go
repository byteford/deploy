package manifest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH — skipping")
	}
}

// newBareRemote creates a real local bare git repo with one commit
// (README.md) on its default branch, and returns its path — used as a
// "remote" URL for Sync, fully offline and hermetic: no network, no
// dependency on any real external repo's content or availability.
func newBareRemote(t *testing.T) string {
	t.Helper()
	requireGit(t)

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGitT(t, "", "init", "--quiet", "--bare", "--initial-branch=main", remote)

	work := t.TempDir()
	runGitT(t, work, "init", "--quiet", "--initial-branch=main")
	runGitT(t, work, "config", "user.email", "test@example.com")
	runGitT(t, work, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, work, "add", ".")
	runGitT(t, work, "commit", "--quiet", "-m", "initial")
	runGitT(t, work, "remote", "add", "origin", remote)
	runGitT(t, work, "push", "--quiet", "origin", "main")

	return remote
}

// runGitT runs git and fails the test on error — a thin wrapper for
// test setup/assertions, distinct from the package's own runGit (which
// returns errors for Sync to handle, not fail a test on).
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	fullArgs := args
	if dir != "" {
		fullArgs = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestSync_ClonesMissingRepo(t *testing.T) {
	remote := newBareRemote(t)
	repoRoot := t.TempDir()
	m := &Manifest{
		Entries: map[string]Entry{
			"services-a": {Name: "services-a", IsGit: true, URL: remote, LocalPath: filepath.Join(repoRoot, "services-a")},
		},
		Order: []string{"services-a"},
	}

	results := Sync(context.Background(), m, nil)
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Action != "cloned" {
		t.Errorf("Action = %q, want cloned", results[0].Action)
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, "services-a", "README.md"))
	if err != nil || string(data) != "v1\n" {
		t.Errorf("cloned content = %q, err %v", data, err)
	}
}

func TestSync_ClonesAtSpecificRef(t *testing.T) {
	remote := newBareRemote(t)
	// Add a tag and a second commit on main, so "clone at the tag"
	// genuinely differs from "clone at latest".
	work := t.TempDir()
	runGitT(t, "", "clone", "--quiet", remote, work)
	runGitT(t, work, "config", "user.email", "test@example.com")
	runGitT(t, work, "config", "user.name", "test")
	runGitT(t, work, "tag", "v1.0.0")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, work, "commit", "--quiet", "-am", "second commit")
	runGitT(t, work, "push", "--quiet", "origin", "main", "v1.0.0")

	repoRoot := t.TempDir()
	m := &Manifest{
		Entries: map[string]Entry{
			"services-a": {Name: "services-a", IsGit: true, URL: remote, Ref: "v1.0.0", LocalPath: filepath.Join(repoRoot, "services-a")},
		},
		Order: []string{"services-a"},
	}
	results := Sync(context.Background(), m, nil)
	if results[0].Err != nil {
		t.Fatal(results[0].Err)
	}
	data, _ := os.ReadFile(filepath.Join(repoRoot, "services-a", "README.md"))
	if string(data) != "v1\n" {
		t.Errorf("expected content pinned to the v1.0.0 tag (%q), got %q", "v1\n", data)
	}
}

func TestSync_UpdatesExistingCleanRepo(t *testing.T) {
	remote := newBareRemote(t)
	repoRoot := t.TempDir()
	entry := Entry{Name: "services-a", IsGit: true, URL: remote, LocalPath: filepath.Join(repoRoot, "services-a")}
	m := &Manifest{Entries: map[string]Entry{"services-a": entry}, Order: []string{"services-a"}}

	// Initial clone.
	if results := Sync(context.Background(), m, nil); results[0].Err != nil {
		t.Fatal(results[0].Err)
	}

	// Push a new commit to the remote from a separate clone.
	other := t.TempDir()
	runGitT(t, "", "clone", "--quiet", remote, other)
	runGitT(t, other, "config", "user.email", "test@example.com")
	runGitT(t, other, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, other, "commit", "--quiet", "-am", "update")
	runGitT(t, other, "push", "--quiet", "origin", "main")

	// Sync again — must pick up the new commit via fast-forward.
	results := Sync(context.Background(), m, nil)
	if results[0].Err != nil {
		t.Fatal(results[0].Err)
	}
	if results[0].Action != "updated" {
		t.Errorf("Action = %q, want updated", results[0].Action)
	}
	data, _ := os.ReadFile(filepath.Join(repoRoot, "services-a", "README.md"))
	if string(data) != "v2\n" {
		t.Errorf("expected the fast-forwarded content %q, got %q", "v2\n", data)
	}
}

func TestSync_RefusesDirtyWorkingTree(t *testing.T) {
	remote := newBareRemote(t)
	repoRoot := t.TempDir()
	entry := Entry{Name: "services-a", IsGit: true, URL: remote, LocalPath: filepath.Join(repoRoot, "services-a")}
	m := &Manifest{Entries: map[string]Entry{"services-a": entry}, Order: []string{"services-a"}}

	if results := Sync(context.Background(), m, nil); results[0].Err != nil {
		t.Fatal(results[0].Err)
	}

	// Make an uncommitted local change.
	dirtyPath := filepath.Join(entry.LocalPath, "README.md")
	if err := os.WriteFile(dirtyPath, []byte("locally edited, never committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results := Sync(context.Background(), m, nil)
	if results[0].Err == nil {
		t.Fatal("expected an error for a dirty working tree")
	}
	if !strings.Contains(results[0].Err.Error(), "uncommitted") {
		t.Errorf("error should mention uncommitted changes, got: %v", results[0].Err)
	}

	// Must NOT have touched the file — the whole point is refusing
	// rather than discarding.
	data, err := os.ReadFile(dirtyPath)
	if err != nil || string(data) != "locally edited, never committed\n" {
		t.Errorf("dirty file was touched/discarded: content=%q err=%v", data, err)
	}
}

func TestSync_RefusesDivergedLocalBranchRatherThanDiscarding(t *testing.T) {
	remote := newBareRemote(t)
	repoRoot := t.TempDir()
	entry := Entry{Name: "services-a", IsGit: true, URL: remote, LocalPath: filepath.Join(repoRoot, "services-a")}
	m := &Manifest{Entries: map[string]Entry{"services-a": entry}, Order: []string{"services-a"}}

	if results := Sync(context.Background(), m, nil); results[0].Err != nil {
		t.Fatal(results[0].Err)
	}

	// Make a real, COMMITTED local commit that's never pushed — a clean
	// working tree (status --porcelain is empty), but a real divergence
	// from origin that a naive "just pull" would either merge-commit or,
	// worse, a "reset --hard" would silently discard.
	runGitT(t, entry.LocalPath, "config", "user.email", "test@example.com")
	runGitT(t, entry.LocalPath, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(entry.LocalPath, "local-only.txt"), []byte("never pushed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, entry.LocalPath, "add", ".")
	runGitT(t, entry.LocalPath, "commit", "--quiet", "-m", "local commit, never pushed")

	// Also push a DIFFERENT new commit to the remote, so there's a real
	// fork in history (not just "local is ahead").
	other := t.TempDir()
	runGitT(t, "", "clone", "--quiet", remote, other)
	runGitT(t, other, "config", "user.email", "test@example.com")
	runGitT(t, other, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("v2 from elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, other, "commit", "--quiet", "-am", "remote-side update")
	runGitT(t, other, "push", "--quiet", "origin", "main")

	results := Sync(context.Background(), m, nil)
	if results[0].Err == nil {
		t.Fatal("expected an error — local has an unpushed commit that can't fast-forward onto origin's diverged history")
	}
	if !strings.Contains(results[0].Err.Error(), "diverged") {
		t.Errorf("error should mention divergence, got: %v", results[0].Err)
	}

	// The local commit must still exist — never discarded.
	log := runGitT(t, entry.LocalPath, "log", "--oneline", "-n", "5")
	if !strings.Contains(log, "local commit, never pushed") {
		t.Errorf("local commit was lost:\n%s", log)
	}
}

func TestSync_LocalPathEntriesAreSkippedNeverTouched(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "marker.txt"), []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := Entry{Name: "legacy-service", IsGit: false, LocalPath: localDir}
	m := &Manifest{Entries: map[string]Entry{"legacy-service": entry}, Order: []string{"legacy-service"}}

	results := Sync(context.Background(), m, nil)
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Action != "local (skipped)" {
		t.Errorf("Action = %q, want \"local (skipped)\"", results[0].Action)
	}
	data, err := os.ReadFile(filepath.Join(localDir, "marker.txt"))
	if err != nil || string(data) != "untouched\n" {
		t.Errorf("local-path entry was touched: %q, err %v", data, err)
	}
}

func TestSync_ContinuesPastAFailedEntry(t *testing.T) {
	remote := newBareRemote(t)
	repoRoot := t.TempDir()
	m := &Manifest{
		Entries: map[string]Entry{
			"broken": {Name: "broken", IsGit: true, URL: "/nonexistent/nothing/here.git", LocalPath: filepath.Join(repoRoot, "broken")},
			"good":   {Name: "good", IsGit: true, URL: remote, LocalPath: filepath.Join(repoRoot, "good")},
		},
		Order: []string{"broken", "good"},
	}

	results := Sync(context.Background(), m, nil)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Name != "broken" || results[0].Err == nil {
		t.Errorf("expected broken to fail, got %+v", results[0])
	}
	if results[1].Name != "good" || results[1].Err != nil {
		t.Errorf("expected good to still succeed despite broken failing first, got %+v", results[1])
	}
}

// TestSync_TerraformArtifactsDontCountAsDirty is a regression test for a
// real bug: running terraform in a cloned repo leaves untracked files
// behind (terraform.tfstate, .terraform/), which made `git status
// --porcelain` report the tree as dirty — meaning the very next `sync`,
// after the completely normal clone-then-apply workflow, would refuse
// to touch a repo that a person never actually changed anything in.
func TestSync_TerraformArtifactsDontCountAsDirty(t *testing.T) {
	remote := newBareRemote(t)
	repoRoot := t.TempDir()
	entry := Entry{Name: "services-a", IsGit: true, URL: remote, LocalPath: filepath.Join(repoRoot, "services-a")}
	m := &Manifest{Entries: map[string]Entry{"services-a": entry}, Order: []string{"services-a"}}

	if results := Sync(context.Background(), m, nil); results[0].Err != nil {
		t.Fatal(results[0].Err)
	}

	// Simulate what a real `terraform init && terraform apply` leaves
	// behind — untracked files terraform itself creates, not anything a
	// person edited.
	if err := os.MkdirAll(filepath.Join(entry.LocalPath, ".terraform", "providers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry.LocalPath, "terraform.tfstate"), []byte(`{"version":4}`), 0o644); err != nil {
		t.Fatal(err)
	}

	results := Sync(context.Background(), m, nil)
	if results[0].Err != nil {
		t.Fatalf("sync should succeed with only terraform's own artifacts present, got: %v", results[0].Err)
	}
	if results[0].Action != "updated" {
		t.Errorf("Action = %q, want updated", results[0].Action)
	}

	// The artifacts themselves must still be untouched — this excludes
	// them from git's dirty-tree accounting, it doesn't delete them.
	if _, err := os.Stat(filepath.Join(entry.LocalPath, "terraform.tfstate")); err != nil {
		t.Errorf("terraform.tfstate should still exist, untouched: %v", err)
	}
}

func TestSync_StillRefusesARealUncommittedChangeAlongsideTerraformArtifacts(t *testing.T) {
	// The exclude fix must not become a blanket "ignore everything"  —
	// a genuine edit to a tracked file sitting right next to terraform's
	// own artifacts must still block sync.
	remote := newBareRemote(t)
	repoRoot := t.TempDir()
	entry := Entry{Name: "services-a", IsGit: true, URL: remote, LocalPath: filepath.Join(repoRoot, "services-a")}
	m := &Manifest{Entries: map[string]Entry{"services-a": entry}, Order: []string{"services-a"}}

	if results := Sync(context.Background(), m, nil); results[0].Err != nil {
		t.Fatal(results[0].Err)
	}

	if err := os.MkdirAll(filepath.Join(entry.LocalPath, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry.LocalPath, "terraform.tfstate"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A REAL edit to the tracked README.
	if err := os.WriteFile(filepath.Join(entry.LocalPath, "README.md"), []byte("locally edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results := Sync(context.Background(), m, nil)
	if results[0].Err == nil {
		t.Fatal("expected an error — a real tracked-file edit must still block sync, exclude patterns must not swallow it")
	}
}

func TestEnsureLocalExclude_Idempotent(t *testing.T) {
	remote := newBareRemote(t)
	repoRoot := t.TempDir()
	entry := Entry{Name: "services-a", IsGit: true, URL: remote, LocalPath: filepath.Join(repoRoot, "services-a")}
	m := &Manifest{Entries: map[string]Entry{"services-a": entry}, Order: []string{"services-a"}}

	Sync(context.Background(), m, nil)

	excludePath := filepath.Join(entry.LocalPath, ".git", "info", "exclude")
	before, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}

	// Calling it again directly must not duplicate the patterns.
	if err := ensureLocalExclude(entry.LocalPath); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("ensureLocalExclude should be a no-op when patterns are already present:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestNoPromptEnv_SetsGitTerminalPromptOff(t *testing.T) {
	env := noPromptEnv()
	found := false
	for _, v := range env {
		if v == "GIT_TERMINAL_PROMPT=0" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected GIT_TERMINAL_PROMPT=0 in the environment git subprocesses run with")
	}
}

func TestNoPromptEnv_PreservesExistingEnvironment(t *testing.T) {
	// Confirms this is additive (os.Environ() + one var), not a
	// from-scratch environment that would silently drop PATH,
	// SSH_AUTH_SOCK, or anything else git/ssh actually needs to work.
	t.Setenv("HOMELAB_DEPLOY_TEST_MARKER", "present")
	env := noPromptEnv()
	found := false
	for _, v := range env {
		if v == "HOMELAB_DEPLOY_TEST_MARKER=present" {
			found = true
			break
		}
	}
	if !found {
		t.Error("noPromptEnv should preserve the rest of the process's environment")
	}
}

func TestNoPromptEnv_DoesNotOverrideAnExistingGitSSHCommand(t *testing.T) {
	// Someone pointing at a specific SSH deploy key via the [env]
	// section (GIT_SSH_COMMAND=ssh -i /path/to/key) must not have that
	// silently clobbered — GIT_TERMINAL_PROMPT is a different variable
	// entirely, so this should already hold, but it's exactly the kind
	// of interaction worth pinning down with a real assertion rather
	// than trusting that two unrelated-sounding env vars don't collide.
	custom := "ssh -i /home/user/.ssh/deploy_key -o IdentitiesOnly=yes"
	t.Setenv("GIT_SSH_COMMAND", custom)
	env := noPromptEnv()

	var gitSSHCommandValues []string
	for _, v := range env {
		if strings.HasPrefix(v, "GIT_SSH_COMMAND=") {
			gitSSHCommandValues = append(gitSSHCommandValues, v)
		}
	}
	if len(gitSSHCommandValues) != 1 {
		t.Fatalf("expected exactly one GIT_SSH_COMMAND entry, got %v", gitSSHCommandValues)
	}
	if gitSSHCommandValues[0] != "GIT_SSH_COMMAND="+custom {
		t.Errorf("GIT_SSH_COMMAND = %q, want the user's own value preserved", gitSSHCommandValues[0])
	}
}
