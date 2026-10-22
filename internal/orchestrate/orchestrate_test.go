package orchestrate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"homelab-deploy/internal/graph"
	"homelab-deploy/internal/report"
	"homelab-deploy/internal/tf"
)

func requireTerraform(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not found on PATH — skipping")
	}
}

// buildFakeRepo creates n service directories, each declaring a distinct
// terraform_data resource (so every directory has real, independent
// pending changes to plan), and runs `terraform init` in each. Returns
// the repo root and the directory list in a stable order.
func buildFakeRepo(t *testing.T, n int) (repoRoot string, dirs []string) {
	t.Helper()
	repoRoot = t.TempDir()
	runner, err := tf.NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for i := 0; i < n; i++ {
		dir := filepath.Join("services", "svc"+string(rune('a'+i)))
		full := filepath.Join(repoRoot, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := `resource "terraform_data" "example" { input = "` + dir + `" }`
		if err := os.WriteFile(filepath.Join(full, "main.tf"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := runner.Init(ctx, repoRoot, dir); err != nil {
			t.Fatalf("init %s: %v", dir, err)
		}
		dirs = append(dirs, dir)
	}
	return repoRoot, dirs
}

func TestPlanAll_CorrectUnderConcurrency(t *testing.T) {
	requireTerraform(t)
	repoRoot, dirs := buildFakeRepo(t, 6)

	runner, err := tf.NewRunner()
	if err != nil {
		t.Fatal(err)
	}

	entries := PlanAll(context.Background(), runner, repoRoot, dirs, Hooks{})

	if len(entries) != len(dirs) {
		t.Fatalf("got %d entries, want %d", len(entries), len(dirs))
	}
	for i, dir := range dirs {
		// Index-based assignment must survive concurrent completion order
		// — entries[i] must correspond to dirs[i] regardless of which
		// goroutine finished first.
		if entries[i].Dir != dir {
			t.Errorf("entries[%d].Dir = %q, want %q (order not preserved under concurrency)", i, entries[i].Dir, dir)
		}
		if entries[i].Status != report.StatusChanges {
			t.Errorf("entries[%d] (%s) status = %s, want %s — %s", i, dir, entries[i].Status, report.StatusChanges, entries[i].Detail)
		}
		if entries[i].Adds != 1 {
			t.Errorf("entries[%d] (%s) Adds = %d, want 1", i, dir, entries[i].Adds)
		}
	}
}

func TestPlanAll_OnDirDoneNeverConcurrent(t *testing.T) {
	requireTerraform(t)
	repoRoot, dirs := buildFakeRepo(t, 6)
	runner, err := tf.NewRunner()
	if err != nil {
		t.Fatal(err)
	}

	var inHook bool // set/cleared inside OnDirDone; a race detector run
	// (or this manual check) catches concurrent entry.
	var calls int
	hooks := Hooks{OnDirDone: func(dir string, entry report.Entry) {
		if inHook {
			t.Error("OnDirDone called concurrently — PlanAll must serialize these calls")
		}
		inHook = true
		calls++
		inHook = false
	}}

	PlanAll(context.Background(), runner, repoRoot, dirs, hooks)
	if calls != len(dirs) {
		t.Errorf("OnDirDone called %d times, want %d", calls, len(dirs))
	}
}

func TestPlanConcurrency_EnvOverride(t *testing.T) {
	t.Setenv("DEPLOY_PLAN_PARALLELISM", "3")
	if got := PlanConcurrency(); got != 3 {
		t.Errorf("PlanConcurrency() = %d, want 3", got)
	}

	t.Setenv("DEPLOY_PLAN_PARALLELISM", "not-a-number")
	if got := PlanConcurrency(); got != DefaultPlanConcurrency {
		t.Errorf("PlanConcurrency() with garbage input = %d, want default %d", got, DefaultPlanConcurrency)
	}

	t.Setenv("DEPLOY_PLAN_PARALLELISM", "-1")
	if got := PlanConcurrency(); got != DefaultPlanConcurrency {
		t.Errorf("PlanConcurrency() with a non-positive value = %d, want default %d", got, DefaultPlanConcurrency)
	}

	os.Unsetenv("DEPLOY_PLAN_PARALLELISM")
	if got := PlanConcurrency(); got != DefaultPlanConcurrency {
		t.Errorf("PlanConcurrency() with nothing set = %d, want default %d", got, DefaultPlanConcurrency)
	}
}

// TestPlanAll_FasterThanSequential is informational, not a correctness
// check (timing comparisons are inherently a bit flaky) — it logs real
// elapsed time for concurrency=1 vs the default, across enough
// directories that per-process overhead (not any artificial delay)
// alone should produce a measurable difference.
func TestPlanAll_FasterThanSequential(t *testing.T) {
	requireTerraform(t)
	if testing.Short() {
		t.Skip("timing comparison skipped in -short mode")
	}
	repoRoot, dirs := buildFakeRepo(t, 10)
	runner, err := tf.NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	t.Setenv("DEPLOY_PLAN_PARALLELISM", "1")
	start := time.Now()
	PlanAll(ctx, runner, repoRoot, dirs, Hooks{})
	sequential := time.Since(start)

	os.Unsetenv("DEPLOY_PLAN_PARALLELISM")
	start = time.Now()
	PlanAll(ctx, runner, repoRoot, dirs, Hooks{})
	concurrent := time.Since(start)

	t.Logf("10 directories: concurrency=1 took %s, concurrency=%d took %s", sequential, DefaultPlanConcurrency, concurrent)
	if concurrent >= sequential {
		t.Logf("warning: concurrent run wasn't faster (sequential=%s concurrent=%s) — expected on a heavily loaded/low-core machine, not necessarily a bug", sequential, concurrent)
	}
}

func TestApplySequential_StopsAtFirstFailure(t *testing.T) {
	requireTerraform(t)
	repoRoot, dirs := buildFakeRepo(t, 3)
	// Work around the known builtin-provider-only IsInitialized gap
	// (terraform_data creates no .terraform/) for the two directories
	// this test actually expects to get as far as attempting apply on —
	// not what's under test here.
	for _, d := range dirs[:2] {
		if err := os.MkdirAll(filepath.Join(repoRoot, d, ".terraform"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Break the second directory's config so its apply fails.
	broken := filepath.Join(repoRoot, dirs[1], "main.tf")
	if err := os.WriteFile(broken, []byte(`resource "terraform_data" "example" { input = var.undeclared }`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner, err := tf.NewRunner()
	if err != nil {
		t.Fatal(err)
	}

	var doneOrder []string
	hooks := Hooks{OnDirDone: func(dir string, entry report.Entry) { doneOrder = append(doneOrder, dir) }}

	entries, err := ApplySequential(context.Background(), runner, repoRoot, dirs, hooks)
	if err == nil {
		t.Fatal("expected an error from the broken second directory")
	}
	// Must have attempted dirs[0] (succeeds) and dirs[1] (fails), and
	// must NOT have gone on to dirs[2] — that's the whole point of
	// sequential, stop-on-failure apply.
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want exactly 2 (stopped after the failure): %+v", len(entries), entries)
	}
	if entries[0].Status != report.StatusOK {
		t.Errorf("dirs[0] status = %s, want OK", entries[0].Status)
	}
	if entries[1].Status != report.StatusError {
		t.Errorf("dirs[1] status = %s, want ERROR", entries[1].Status)
	}
	for _, d := range doneOrder {
		if d == dirs[2] {
			t.Errorf("dirs[2] should never have been attempted, but OnDirDone was called for it")
		}
	}
}

// TestApplySequential_HandleApplyConfirmCalledOnlyWhenChangesExist verifies
// the new plan-then-confirm gate only ever asks about a directory that
// actually has something to apply — a directory already applied (no
// pending changes on this fresh plan) must apply silently, with no call to
// HandleApplyConfirm at all, exactly like terraform apply itself doing
// nothing when there's nothing to do.
func TestApplySequential_HandleApplyConfirmCalledOnlyWhenChangesExist(t *testing.T) {
	requireTerraform(t)
	repoRoot, dirs := buildFakeRepo(t, 2)
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(repoRoot, d, ".terraform"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	runner, err := tf.NewRunner()
	if err != nil {
		t.Fatal(err)
	}

	// Apply dirs[0] up front (default hooks — always proceeds) so its
	// next plan shows no changes.
	if _, err := ApplySequential(context.Background(), runner, repoRoot, dirs[:1], Hooks{}); err != nil {
		t.Fatalf("priming apply of dirs[0] failed: %v", err)
	}

	var confirmedDirs []string
	hooks := Hooks{HandleApplyConfirm: func(dir string, entry report.Entry) bool {
		confirmedDirs = append(confirmedDirs, dir)
		if entry.Status != report.StatusChanges || len(entry.ResourceChanges) == 0 {
			t.Errorf("HandleApplyConfirm(%s) called with entry that doesn't show real changes: %+v", dir, entry)
		}
		return true
	}}

	if _, err := ApplySequential(context.Background(), runner, repoRoot, dirs, hooks); err != nil {
		t.Fatalf("ApplySequential: %v", err)
	}
	if len(confirmedDirs) != 1 || confirmedDirs[0] != dirs[1] {
		t.Errorf("HandleApplyConfirm called for %v, want exactly [%s] (dirs[0] already had no changes to confirm)", confirmedDirs, dirs[1])
	}
}

// TestApplySequential_HandleApplyConfirmDeclineStopsRun verifies declining
// the confirm gate aborts the whole run without applying anything —
// matching what happens if you answer anything but "yes" at terraform's
// own interactive apply prompt.
func TestApplySequential_HandleApplyConfirmDeclineStopsRun(t *testing.T) {
	requireTerraform(t)
	repoRoot, dirs := buildFakeRepo(t, 2)
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(repoRoot, d, ".terraform"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	runner, err := tf.NewRunner()
	if err != nil {
		t.Fatal(err)
	}

	var doneOrder []string
	hooks := Hooks{
		OnDirDone:          func(dir string, entry report.Entry) { doneOrder = append(doneOrder, dir) },
		HandleApplyConfirm: func(dir string, entry report.Entry) bool { return false },
	}

	entries, err := ApplySequential(context.Background(), runner, repoRoot, dirs, hooks)
	if err == nil {
		t.Fatal("expected an error — declining the confirm gate must stop the run")
	}
	if len(entries) != 1 || entries[0].Dir != dirs[0] || entries[0].Status != report.StatusError {
		t.Fatalf("expected exactly one ERROR entry for dirs[0], got %+v", entries)
	}
	for _, d := range doneOrder {
		if d == dirs[1] {
			t.Error("dirs[1] should never have been attempted after dirs[0]'s apply was declined")
		}
	}

	// Nothing should actually have been applied — dirs[0] must still
	// show the same pending change on a fresh plan.
	res := runner.Plan(context.Background(), repoRoot, dirs[0], PlanFilePath(repoRoot, dirs[0]))
	if res.Err != nil {
		t.Fatalf("re-planning dirs[0] after the declined apply: %v", res.Err)
	}
	if !res.HasChanges {
		t.Error("dirs[0] shows no changes after a declined apply — something was applied despite the decline")
	}
}

func TestApplySequential_ManualStepBlocksByDefault(t *testing.T) {
	requireTerraform(t)
	repoRoot, dirs := buildFakeRepo(t, 1)
	if err := os.WriteFile(filepath.Join(repoRoot, dirs[0], ".manual-before"), []byte("unseal vault"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner, err := tf.NewRunner()
	if err != nil {
		t.Fatal(err)
	}

	// No HandleManualStep hook -> DefaultManualStepHandler -> must NOT
	// proceed, since there's no .manual-check reporting satisfied. This
	// is exactly the safety property a non-interactive caller (the web
	// server) depends on.
	entries, err := ApplySequential(context.Background(), runner, repoRoot, dirs, Hooks{})
	if err == nil {
		t.Fatal("expected an error — a manual step with no satisfied check must block by default")
	}
	if len(entries) != 1 || entries[0].Status != report.StatusError {
		t.Fatalf("expected one blocked entry, got %+v", entries)
	}
}

func TestApplySequential_ManualStepHookCanOverride(t *testing.T) {
	requireTerraform(t)
	repoRoot, dirs := buildFakeRepo(t, 1)
	if err := os.WriteFile(filepath.Join(repoRoot, dirs[0], ".manual-before"), []byte("unseal vault"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Work around the known builtin-provider-only IsInitialized gap
	// (terraform_data creates no .terraform/) — not what's under test.
	if err := os.MkdirAll(filepath.Join(repoRoot, dirs[0], ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}

	runner, err := tf.NewRunner()
	if err != nil {
		t.Fatal(err)
	}

	var sawInstructions string
	hooks := Hooks{HandleManualStep: func(dir string, ms graph.ManualStep) bool {
		sawInstructions = ms.Instructions
		return true // e.g. the CLI's interactive confirm
	}}
	_, err = ApplySequential(context.Background(), runner, repoRoot, dirs, hooks)
	if err != nil {
		t.Fatalf("expected the override hook to let this proceed, got: %v", err)
	}
	if sawInstructions != "unseal vault" {
		t.Errorf("hook saw instructions %q, want %q", sawInstructions, "unseal vault")
	}
}

func TestDefaultManualStepHandler(t *testing.T) {
	cases := []struct {
		name string
		ms   graph.ManualStep
		want bool
	}{
		{"no manual step at all", graph.ManualStep{}, true},
		{"satisfied check", graph.ManualStep{Instructions: "x", Satisfied: true}, true},
		{"unsatisfied, no check", graph.ManualStep{Instructions: "x", Satisfied: false}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DefaultManualStepHandler("services/a", c.ms); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestDefaultUnsavedPlanHandler(t *testing.T) {
	// Always false, regardless of input — the only safe default for a
	// caller with no way to actually ask anyone (the web server), same
	// posture as DefaultManualStepHandler's own bias toward refusing
	// rather than guessing when nothing else was supplied.
	if DefaultUnsavedPlanHandler("services/a", "Plan: 1 to add, 0 to change, 0 to destroy.") {
		t.Error("expected false regardless of input")
	}
	if DefaultUnsavedPlanHandler("", "") {
		t.Error("expected false for empty input too")
	}
}

func TestHooks_UnsavedPlanHandler_NilFallsBackToDefault(t *testing.T) {
	var h Hooks // HandleUnsavedPlanApply left nil
	if h.unsavedPlanHandler()("services/a", "some plan text") {
		t.Error("a nil HandleUnsavedPlanApply must fall back to the safe default (always false)")
	}
}

func TestHooks_UnsavedPlanHandler_UsesSuppliedHook(t *testing.T) {
	var sawDir, sawText string
	h := Hooks{
		HandleUnsavedPlanApply: func(dir, planText string) bool {
			sawDir, sawText = dir, planText
			return true
		},
	}
	if !h.unsavedPlanHandler()("services/a", "Plan: 2 to add, 0 to change, 0 to destroy.") {
		t.Error("expected the supplied hook's return value to be used")
	}
	if sawDir != "services/a" || sawText != "Plan: 2 to add, 0 to change, 0 to destroy." {
		t.Errorf("hook received dir=%q planText=%q, want the exact values passed in", sawDir, sawText)
	}
}

func TestInitAll_ReportsPerDirectoryAndAggregateError(t *testing.T) {
	requireTerraform(t)
	repoRoot := t.TempDir()
	dir := filepath.Join("services", "a")
	if err := os.MkdirAll(filepath.Join(repoRoot, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, dir, "main.tf"), []byte(`resource "terraform_data" "x" {}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner, err := tf.NewRunner()
	if err != nil {
		t.Fatal(err)
	}

	var seen []string
	err = InitAll(context.Background(), runner, repoRoot, []string{dir}, func(d string, initErr error) {
		seen = append(seen, d)
		if initErr != nil {
			t.Errorf("unexpected init error for %s: %v", d, initErr)
		}
	})
	if err != nil {
		t.Fatalf("InitAll: %v", err)
	}
	if len(seen) != 1 || seen[0] != dir {
		t.Errorf("onDirDone calls = %v, want [%s]", seen, dir)
	}
}

func TestPlanFilePath_NeverInsideTheDirItself(t *testing.T) {
	got := PlanFilePath("/repo", "services/a")
	want := filepath.Join("/repo", LogDir, "services_a.tfplan")
	if got != want {
		t.Errorf("PlanFilePath = %q, want %q", got, want)
	}
}
