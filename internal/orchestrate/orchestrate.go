// Package orchestrate holds the plan/apply/init orchestration logic that
// used to live directly in main.go. It's pulled out here so the CLI and
// the web server (internal/server) run through the exact same code path
// — the server isn't a second implementation of "how to plan/apply a
// dependency graph" that could quietly drift from the CLI's behavior,
// it's the same functions with different Hooks.
package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"homelab-deploy/internal/graph"
	"homelab-deploy/internal/manifest"
	"homelab-deploy/internal/report"
	"homelab-deploy/internal/tf"
)

// LogDir is where reports and saved plan files live — see the long
// explanation on PlanFilePath for why plan files specifically don't live
// inside a Terraform working directory.
const LogDir = ".deploy-logs"

// PlanFilePath returns where a directory's saved plan file lives. It's
// deliberately NOT inside the Terraform working directory itself: a
// saved plan is a binary artifact that can contain plan-time secret
// values, and it doesn't belong next to tracked .tf files where it could
// show up in `git status` or get committed by accident. It lives in
// LogDir instead, alongside the HTML/JSON report, which is expected to
// be gitignored.
func PlanFilePath(repoRoot, dir string) string {
	slug := strings.ReplaceAll(dir, "/", "_")
	return filepath.Join(repoRoot, LogDir, slug+".tfplan")
}

// DefaultPlanConcurrency is how many directories plan at once when not
// overridden. These are I/O-bound calls (provider APIs over the
// network: Nomad, Vault, TrueNAS, Authentik, ...), not CPU-bound work,
// so this can reasonably exceed the number of CPU cores.
const DefaultPlanConcurrency = 6

