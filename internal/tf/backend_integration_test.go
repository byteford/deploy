package tf

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateBackend_RealLocalToLocalMigration exercises the entire
// migration mechanism against real terraform — the backend stub, the
// {dir} substitution, and critically -force-copy actually copying
// EXISTING state into the new location rather than just quietly
// starting the new backend with empty state (which would look
// successful but silently lose track of every real resource).
//
// It uses the "local" backend type pointed at two DIFFERENT paths
// rather than a real remote backend — Terraform's local backend
// genuinely goes through the same migration codepath (backend change
// detected, -force-copy triggers a real state copy) as a remote one
// would, so this is a real test of the mechanism, not a mock of it. It
// just doesn't need any cloud credentials or network access to prove it.
func TestMigrateBackend_RealLocalToLocalMigration(t *testing.T) {
	requireTerraform(t)

	repoRoot := t.TempDir()
	const dir = "services/example"
	full := filepath.Join(repoRoot, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "main.tf"), []byte(`
resource "terraform_data" "example" {
  input = "migrate-me"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Stage 1: default (implicit local) backend, with a real resource
	// in its state — this is "before", the thing that must survive.
	if err := runner.Init(ctx, repoRoot, dir); err != nil {
		t.Fatalf("initial Init: %v", err)
	}
	planFile := filepath.Join(t.TempDir(), "plan.tfplan")
	res := runner.Plan(ctx, repoRoot, dir, planFile)
	if res.Err != nil {
		t.Fatalf("initial Plan: %v\n%s", res.Err, res.RawOutput)
	}
	if err := runner.Apply(ctx, repoRoot, dir, planFile, nil); err != nil {
		t.Fatalf("initial Apply: %v", err)
	}

	defaultStatePath := filepath.Join(full, "terraform.tfstate")
	if _, err := os.Stat(defaultStatePath); err != nil {
		t.Fatalf("expected default local state file to exist before migration: %v", err)
	}

	// Stage 2: migrate to an EXPLICIT local backend at a different path
	// — same mechanism (-force-copy + -backend-config) a remote backend
	// migration would use, fully offline.
	newStateDir := t.TempDir()
	newStatePath := filepath.Join(newStateDir, dir+".tfstate")
	backend := BackendConfig{
		Type:     "local",
		Settings: map[string]string{"path": filepath.Join(newStateDir, "{dir}.tfstate")},
	}

	if err := WriteBackendStub(filepath.Join(repoRoot, dir), backend.Type); err != nil {
		t.Fatalf("WriteBackendStub: %v", err)
	}
	if err := runner.MigrateBackend(ctx, repoRoot, dir, backend); err != nil {
		t.Fatalf("MigrateBackend: %v", err)
	}

	// The real proof this worked: the resource's actual state must be
	// readable from the NEW path, not just an empty state file created
	// there. Confirm by reading the new state file directly...
	newStateBytes, err := os.ReadFile(newStatePath)
	if err != nil {
		t.Fatalf("expected state at the new path %s: %v", newStatePath, err)
	}
	if !strings.Contains(string(newStateBytes), "migrate-me") {
		t.Fatalf("new state file doesn't contain the migrated resource's data:\n%s", newStateBytes)
	}

	// ...and by confirming a fresh Plan against the NEW backend sees
	// the resource as already existing (no changes needed) rather than
	// planning to create it again from scratch, which is what would
	// happen if migration had silently started with empty state instead
	// of actually copying the old state over.
	planFile2 := filepath.Join(t.TempDir(), "plan2.tfplan")
	res = runner.Plan(ctx, repoRoot, dir, planFile2)
	if res.Err != nil {
		t.Fatalf("post-migration Plan: %v\n%s", res.Err, res.RawOutput)
	}
	if res.HasChanges {
		t.Fatalf("post-migration Plan reported changes — state wasn't actually carried over: %+v", res)
	}
}

// TestMigrateBackend_FailedMigrationRollsBackStub confirms the
// idempotency contract end to end: if the migration itself fails, the
// stub this test's caller would have written must be removable so a
// retry is treated as "not yet migrated" rather than silently skipped.
// This exercises RemoveBackendStub's role in that contract directly
// against a real failure (an invalid backend type that terraform itself
// rejects), rather than only unit-testing RemoveBackendStub in
// isolation.
func TestMigrateBackend_FailedMigrationRollsBackStub(t *testing.T) {
	requireTerraform(t)

	repoRoot := t.TempDir()
	const dir = "services/example"
	full := filepath.Join(repoRoot, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "main.tf"), []byte(`
resource "terraform_data" "example" {
  input = "v1"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := runner.Init(ctx, repoRoot, dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// A backend TYPE that doesn't exist — terraform will reject this at
	// init time, simulating a real migration failure (a typo'd type,
	// bad credentials, unreachable bucket, etc. would fail the same way
	// from this package's point of view: Init returns an error).
	const bogusType = "not-a-real-backend-type"
	if err := WriteBackendStub(filepath.Join(repoRoot, dir), bogusType); err != nil {
		t.Fatalf("WriteBackendStub: %v", err)
	}
	backend := BackendConfig{Type: bogusType, Settings: map[string]string{"path": "{dir}.tfstate"}}

	migrateErr := runner.MigrateBackend(ctx, repoRoot, dir, backend)
	if migrateErr == nil {
		t.Fatal("expected an error for a nonexistent backend type")
	}

	// This mirrors what main.go's migrateOneDir does on failure — the
	// caller is responsible for the rollback, this test just confirms
	// RemoveBackendStub actually restores the pre-migration state.
	if err := RemoveBackendStub(filepath.Join(repoRoot, dir)); err != nil {
		t.Fatalf("RemoveBackendStub: %v", err)
	}
	if HasBackendStub(filepath.Join(repoRoot, dir)) {
		t.Fatal("stub should be gone after rollback, so a retry is treated as not-yet-migrated")
	}
}
