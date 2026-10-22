// Package tf drives the terraform CLI via the official terraform-exec
// library and parses plan output via terraform-json, instead of grepping
// human-readable text (the bash version's `grep -E '^Plan:'`). That's the
// main win over the shell script: a `Plan` result here carries an actual
// per-resource list of create/update/delete/replace actions, which the
// text-based approach can't give you.
package tf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"homelab-deploy/internal/manifest"
)

// ResourceChange is one resource's planned action, flattened out of
// terraform-json's richer (and more verbose) ResourceChange type.
// Attributes is populated only for "update"/"replace" — the leaf-level
// diff of what's actually changing, for create/delete everything is
// new/gone so a diff adds nothing.
type ResourceChange struct {
	Address    string
	Action     string // "create", "update", "delete", "replace", "no-op"
	Attributes []AttributeChange
}

// PlanResult is the structured outcome of planning one directory.
type PlanResult struct {
	Dir                     string
	Initialized             bool
	HasChanges              bool
	ResourceChanges         []ResourceChange
	Adds, Changes, Destroys int
	RawOutput               string // human-readable terraform output, for the report's collapsible detail
	Err                     error
	// UnsavedPlan is true when this directory's backend refused to save
	// a generated plan locally (Terraform's "remote" backend does this
	// when a workspace uses remote execution — Terraform Cloud itself
	// manages the plan artifact, not the local disk) — see Plan's doc
	// comment. Apply for this directory will re-plan and require an
	// explicit confirmation of that fresh diff before applying it —
	// see UnsavedPlanConfirm.
	UnsavedPlan bool
	// RemotePlanURL is the "view this run in a browser" link Terraform
	// Cloud prints to a remote-execution workspace's plain-text output
	// (see remoteRunURLLine) — empty unless UnsavedPlan is also true,
	// since that output only exists on the same retry that sets it.
	// Lets the dashboard/report point someone at TFC's own UI for the
	// structured detail this backend can't hand back locally.
	RemotePlanURL string
	// Warnings is every warning diagnostic Terraform printed to
	// RawOutput (see parseWarnings) — e.g. a deprecated argument, or a
	// value assigned for an undeclared variable. Set whenever RawOutput
	// is, regardless of whether the plan itself errored or found no
	// changes, since a warning can precede either.
	Warnings []Warning
}

// Runner holds the resolved terraform binary so callers don't repeatedly
// re-resolve PATH.
type Runner struct {
	execPath string
	resolver *manifest.Resolver // nil = single-repo mode, filepath.Join(repoRoot, dir)
}

func NewRunner() (*Runner, error) {
	path, err := exec.LookPath("terraform")
	if err != nil {
		return nil, fmt.Errorf("terraform not found on PATH: %w", err)
	}
	return &Runner{execPath: path}, nil
}

// SetResolver opts a Runner into multi-repo path resolution — see
// internal/manifest. Deliberately a post-construction setter rather
// than a NewRunner parameter, so every existing caller (and the large
// existing test suite that constructs a bare NewRunner()) keeps working
// unchanged; only main.go/internal/server, which actually need
// multi-repo awareness, call this.
func (r *Runner) SetResolver(resolver *manifest.Resolver) {
	r.resolver = resolver
}

// resolveWorkDir is where every method below turns (repoRoot, dir) into
// the real directory terraform should run in — repoRoot-joined-with-dir
// in single-repo mode, or the manifest-resolved WorkDir (which may
// differ entirely, and may be a subpath within a repo's own checkout)
// once SetResolver has been called.
func (r *Runner) resolveWorkDir(repoRoot, dir string) string {
	if r.resolver != nil {
		return r.resolver.WorkDir(repoRoot, dir)
	}
	return filepath.Join(repoRoot, dir)
}

func (r *Runner) newClient(repoRoot, dir string) (*tfexec.Terraform, error) {
	if r.resolver != nil {
		if hint := r.resolver.NotSyncedHint(repoRoot, dir); hint != "" {
			return nil, errors.New(hint)
		}
	}
	workDir := r.resolveWorkDir(repoRoot, dir)
	tf, err := tfexec.NewTerraform(workDir, r.execPath)
	if err != nil {
		return nil, err
	}
	return tf, nil
}

// IsInitialized reports whether `terraform init` has been run in dir —
// same check as the bash version's `[[ -d "$dir/.terraform" ]]`.
func (r *Runner) IsInitialized(repoRoot, dir string) bool {
	info, err := os.Stat(filepath.Join(r.resolveWorkDir(repoRoot, dir), ".terraform"))
	return err == nil && info.IsDir()
}

