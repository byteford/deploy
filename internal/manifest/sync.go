package manifest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SyncResult is one entry's outcome from Sync.
type SyncResult struct {
	Name   string
	Action string // "cloned", "updated", "local (skipped)"
	Err    error
}

// Sync clones any git entry that isn't checked out yet, and updates any
// that is — fast-forward only. A dirty working tree, or local commits
// that can't fast-forward onto the target ref, both come back as errors
// for that entry rather than ever being force-overwritten or discarded;
// resolving them is left to the person, on purpose. Local-path entries
// are never touched at all. Every entry is attempted and reported even
// if an earlier one failed — one repo's clone failure shouldn't block
// updating everything else, unlike apply's dependency-ordered
// stop-on-failure (sync has no dependency order to respect; the
// dependency graph only matters once terraform runs).
//
// liveWriterFor, if non-nil, is called once per entry to get an
// io.Writer that receives that entry's git output as it's produced —
// e.g. the web dashboard routes each repo to its own live pane. A nil
// liveWriterFor (or a nil writer returned for a given entry) means no
// live output for that entry, matching Sync's original single-writer
// behavior for any existing caller that doesn't need per-entry routing.
func Sync(ctx context.Context, m *Manifest, liveWriterFor func(name string) io.Writer) []SyncResult {
	var results []SyncResult
	for _, name := range m.Order {
		e := m.Entries[name]
		if !e.IsGit {
			results = append(results, SyncResult{Name: name, Action: "local (skipped)"})
			continue
		}
		var liveOut io.Writer
		if liveWriterFor != nil {
			liveOut = liveWriterFor(name)
		}
		action, err := syncOne(ctx, e, liveOut)
		results = append(results, SyncResult{Name: name, Action: action, Err: err})
	}
	return results
}