// PlanConcurrency reads DEPLOY_PLAN_PARALLELISM (settable in .env's
// [env] section, or any normal way of setting an environment variable)
// to override DefaultPlanConcurrency. Invalid or non-positive values
// fall back to the default rather than erroring — this is a performance
// tuning knob, not something that should be able to break a run.
func PlanConcurrency() int {
	if v := os.Getenv("DEPLOY_PLAN_PARALLELISM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultPlanConcurrency
}

// Hooks lets a caller (the CLI or the web server) observe and steer a
// run without PlanAll/ApplySequential needing to know which one it's
// talking to.
type Hooks struct {
	// LiveWriter, if non-nil, is called once per directory to get an
	// io.Writer that receives that directory's terraform output as it's
	// produced, in addition to the buffered RawOutput every caller gets
	// regardless. The CLI doesn't set this — it only needs the final
	// one-line summary via OnDirDone. The web server returns an SSE
	// broadcaster writer for the directory.
	LiveWriter func(dir string) io.Writer

	// OnDirDone is called once a directory's plan/apply finishes,
	// successful or not. PlanAll/ApplySequential guarantee this is never
	// called concurrently for two different directories, even though
	// PlanAll itself runs directories in parallel — so implementations
	// (e.g. printing to stdout) don't need their own locking.
	OnDirDone func(dir string, entry report.Entry)

	// HandleManualStep decides whether to proceed with applying dir,
	// given its evaluated manual-step state. Return false to skip
	// applying this directory (recorded as an error entry) instead. If
	// nil, DefaultManualStepHandler is used: proceed only if the
	// directory's .manual-check already reports satisfied (or there's
	// no manual step at all) — safe for a non-interactive caller like
	// the web server, which can't pause for a person to press Enter.
	// The CLI supplies its own version that prints the instructions and
	// blocks on stdin, matching the original interactive behavior.
	HandleManualStep func(dir string, ms graph.ManualStep) bool

	// HandleUnsavedPlanApply is called when dir's backend can't save a
	// generated plan at all (Terraform's "remote" backend in remote-
	// execution mode is the known case) and applying it needs a fresh,
	// explicit confirmation of the exact diff about to be applied —
	// see tf.UnsavedPlanConfirm's doc comment for the full reasoning.
	// If nil, DefaultUnsavedPlanHandler is used: always skip — the only
	// safe default for a non-interactive caller like the web server,
	// which has no way to actually ask anyone. The CLI supplies its own
	// version that prints the diff and blocks on stdin for a "yes".
	HandleUnsavedPlanApply tf.UnsavedPlanConfirm

	// HandleApplyConfirm is called by ApplySequential once a directory's
	// own plan (computed fresh, immediately before this call) shows real
	// changes, giving the caller a chance to review that exact diff
	// before it's applied — entry is the same shape PlanAll produces,
	// ResourceChanges and all. Return false to abort the whole run
	// (recorded as an error for dir, nothing after it runs), matching
	// what happens if you answer anything but "yes" at terraform's own
	// interactive apply prompt. Not called at all when the plan shows no
	// changes (nothing to confirm), nor for a directory whose backend
	// can't save a plan file (see HandleUnsavedPlanApply above — that
	// path already has its own dedicated confirm step immediately before
	// applying, and calling both here would prompt twice for the same
	// diff). If nil, DefaultApplyConfirmHandler is used: always proceed
	// — unlike the other two handlers above, the safe default here is
	// "yes", not "no", since the CLI's existing plan-then-apply-as-two-
	// commands contract already relies on apply never pausing to ask.
	HandleApplyConfirm func(dir string, entry report.Entry) bool

	// Resolver opts ApplySequential's manual-step evaluation into
	// multi-repo path resolution (internal/manifest) — nil is fine
	// (single-repo mode). Every other path Runner touches already
	// resolves correctly on its own via Runner.SetResolver; this is the
	// one place orchestrate itself calls into the graph package's own
	// path resolution (graph.Evaluate, for .manual-check/.manual-before)
	// rather than going through the Runner.
	Resolver *manifest.Resolver
}

func (h Hooks) liveWriter(dir string) io.Writer {
	if h.LiveWriter == nil {
		return nil
	}
	return h.LiveWriter(dir)
}

func (h Hooks) manualStepHandler() func(string, graph.ManualStep) bool {
	if h.HandleManualStep != nil {
		return h.HandleManualStep
	}
	return DefaultManualStepHandler
}

// DefaultManualStepHandler proceeds only when there's nothing to wait on
// (no .manual-before) or a .manual-check already reports satisfied. It
// never blocks or prompts — the right default for any non-interactive
// caller.
func DefaultManualStepHandler(_ string, ms graph.ManualStep) bool {
	return ms.Instructions == "" || ms.Satisfied
}

func (h Hooks) unsavedPlanHandler() tf.UnsavedPlanConfirm {
	if h.HandleUnsavedPlanApply != nil {
		return h.HandleUnsavedPlanApply
	}
	return DefaultUnsavedPlanHandler
}

// DefaultUnsavedPlanHandler always skips — a directory whose backend
// can't save a plan for review needs a real, live confirmation of the
// exact diff about to be applied, and there's no safe way to get one
// without a real person to ask. Matches DefaultManualStepHandler's own
// bias toward refusing rather than guessing when nobody supplied a way
// to actually ask.
func DefaultUnsavedPlanHandler(_ string, _ string) bool {
	return false
}

func (h Hooks) applyConfirmHandler() func(string, report.Entry) bool {
	if h.HandleApplyConfirm != nil {
		return h.HandleApplyConfirm
	}
	return DefaultApplyConfirmHandler
}

// DefaultApplyConfirmHandler always proceeds — see HandleApplyConfirm's
// doc comment for why "yes" (not "no") is the safe default here: the CLI
// never sets this hook, and defaulting to refuse would silently stop
// every existing CLI apply from ever applying anything.
func DefaultApplyConfirmHandler(_ string, _ report.Entry) bool {
	return true
}

// planTF/applyTF call the tf.Runner methods with or without a live
// writer depending on whether the hook supplied one — Plan/Apply take
// liveOut as a variadic arg specifically so callers that don't need it
// (a bare nil here) pay nothing extra.
func planTF(ctx context.Context, runner *tf.Runner, repoRoot, dir, planFile string, live io.Writer) tf.PlanResult {
	if live != nil {
		return runner.Plan(ctx, repoRoot, dir, planFile, live)
	}
	return runner.Plan(ctx, repoRoot, dir, planFile)
}

func applyTF(ctx context.Context, runner *tf.Runner, repoRoot, dir, planFile string, confirmUnsaved tf.UnsavedPlanConfirm, live io.Writer) error {
	if live != nil {
		return runner.Apply(ctx, repoRoot, dir, planFile, confirmUnsaved, live)
	}
	return runner.Apply(ctx, repoRoot, dir, planFile, confirmUnsaved)
}

// PlanAll runs Plan for every directory in dirs concurrently, bounded by
// PlanConcurrency(). Plan is read-only — it never mutates real
// infrastructure — so directories don't need to wait on each other's
// plan the way ApplySequential waits on the dependency graph.
func PlanAll(ctx context.Context, runner *tf.Runner, repoRoot string, dirs []string, hooks Hooks) []report.Entry {
	entries := make([]report.Entry, len(dirs))
	sem := make(chan struct{}, PlanConcurrency())
	var wg sync.WaitGroup
	var hookMu sync.Mutex

	for i, dir := range dirs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, dir string) {
			defer wg.Done()
			defer func() { <-sem }()

			planFile := PlanFilePath(repoRoot, dir)
			res := planTF(ctx, runner, repoRoot, dir, planFile, hooks.liveWriter(dir))
			entry := report.FromPlanResult(res)
			entries[i] = entry

			hookMu.Lock()
			if hooks.OnDirDone != nil {
				hooks.OnDirDone(dir, entry)
			}
			hookMu.Unlock()
		}(i, dir)
	}
	wg.Wait()
	return entries
}