// Init runs `terraform init -upgrade` in dir.
func (r *Runner) Init(ctx context.Context, repoRoot, dir string) error {
	tf, err := r.newClient(repoRoot, dir)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	tf.SetStdout(&buf)
	tf.SetStderr(&buf)
	if err := tf.Init(ctx, tfexec.Upgrade(true)); err != nil {
		return fmt.Errorf("%w\n%s", err, buf.String())
	}
	return nil
}

// Plan runs `terraform plan`, writes a plan file to planFile, and parses
// it via terraform-json for a structured per-resource summary. This
// replaces the bash version's `-detailed-exitcode` + grep entirely.
//
// planFile is caller-chosen deliberately: it does NOT belong inside dir
// (a real Terraform working directory) — a saved plan file is a binary
// artifact that can contain plan-time secret values, and leaving it next
// to tracked .tf files means it can end up in `git status`/committed by
// accident. Callers should point it at a gitignored scratch location
// (e.g. the same log directory the HTML/JSON report goes to).
//
// liveOut, if given (non-nil), receives terraform's stdout/stderr as
// it's written, in addition to (not instead of) the buffer used to build
// RawOutput — e.g. the web server tees this to an SSE broadcaster so a
// browser can watch the plan happen in real time. The CLI doesn't need
// this and passes nothing.
func (r *Runner) Plan(ctx context.Context, repoRoot, dir, planFile string, liveOut ...io.Writer) PlanResult {
	res := PlanResult{Dir: dir}

	tf, err := r.newClient(repoRoot, dir)
	if err != nil {
		res.Err = err
		return res
	}

	if err := os.MkdirAll(filepath.Dir(planFile), 0o755); err != nil {
		res.Err = fmt.Errorf("creating directory for plan file: %w", err)
		return res
	}

	var buf bytes.Buffer
	stdout := teeWriter(&buf, liveOut)
	tf.SetStdout(stdout)
	tf.SetStderr(stdout)

	hasChanges, err := tf.Plan(ctx, tfexec.Out(planFile))
	if err != nil && isSavedPlanUnsupported(err) {
		// Some backends refuse to save a generated plan locally at all —
		// Terraform's "remote" backend does this specifically when a
		// workspace uses remote execution, since Terraform Cloud itself
		// manages the plan artifact rather than handing back a local
		// file. Retry as a plain plan (no -out): same tf.Plan call,
		// same buf/liveOut streaming already set up above, so this
		// directory gets the exact same full, human-readable diff
		// every other directory's live pane shows — it just can't be
		// saved for Apply to reuse later. Apply handles that
		// explicitly (see its own doc comment): a directory in this
		// state gets re-planned again, fresh, immediately before
		// applying, with an explicit confirmation required for that
		// specific diff — there's no saved plan to silently trust.
		buf.Reset()
		res.UnsavedPlan = true
		hasChanges, err = tf.Plan(ctx)
	}
	res.RawOutput = buf.String()
	res.Warnings = parseWarnings(res.RawOutput)
	if res.UnsavedPlan {
		res.RemotePlanURL = ParseRemotePlanURL(res.RawOutput)
	}
	if err != nil {
		res.Err = err
		return res
	}
	res.HasChanges = hasChanges
	res.Initialized = true

	if !hasChanges {
		// Nothing to apply against this plan file later — remove it
		// rather than leaving a stale no-op plan lying around.
		os.Remove(planFile)
		return res
	}

	if res.UnsavedPlan {
		// No saved file for ShowPlanFile below to read — get the
		// summary counts from terraform's own human-readable summary
		// line instead ("Plan: 3 to add, 1 to change, 0 to destroy.")
		// — the same universal line the original bash version of this
		// tool grepped for everywhere, and one Terraform Cloud's
		// remote execution is designed to relay faithfully (unlike the
		// newer, less battle-tested `-json` streaming format, which an
		// earlier version of this used and which turned out not to
		// carry the same event types over a real remote-execution
		// connection — this plain-text approach doesn't depend on that
		// guess at all). The full diff is still right there in
		// RawOutput/the live pane; it's just not ALSO broken out into
		// the structured per-resource list every other directory's
		// report gets, since that needs the JSON a saved plan file
		// would give, and there isn't one here.
		res.Adds, res.Changes, res.Destroys = parsePlanSummaryLine(res.RawOutput)
		return res
	}

	// ShowPlanFile runs its own `terraform show -json` under the hood,
	// which writes its own (large, single-line JSON) text output through
	// the same client — and tf.SetStdout from above is still in effect.
	// That output was never part of RawOutput (this snapshot already
	// happened) so the CLI/report never showed it, but it WOULD still
	// hit liveOut if left alone, leaking a JSON dump into the live
	// stream. Redirect to io.Discard first: nobody should see this
	// output at all, buffered or live.
	tf.SetStdout(io.Discard)
	tf.SetStderr(io.Discard)

	plan, err := tf.ShowPlanFile(ctx, planFile)
	if err != nil {
		res.Err = fmt.Errorf("parsing plan file: %w", err)
		return res
	}
	res.ResourceChanges = summarize(plan, &res.Adds, &res.Changes, &res.Destroys)
	return res
}

