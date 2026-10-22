package tf

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireTerraform skips the test if a real `terraform` binary isn't on
// PATH. These tests exercise this package's Runner against actual
// `terraform init/plan/apply` — deliberately not mocked, since the whole
// point of this package is the translation between terraform-json output
// and our ResourceChange/PlanResult types, which a mock would hide bugs
// in. They use the built-in terraform_data resource (part of Terraform
// core since 1.4) specifically so no provider download, registry access,
// or network of any kind is needed to run them.
func requireTerraform(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not found on PATH — skipping integration test")
	}
}

// writeTFConfig writes main.tf for one directory under repoRoot/dir.
func writeTFConfig(t *testing.T, repoRoot, dir, config string) {
	t.Helper()
	full := filepath.Join(repoRoot, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "main.tf"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

const tfConfigWithResource = `
resource "terraform_data" "example" {
  input = %q
}
`

const tfConfigEmpty = `
# resource removed
`

// TestIntegration_FullLifecycle drives one directory through the full
// create -> update -> delete cycle and checks that each stage produces
// the ResourceChange/count our report package expects.
func TestIntegration_FullLifecycle(t *testing.T) {
	requireTerraform(t)

	repoRoot := t.TempDir()
	const dir = "example"
	writeTFConfig(t, repoRoot, dir, fmt.Sprintf(tfConfigWithResource, "v1"))

	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := runner.Init(ctx, repoRoot, dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// NOTE: not asserting IsInitialized() here. terraform_data is a
	// built-in provider (no download needed), so `terraform init` never
	// creates .terraform/ for a config that uses *only* it — the
	// directory-existence heuristic genuinely can't distinguish that from
	// "never initialized" in this one degenerate case. Every real
	// directory in this repo pulls in an actual provider (nomad, vault,
	// authentik, ...) and does create .terraform/, so this doesn't affect
	// real usage; IsInitialized's directory-check behavior itself is
	// covered directly by TestIsInitialized in tf_test.go, which doesn't
	// need a real terraform binary.

	// --- create ---
	planFile := filepath.Join(t.TempDir(), "plan.tfplan")
	res := runner.Plan(ctx, repoRoot, dir, planFile)
	if res.Err != nil {
		t.Fatalf("Plan (create): %v\n%s", res.Err, res.RawOutput)
	}
	if !res.HasChanges || res.Adds != 1 || res.Changes != 0 || res.Destroys != 0 {
		t.Fatalf("Plan (create) = %+v, want HasChanges=true Adds=1", res)
	}
	if len(res.ResourceChanges) != 1 || res.ResourceChanges[0].Action != "create" {
		t.Fatalf("ResourceChanges = %+v, want one create", res.ResourceChanges)
	}
	if err := runner.Apply(ctx, repoRoot, dir, planFile, nil); err != nil {
		t.Fatalf("Apply (create): %v", err)
	}

	// --- no-op ---
	res = runner.Plan(ctx, repoRoot, dir, planFile)
	if res.Err != nil {
		t.Fatalf("Plan (no-op): %v\n%s", res.Err, res.RawOutput)
	}
	if res.HasChanges {
		t.Fatalf("Plan (no-op) reported changes it shouldn't have: %+v", res)
	}

	// --- update ---
	writeTFConfig(t, repoRoot, dir, fmt.Sprintf(tfConfigWithResource, "v2"))
	res = runner.Plan(ctx, repoRoot, dir, planFile)
	if res.Err != nil {
		t.Fatalf("Plan (update): %v\n%s", res.Err, res.RawOutput)
	}
	if !res.HasChanges || res.Changes != 1 || res.Adds != 0 || res.Destroys != 0 {
		t.Fatalf("Plan (update) = %+v, want HasChanges=true Changes=1", res)
	}
	if len(res.ResourceChanges) != 1 || res.ResourceChanges[0].Action != "update" {
		t.Fatalf("ResourceChanges = %+v, want one update", res.ResourceChanges)
	}
	if err := runner.Apply(ctx, repoRoot, dir, planFile, nil); err != nil {
		t.Fatalf("Apply (update): %v", err)
	}

	// --- delete ---
	writeTFConfig(t, repoRoot, dir, tfConfigEmpty)
	res = runner.Plan(ctx, repoRoot, dir, planFile)
	if res.Err != nil {
		t.Fatalf("Plan (delete): %v\n%s", res.Err, res.RawOutput)
	}
	if !res.HasChanges || res.Destroys != 1 || res.Adds != 0 || res.Changes != 0 {
		t.Fatalf("Plan (delete) = %+v, want HasChanges=true Destroys=1", res)
	}
	if len(res.ResourceChanges) != 1 || res.ResourceChanges[0].Action != "delete" {
		t.Fatalf("ResourceChanges = %+v, want one delete", res.ResourceChanges)
	}
	if err := runner.Apply(ctx, repoRoot, dir, planFile, nil); err != nil {
		t.Fatalf("Apply (delete): %v", err)
	}

	res = runner.Plan(ctx, repoRoot, dir, planFile)
	if res.Err != nil {
		t.Fatalf("Plan (final): %v\n%s", res.Err, res.RawOutput)
	}
	if res.HasChanges {
		t.Fatalf("Plan (final) reported changes after everything was torn down: %+v", res)
	}
}

// TestIntegration_PlanErrorSurfacesRawOutput checks that a broken// config (a reference to an undeclared variable) comes back as a
// PlanResult.Err with the actual terraform error text preserved in
// RawOutput, rather than a generic failure — the RawOutput field is what
// the HTML report's "full output" panel shows.
func TestIntegration_PlanErrorSurfacesRawOutput(t *testing.T) {
	requireTerraform(t)

	repoRoot := t.TempDir()
	const dir = "broken"
	writeTFConfig(t, repoRoot, dir, `
resource "terraform_data" "example" {
  input = var.undeclared
}
`)

	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := runner.Init(ctx, repoRoot, dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	planFile := filepath.Join(t.TempDir(), "plan.tfplan")
	res := runner.Plan(ctx, repoRoot, dir, planFile)
	if res.Err == nil {
		t.Fatal("expected Plan to fail for a reference to an undeclared variable")
	}
}

// TestIntegration_LiveOutputTee checks that Plan/Apply's optional
// liveOut writer actually receives real terraform output as it's
// produced — not just that RawOutput still gets built correctly. This
// is what the web server's SSE streaming depends on, so it's worth
// verifying against a real terraform run rather than only trusting that
// io.MultiWriter itself is correct (it is, but the wiring around it is
// what could actually be wrong).
func TestIntegration_LiveOutputTee(t *testing.T) {
	requireTerraform(t)

	repoRoot := t.TempDir()
	const dir = "example"
	writeTFConfig(t, repoRoot, dir, fmt.Sprintf(tfConfigWithResource, "v1"))

	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := runner.Init(ctx, repoRoot, dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	var live bytes.Buffer
	planFile := filepath.Join(t.TempDir(), "plan.tfplan")
	res := runner.Plan(ctx, repoRoot, dir, planFile, &live)
	if res.Err != nil {
		t.Fatalf("Plan: %v\n%s", res.Err, res.RawOutput)
	}
	if live.Len() == 0 {
		t.Fatal("liveOut received nothing during Plan")
	}
	if live.String() != res.RawOutput {
		t.Errorf("liveOut content differs from RawOutput — they should be identical, just one written live and one buffered\nlive: %q\nRawOutput: %q", live.String(), res.RawOutput)
	}

	live.Reset()
	if err := runner.Apply(ctx, repoRoot, dir, planFile, nil, &live); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if live.Len() == 0 {
		t.Fatal("liveOut received nothing during Apply")
	}
}

// TestApply_AppliesFreshWhenNoSavedPlanFileExists exercises the
// "try to save a plan fresh" path Apply now takes whenever there's no
// saved plan file yet — the common case being a directory nobody ran
// Plan against before calling Apply directly. For any backend that
// supports saving (a plain local backend here — this doesn't need a
// remote-execution trigger, which isn't reproducible in this
// environment), that should just work transparently: no confirmation
// needed (confirmUnsaved is nil here, and Apply must not need it), the
// freshly saved plan gets applied exactly as if Plan had been run
// first. Terraform's "remote" backend refusing to save at all — the
// case that actually needs confirmUnsaved — can't be triggered for
// real here; see confirmUnsavedPlanCLI's own tests in the main package
// for coverage of that side of the flow instead.
func TestApply_AppliesFreshWhenNoSavedPlanFileExists(t *testing.T) {
	requireTerraform(t)

	repoRoot := t.TempDir()
	const dir = "services/example"
	full := filepath.Join(repoRoot, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "main.tf"), []byte(`
resource "terraform_data" "example" {
  input = "applied-without-a-saved-plan"
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

	// Deliberately never call Plan at all — planFile never exists,
	// exactly matching what happens after Plan degrades for an
	// UnsavedPlan directory (it explicitly avoids writing one).
	planFile := filepath.Join(t.TempDir(), "never-written.tfplan")
	if _, err := os.Stat(planFile); !os.IsNotExist(err) {
		t.Fatalf("test setup bug: planFile should not exist, stat returned: %v", err)
	}

	if err := runner.Apply(ctx, repoRoot, dir, planFile, nil); err != nil {
		t.Fatalf("Apply with no saved plan file should re-plan and apply directly, got: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(full, "terraform.tfstate"))
	if err != nil {
		t.Fatalf("expected state to exist after apply: %v", err)
	}
	if !strings.Contains(string(data), "applied-without-a-saved-plan") {
		t.Fatalf("resource wasn't actually created:\n%s", data)
	}
}