// ApplySequential applies every directory in dirs, in order, stopping at
// the first failure — dirs is expected to already be in dependency
// order (graph.Graph.Order). Unlike PlanAll this never runs directories
// concurrently: apply mutates real infrastructure, and a downstream
// directory can genuinely depend on an upstream one having actually
// been applied already. Returns the entries recorded so far (including
// the one that failed, if any) alongside the error.
func ApplySequential(ctx context.Context, runner *tf.Runner, repoRoot string, dirs []string, hooks Hooks) ([]report.Entry, error) {
	handleManual := hooks.manualStepHandler()
	var entries []report.Entry

	record := func(entry report.Entry) {
		entries = append(entries, entry)
		if hooks.OnDirDone != nil {
			hooks.OnDirDone(entry.Dir, entry)
		}
	}

	for _, dir := range dirs {
		ms, err := graph.Evaluate(hooks.Resolver, repoRoot, dir)
		if err != nil {
			entry := report.Entry{Dir: dir, Status: report.StatusError, Detail: err.Error()}
			record(entry)
			return entries, fmt.Errorf("evaluating manual step for %s: %w", dir, err)
		}
		if !handleManual(dir, ms) {
			err := fmt.Errorf("manual step required first: %s", strings.TrimSpace(ms.Instructions))
			entry := report.Entry{Dir: dir, Status: report.StatusError, Detail: err.Error()}
			record(entry)
			return entries, err
		}

		if !runner.IsInitialized(repoRoot, dir) {
			err := fmt.Errorf("not initialized — run init first")
			if hooks.Resolver != nil {
				if hint := hooks.Resolver.NotSyncedHint(repoRoot, dir); hint != "" {
					err = errors.New(hint)
				}
			}
			entry := report.Entry{Dir: dir, Status: report.StatusError, Detail: err.Error()}
			record(entry)
			return entries, err
		}

		// Plan fresh, immediately before applying, so there's always a
		// real diff to review right here — never a leftover plan file
		// from some earlier, possibly stale "Plan All" click. This is
		// also what makes the confirm gate below trustworthy: the plan
		// file it writes is exactly what applyTF applies a few lines
		// later, the same guarantee a hand-typed
		// `terraform plan -out=x && terraform apply x` gives you.
		planFile := PlanFilePath(repoRoot, dir)
		planRes := planTF(ctx, runner, repoRoot, dir, planFile, hooks.liveWriter(dir))
		planEntry := report.FromPlanResult(planRes)

		if planEntry.Status == report.StatusError {
			record(planEntry)
			return entries, fmt.Errorf("plan failed in %s: %w", dir, planRes.Err)
		}
		if planEntry.Status == report.StatusOK {
			// Nothing to apply here — matches terraform apply itself
			// doing nothing when a plan finds no changes.
			record(planEntry)
			continue
		}

		// planEntry.Status == StatusChanges from here down. A backend
		// that can't save a plan file (UnsavedPlan) already gets its own
		// dedicated re-plan-and-confirm step inside applyTF itself (see
		// HandleUnsavedPlanApply) — asking again here would be a second
		// prompt for the same diff, so skip straight to applyTF for that
		// case.
		if !planRes.UnsavedPlan {
			if !hooks.applyConfirmHandler()(dir, planEntry) {
				detail := "apply declined"
				if ctx.Err() != nil {
					detail = "canceled while awaiting confirmation"
				}
				entry := report.Entry{Dir: dir, Status: report.StatusError, Detail: detail}
				record(entry)
				return entries, fmt.Errorf("apply declined for %s: %s", dir, detail)
			}
		}

		applyErr := applyTF(ctx, runner, repoRoot, dir, planFile, hooks.unsavedPlanHandler(), hooks.liveWriter(dir))

		var entry report.Entry
		if applyErr != nil {
			// The actual error text matters here, not a generic
			// placeholder — e.g. this is exactly what carries the
			// "run homelab-deploy <dir> apply from the CLI instead"
			// message for a directory whose backend can't save a plan
			// for review out to the report/dashboard.
			entry = report.Entry{Dir: dir, Status: report.StatusError, Detail: applyErr.Error()}
		} else {
			entry = report.Entry{Dir: dir, Status: report.StatusOK, Detail: "applied"}
		}
		record(entry)
		if applyErr != nil {
			return entries, fmt.Errorf("apply failed in %s: %w", dir, applyErr)
		}
	}
	return entries, nil
}

// InitAll runs `terraform init` in every directory in dirs. Order
// doesn't matter — init has no cross-directory dependency.
func InitAll(ctx context.Context, runner *tf.Runner, repoRoot string, dirs []string, onDirDone func(dir string, err error)) error {
	var failed bool
	for _, dir := range dirs {
		err := runner.Init(ctx, repoRoot, dir)
		if err != nil {
			failed = true
		}
		if onDirDone != nil {
			onDirDone(dir, err)
		}
	}
	if failed {
		return fmt.Errorf("one or more directories failed to initialize")
	}
	return nil
}