// isSavedPlanUnsupported reports whether err is Terraform's own "this
// backend can't save a generated plan locally" error. Currently known
// to happen with Terraform's "remote" backend specifically when a
// workspace uses remote execution (using "remote" purely for state
// storage, with local execution, allows -out fine) — Terraform Cloud
// itself manages the plan artifact in that mode, not the local CLI.
// Matched by text since terraform-exec doesn't expose a typed error for
// this; the exact wording is Terraform's own and has been stable across
// versions.
func isSavedPlanUnsupported(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Saving a generated plan is currently not supported")
}

// remoteRunURLLine matches the "To view this run in a browser, visit:"
// line Terraform Cloud prints to a remote-execution workspace's
// plain-text output, followed by the run's own URL on the next line —
// e.g. https://app.terraform.io/app/my-org/my-workspace/runs/run-abc123.
// This is Terraform's own wording, not something this tool controls;
// matched by text for the same reason isSavedPlanUnsupported is (no
// typed/structured alternative exists for plain-text remote-execution
// output).
var remoteRunURLLine = regexp.MustCompile(`To view this run in a browser, visit:\s*\n\s*(\S+)`)

// ParseRemotePlanURL extracts the TFC run URL from a remote-execution
// plan's raw output, or "" if the line isn't present — e.g. a workspace
// configured for remote *state* only, without remote *execution*, never
// prints it even though it can still hit isSavedPlanUnsupported. Exported
// so a caller reviewing an UnsavedPlanConfirm's raw planText (which has no
// other structured way to surface this) can offer the same link.
func ParseRemotePlanURL(output string) string {
	m := remoteRunURLLine.FindStringSubmatch(output)
	if m == nil {
		return ""
	}
	return m[1]
}

// planSummaryLine matches terraform's human-readable plan summary, e.g.
// "Plan: 3 to add, 1 to change, 0 to destroy." — the same line the
// original bash version of this tool grepped for everywhere; here it's
// scoped to just the one case (UnsavedPlan) where structured JSON
// genuinely isn't available.
var planSummaryLine = regexp.MustCompile(`Plan: (\d+) to add, (\d+) to change, (\d+) to destroy`)

func parsePlanSummaryLine(output string) (adds, changes, destroys int) {
	m := planSummaryLine.FindStringSubmatch(output)
	if m == nil {
		return 0, 0, 0
	}
	adds, _ = strconv.Atoi(m[1])
	changes, _ = strconv.Atoi(m[2])
	destroys, _ = strconv.Atoi(m[3])
	return adds, changes, destroys
}

// Warning is one warning diagnostic Terraform printed, split into its
// one-line headline and the full detail that followed it (source
// location and explanation, exactly as Terraform wrapped and indented
// it) — see parseWarnings.
type Warning struct {
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"` // may be empty; multi-line when present
}

// diagnosticHeader matches the first line of any Terraform diagnostic in
// `-no-color` output (the flag terraform-exec itself always passes) —
// e.g. "Warning: Argument is deprecated" or "Error: Invalid value".
// Terraform's diagnostic renderer starts every diagnostic with exactly
// this "<Severity>: <summary>" at the start of a line, with no leading
// indentation.
var diagnosticHeader = regexp.MustCompile(`^(Warning|Error): (.*)$`)

