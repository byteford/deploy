package graph

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"homelab-deploy/internal/manifest"
)

// ManualStep describes what's needed before a directory can be applied.
// Satisfied is true when a .manual-check script exited 0 (step already
// done, no pause needed). CheckOutput/CheckErr surface *why*, mirroring
// the bash version's "show the check's output even on unexpected failure"
// behavior — a bad shebang or CRLF line endings shouldn't fail silently.
type ManualStep struct {
	Dir          string
	HasCheck     bool
	CheckExists  bool
	Satisfied    bool
	CheckOutput  string
	CheckErr     error
	Instructions string // contents of .manual-before, empty if absent
}

// Evaluate runs a directory's .manual-check (if present and executable)
// and reads its .manual-before instructions (if present). It does not
// block for input — callers decide how/whether to pause based on the
// result, which makes this testable and reusable outside a CLI.
// resolver may be nil (single-repo mode); see manifest.Resolver.Dir for
// why this uses Dir specifically, not WorkDir — these are orchestration
// metadata files describing the repo as a whole, not Terraform config.
func Evaluate(resolver *manifest.Resolver, repoRoot, dir string) (ManualStep, error) {
	ms := ManualStep{Dir: dir}
	base := resolveDir(resolver, repoRoot, dir)

	checkPath := filepath.Join(base, ".manual-check")
	if info, err := os.Stat(checkPath); err == nil {
		ms.HasCheck = true
		if info.Mode()&0111 == 0 {
			ms.CheckErr = fmt.Errorf("%s exists but isn't executable (chmod +x it)", checkPath)
		} else {
			cmd := exec.Command(checkPath)
			cmd.Dir = base
			var buf bytes.Buffer
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			runErr := cmd.Run()
			ms.CheckOutput = buf.String()
			if runErr == nil {
				ms.Satisfied = true
			} else {
				ms.CheckErr = runErr
			}
		}
	} else if !os.IsNotExist(err) {
		return ms, err
	}

	beforePath := filepath.Join(base, ".manual-before")
	if data, err := os.ReadFile(beforePath); err == nil {
		ms.Instructions = string(data)
	} else if !os.IsNotExist(err) {
		return ms, err
	}

	return ms, nil
}

// resolveDir is the graph package's one seam for turning (repoRoot, dir)
// into a real path — resolver may be nil (single-repo mode).
func resolveDir(resolver *manifest.Resolver, repoRoot, dir string) string {
	if resolver != nil {
		return resolver.Dir(repoRoot, dir)
	}
	return filepath.Join(repoRoot, dir)
}

// PromptIfNeeded prints the manual-step status and, if instructions exist
// and the check (if any) wasn't satisfied, blocks on Enter — same
// contract as the original script's maybe_pause_for_manual_step.
func PromptIfNeeded(ms ManualStep, stdin *bufio.Reader) {
	if ms.HasCheck {
		if ms.CheckErr != nil && ms.CheckOutput == "" {
			fmt.Printf("warning: %v\n", ms.CheckErr)
		} else if ms.Satisfied {
			fmt.Printf("==> manual step for %s already satisfied, skipping pause\n", ms.Dir)
			if ms.CheckOutput != "" {
				printIndented(ms.CheckOutput)
			}
			return
		} else {
			fmt.Printf("==> manual step for %s not yet satisfied\n", ms.Dir)
			if ms.CheckOutput != "" {
				fmt.Println("    check output:")
				printIndented(ms.CheckOutput)
			} else {
				fmt.Println("    (no output from the check — verify the shebang and that it's not saved with Windows CRLF line endings)")
			}
		}
	}

	if ms.Instructions != "" {
		fmt.Println()
		fmt.Printf("---- manual step required before %s ----\n", ms.Dir)
		fmt.Print(ms.Instructions)
		fmt.Println()
		fmt.Print("Press enter once done to continue... ")
		_, _ = stdin.ReadString('\n')
	}
}

func printIndented(s string) {
	sc := bufio.NewScanner(bytes.NewBufferString(s))
	for sc.Scan() {
		fmt.Printf("    %s\n", sc.Text())
	}
}