func syncOne(ctx context.Context, e Entry, liveOut io.Writer) (action string, err error) {
	if _, statErr := os.Stat(e.LocalPath); os.IsNotExist(statErr) {
		if err := runGit(ctx, "", liveOut, "clone", "--quiet", e.URL, e.LocalPath); err != nil {
			return "", fmt.Errorf("cloning: %w", err)
		}
		if e.Ref != "" {
			if err := runGit(ctx, e.LocalPath, liveOut, "checkout", "--quiet", e.Ref); err != nil {
				return "", fmt.Errorf("checking out %s: %w", e.Ref, err)
			}
		}
		if err := ensureLocalExclude(e.LocalPath); err != nil {
			return "", fmt.Errorf("setting up local excludes: %w", err)
		}
		return "cloned", nil
	}

	// Idempotent and cheap — also covers a repo cloned before this
	// exclude list existed, or one where .git/info/exclude was lost
	// some other way. Doing this BEFORE the dirty-check below matters:
	// if terraform has already run here and left untracked
	// terraform.tfstate/.terraform behind, adding the exclude now makes
	// `git status --porcelain` stop seeing them immediately, unstuck-ing
	// a repo that would otherwise look permanently dirty.
	if err := ensureLocalExclude(e.LocalPath); err != nil {
		return "", fmt.Errorf("setting up local excludes: %w", err)
	}

	status, err := gitOutput(ctx, e.LocalPath, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("checking status: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return "", fmt.Errorf("has uncommitted changes — resolve, commit, or stash them before syncing:\n%s", status)
	}

	if err := runGit(ctx, e.LocalPath, liveOut, "fetch", "--quiet", "origin"); err != nil {
		return "", fmt.Errorf("fetching: %w", err)
	}

	target := e.Ref
	if target == "" {
		out, err := gitOutput(ctx, e.LocalPath, "rev-parse", "--abbrev-ref", "origin/HEAD")
		if err != nil {
			return "", fmt.Errorf("determining the default branch: %w", err)
		}
		target = strings.TrimPrefix(strings.TrimSpace(out), "origin/")
	}

	if err := runGit(ctx, e.LocalPath, liveOut, "checkout", "--quiet", target); err != nil {
		return "", fmt.Errorf("checking out %s: %w", target, err)
	}

	// If origin/<target> exists as a remote-tracking branch, fast-forward
	// onto it — git's own --ff-only refuses (rather than us needing to
	// reimplement divergence detection) if the local branch has commits
	// origin doesn't, which is exactly the "don't discard anything, make
	// the person sort it out" behavior wanted here. A tag or a raw commit
	// SHA has no corresponding origin/<target> at all — the checkout
	// above already landed on that exact, immutable commit, so there's
	// nothing further to fast-forward.
	if _, err := gitOutput(ctx, e.LocalPath, "rev-parse", "--verify", "origin/"+target); err == nil {
		if err := runGit(ctx, e.LocalPath, liveOut, "merge", "--ff-only", "origin/"+target); err != nil {
			return "", fmt.Errorf("local branch has diverged from origin/%s and can't fast-forward — resolve manually: %w", target, err)
		}
	}
	return "updated", nil
}

// terraformExcludePatterns are Terraform's own working-directory
// artifacts — plugin cache, local state, crash logs. Every one of these
// matches anywhere in the tree (no leading "/"), which is what makes
// this correct regardless of whether a repo's tf root is nested in a
// TFRoot subpath or sits at the repo's own top level.
var terraformExcludePatterns = []string{
	".terraform/",
	"terraform.tfstate",
	"terraform.tfstate.backup",
	"*.tfstate.backup",
	"crash.log",
}

// ensureLocalExclude adds terraformExcludePatterns to this repo's LOCAL
// git excludes (.git/info/exclude) — deliberately not the repo's own
// tracked .gitignore, which isn't ours to modify and might already
// differ from what a person wants committed there. Without this,
// running terraform in a cloned repo leaves untracked files behind
// (terraform.tfstate, .terraform/), which makes `git status --porcelain`
// report the tree as dirty — and dirty-tree detection is supposed to
// mean "the person changed something," not "terraform did its normal
// job here." Idempotent: only appends patterns not already present.
func ensureLocalExclude(localPath string) error {
	excludePath := filepath.Join(localPath, ".git", "info", "exclude")
	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var toAdd []string
	for _, p := range terraformExcludePatterns {
		if !strings.Contains(string(existing), p) {
			toAdd = append(toAdd, p)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}

	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		fmt.Fprintln(f)
	}
	fmt.Fprintln(f, "# Added by homelab-deploy: Terraform's own working-directory")
	fmt.Fprintln(f, "# artifacts, excluded locally (not in this repo's own .gitignore) so")
	fmt.Fprintln(f, "# running terraform here doesn't make `sync` think the tree is dirty.")
	for _, p := range toAdd {
		fmt.Fprintln(f, p)
	}
	return nil
}

func runGit(ctx context.Context, dir string, liveOut io.Writer, args ...string) error {
	fullArgs := args
	if dir != "" {
		fullArgs = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Env = noPromptEnv()
	var buf bytes.Buffer
	var out io.Writer = &buf
	if liveOut != nil {
		out = io.MultiWriter(&buf, liveOut)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, buf.String())
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Env = noPromptEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

// noPromptEnv is this process's environment plus GIT_TERMINAL_PROMPT=0,
// which tells git itself to never prompt for HTTPS credentials. sync
// can run unattended (the web dashboard's Sync button, a cron job) with
// nobody there to type a password, so a private HTTPS repo with no
// credential helper configured must fail with a clear error rather
// than hang forever waiting for input that will never come.
//
// This only covers git's OWN prompting — it does not set
// GIT_SSH_COMMAND, deliberately: doing so would silently override a
// deploy-key setup someone configures via the [env] section of their
// .env/.env.shared (e.g. GIT_SSH_COMMAND=ssh -i /path/to/key). SSH's
// own credential prompting is a separate program's behavior, not
// something this sets explicitly — it relies on the same mechanism
// already verified for the HTTPS case: every git subprocess here gets
// a non-readable stdin (Go leaves Cmd.Stdin as /dev/null when unset),
// and both git and OpenSSH treat a non-interactive stdin as "don't
// prompt, just fail" rather than hanging. Verified directly for the
// HTTPS path: a real `git clone` against an inaccessible URL failed in
// ~6s with "could not read Username ... No such device or address"
// rather than hanging.
func noPromptEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}