// diagnosticDivider matches the horizontal-rule line
// ("───────────────────────────────") Terraform prints immediately after
// its diagnostics section, once, before whatever trailing text follows
// (e.g. "Saved the plan to: ...") — captured from a real
// `terraform plan -no-color -out=...` run. Confirms a diagnostic's detail
// paragraph(s) are finished without having to guess at how many blank-
// line-separated paragraphs a given diagnostic has (a diagnostic's
// summary, source-location snippet, and detail text are themselves
// blank-line separated, so "the next blank line" alone can't mark the
// end).
var diagnosticDivider = regexp.MustCompile(`^─+$`)

// parseWarnings extracts every warning diagnostic out of output — its
// summary headline plus the full detail text that followed it — in the
// order Terraform printed them. Error diagnostics are recognized (so a
// warning immediately preceding one isn't misread as continuing into
// it) but not themselves returned; PlanResult.Err already carries
// whatever error text matters.
func parseWarnings(output string) []Warning {
	var warnings []Warning
	var current *Warning
	var detail []string

	finalize := func() {
		if current == nil {
			return
		}
		start, end := 0, len(detail)
		for start < end && detail[start] == "" {
			start++
		}
		for end > start && detail[end-1] == "" {
			end--
		}
		current.Detail = strings.Join(detail[start:end], "\n")
		warnings = append(warnings, *current)
		current = nil
		detail = nil
	}

	for _, line := range strings.Split(output, "\n") {
		switch {
		case diagnosticHeader.MatchString(line):
			finalize()
			m := diagnosticHeader.FindStringSubmatch(line)
			if m[1] == "Warning" {
				current = &Warning{Summary: m[2]}
			}
		case diagnosticDivider.MatchString(line):
			finalize()
		case current != nil:
			detail = append(detail, line)
		}
	}
	finalize()
	return warnings
}

// teeWriter returns buf alone if no live writer was given, or an
// io.MultiWriter that writes to both — always wrapped in a mutex.
// terraform-exec reads a command's stdout and stderr on separate
// goroutines, and both get funneled into this same writer (Plan/Apply/
// MigrateBackend all call SetStdout and SetStderr with the same
// teeWriter result), so without synchronization here, concurrent
// stdout+stderr output is a real data race on the underlying
// bytes.Buffer — not hypothetical: it's what TestMigrateBackend_
// FailedMigrationRollsBackStub's real terraform error caught.
func teeWriter(buf *bytes.Buffer, liveOut []io.Writer) io.Writer {
	var w io.Writer = buf
	if len(liveOut) > 0 && liveOut[0] != nil {
		w = io.MultiWriter(buf, liveOut[0])
	}
	return &syncWriter{w: w}
}

// syncWriter serializes concurrent Write calls to an underlying
// io.Writer that isn't safe for concurrent use on its own (bytes.Buffer,
// or an io.MultiWriter built over one).
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// wrapWithOutput wraps err with buf's accumulated terraform output —
// the standard shape of every error Apply/Plan return, so the caller
// sees the actual terraform output alongside the Go error, not just
// one or the other.
func wrapWithOutput(err error, buf *bytes.Buffer) error {
	return fmt.Errorf("%w\n%s", err, buf.String())
}

// applySavedPlan applies planFile (DirOrPlan) and removes it on
// success — the two places Apply below reach this same "apply exactly
// what was already saved" step: once for a plan file left over from a
// prior Plan call, once for one just computed fresh a few lines above.
func applySavedPlan(ctx context.Context, tf *tfexec.Terraform, planFile string, buf *bytes.Buffer) error {
	if err := tf.Apply(ctx, tfexec.DirOrPlan(planFile)); err != nil {
		return wrapWithOutput(err, buf)
	}
	os.Remove(planFile)
	return nil
}

func summarize(plan *tfjson.Plan, adds, changes, destroys *int) []ResourceChange {
	var out []ResourceChange
	for _, rc := range plan.ResourceChanges {
		action := actionOf(rc.Change.Actions)
		if action == "no-op" {
			continue
		}
		switch action {
		case "create":
			*adds++
		case "delete":
			*destroys++
		default: // update, replace
			*changes++
		}

		change := ResourceChange{Address: rc.Address, Action: action}
		if action == "update" || action == "replace" {
			change.Attributes = diffAttributes(
				rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown,
				rc.Change.BeforeSensitive, rc.Change.AfterSensitive,
			)
		}
		out = append(out, change)
	}
	return out
}

func actionOf(actions tfjson.Actions) string {
	switch {
	case actions.NoOp():
		return "no-op"
	case actions.Create():
		return "create"
	case actions.Delete():
		return "delete"
	case actions.Replace():
		return "replace"
	case actions.Update():
		return "update"
	default:
		return "unknown"
	}
}

// Apply runs `terraform apply` against planFile if it exists (the saved
// output of a prior Plan call — see Plan's doc comment for why that path
// lives outside dir), else falls back to a fresh interactive-free apply.
// The plan file is removed after a successful apply: once consumed, a
// stale saved plan is more likely to mislead a future run than help it.
// liveOut works exactly as in Plan.
// UnsavedPlanConfirm is called by Apply when dir's backend can't save a
// generated plan at all (see PlanResult.UnsavedPlan and Apply's own doc
// comment) — planText is a freshly computed plan's complete
// human-readable output, produced immediately before this call, for
// the caller to actually show someone before deciding whether to
// proceed. Return true to apply exactly that diff, false to skip this
// directory instead. A nil UnsavedPlanConfirm (passed as nil to Apply)
// means always skip — the only safe default for a caller that has no
// way to actually ask anyone, like the web server; see
// orchestrate.DefaultUnsavedPlanHandler.
type UnsavedPlanConfirm func(dir, planText string) bool

// Apply applies planFile if it exists — the normal case, applying
// exactly the plan a prior Plan call already saved and that was already
// reviewed. If planFile doesn't exist, Apply computes one fresh first
// rather than ever applying blind: for a directory nobody has run Plan
// against yet, this behaves exactly like Plan-then-Apply would (no
// confirmation needed — the freshly saved plan IS what gets applied,
// same guarantee as the normal case, just computed just-in-time). Only
// if that fresh Plan itself fails with "this backend can't save a
// generated plan" (see isSavedPlanUnsupported) does this reach
// confirmUnsaved: a directory in that state re-plans once more, plain
// this time, and requires confirmUnsaved to approve that exact diff
// before applying it — there's no saved plan to silently trust here,
// because there never was one to review, so the reviewing has to
// happen right now, immediately before applying, not earlier. A nil
// confirmUnsaved (or one that returns false) means this directory is
// skipped with a clear error rather than ever applied unreviewed.
func (r *Runner) Apply(ctx context.Context, repoRoot, dir, planFile string, confirmUnsaved UnsavedPlanConfirm, liveOut ...io.Writer) error {
	tf, err := r.newClient(repoRoot, dir)
	if err != nil {
		return err
	}
	// Needed before the "no saved plan file" branch below can try
	// saving one fresh — Plan() always does this same MkdirAll first,
	// but a caller can reach Apply directly (e.g. running `apply`
	// without ever running `plan` first) without that having happened
	// yet.
	if err := os.MkdirAll(filepath.Dir(planFile), 0o755); err != nil {
		return fmt.Errorf("creating directory for plan file: %w", err)
	}
	var buf bytes.Buffer
	stdout := teeWriter(&buf, liveOut)
	tf.SetStdout(stdout)
	tf.SetStderr(stdout)

	if _, err := os.Stat(planFile); err == nil {
		return applySavedPlan(ctx, tf, planFile, &buf)
	}

	// No saved plan file — try to get one fresh. For almost every
	// directory this is the ordinary "apply was run without planning
	// first" case, and it works exactly like a normal Plan followed
	// immediately by Apply from that same file: no confirmation needed,
	// since the freshly saved plan IS what gets applied.
	hasChanges, err := tf.Plan(ctx, tfexec.Out(planFile))
	if err == nil {
		if !hasChanges {
			os.Remove(planFile)
			return nil
		}
		return applySavedPlan(ctx, tf, planFile, &buf)
	}
	if !isSavedPlanUnsupported(err) {
		return wrapWithOutput(err, &buf)
	}

	// This directory's backend genuinely can't save a plan at all —
	// re-plan once more, plain this time, and require an explicit,
	// fresh confirmation of exactly this diff before applying it.
	buf.Reset()
	hasChanges, err = tf.Plan(ctx)
	planText := buf.String()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, planText)
	}
	if !hasChanges {
		return nil
	}
	if confirmUnsaved == nil || !confirmUnsaved(dir, planText) {
		return fmt.Errorf("apply skipped: this backend can't save a plan for review, and applying it needs an explicit confirmation this caller can't give — run \"homelab-deploy %s apply\" from the CLI instead", dir)
	}
	buf.Reset()
	if err := tf.Apply(ctx); err != nil {
		return wrapWithOutput(err, &buf)
	}
	return nil
}
