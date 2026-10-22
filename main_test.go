package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"homelab-deploy/internal/tf"
)

func TestParseFlags_Defaults(t *testing.T) {
	gf, remaining, err := parseFlags([]string{"plan"})
	if err != nil {
		t.Fatal(err)
	}
	if gf.workingDir != "" || gf.envFile != "" || gf.verbose || gf.timeout != 0 {
		t.Errorf("expected zero-value defaults, got %+v", gf)
	}
	if len(remaining) != 1 || remaining[0] != "plan" {
		t.Errorf("remaining = %v, want [plan]", remaining)
	}
}

func TestParseFlags_WorkingDirLongForm(t *testing.T) {
	gf, remaining, err := parseFlags([]string{"--working-dir", "/repo", "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if gf.workingDir != "/repo" {
		t.Errorf("workingDir = %q, want /repo", gf.workingDir)
	}
	if len(remaining) != 1 || remaining[0] != "plan" {
		t.Errorf("remaining = %v, want [plan]", remaining)
	}
}

func TestParseFlags_WorkingDirShorthand(t *testing.T) {
	// -C is the shorthand, matching git -C / make -C convention.
	gf, _, err := parseFlags([]string{"-C", "/repo", "apply"})
	if err != nil {
		t.Fatal(err)
	}
	if gf.workingDir != "/repo" {
		t.Errorf("workingDir = %q, want /repo (via -C)", gf.workingDir)
	}
}

func TestParseFlags_WorkingDirEqualsForm(t *testing.T) {
	gf, _, err := parseFlags([]string{"--working-dir=/repo", "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if gf.workingDir != "/repo" {
		t.Errorf("workingDir = %q, want /repo", gf.workingDir)
	}
}

func TestParseFlags_EnvFile(t *testing.T) {
	gf, _, err := parseFlags([]string{"--env-file", "/etc/homelab-deploy.env", "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if gf.envFile != "/etc/homelab-deploy.env" {
		t.Errorf("envFile = %q, want /etc/homelab-deploy.env", gf.envFile)
	}
}

func TestParseFlags_VerboseLongAndShort(t *testing.T) {
	for _, flagArg := range []string{"--verbose", "-v"} {
		gf, _, err := parseFlags([]string{flagArg, "plan"})
		if err != nil {
			t.Fatal(err)
		}
		if !gf.verbose {
			t.Errorf("%s: verbose = false, want true", flagArg)
		}
	}
}

func TestParseFlags_Timeout(t *testing.T) {
	gf, _, err := parseFlags([]string{"--timeout", "10m", "apply"})
	if err != nil {
		t.Fatal(err)
	}
	if gf.timeout != 10*time.Minute {
		t.Errorf("timeout = %v, want 10m", gf.timeout)
	}
}

func TestParseFlags_InvalidTimeoutErrors(t *testing.T) {
	_, _, err := parseFlags([]string{"--timeout", "not-a-duration", "plan"})
	if err == nil {
		t.Fatal("expected an error for an invalid duration")
	}
}

func TestParseFlags_MultipleFlagsCombined(t *testing.T) {
	gf, remaining, err := parseFlags([]string{
		"--working-dir", "/repo",
		"--env-file", "/repo/.env.prod",
		"--verbose",
		"--timeout", "5m",
		"apply",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gf.workingDir != "/repo" || gf.envFile != "/repo/.env.prod" || !gf.verbose || gf.timeout != 5*time.Minute {
		t.Errorf("unexpected parsed flags: %+v", gf)
	}
	if len(remaining) != 1 || remaining[0] != "apply" {
		t.Errorf("remaining = %v, want [apply]", remaining)
	}
}

func TestParseFlags_RemainingArgsPreservesCommandArgs(t *testing.T) {
	// e.g. `homelab-deploy -v services/foo apply` — the subcommand's own
	// arguments (the directory, "apply") must survive intact in order.
	gf, remaining, err := parseFlags([]string{"-v", "services/foo", "apply"})
	if err != nil {
		t.Fatal(err)
	}
	if !gf.verbose {
		t.Error("expected verbose=true")
	}
	if len(remaining) != 2 || remaining[0] != "services/foo" || remaining[1] != "apply" {
		t.Errorf("remaining = %v, want [services/foo apply]", remaining)
	}
}

func TestParseFlags_FlagsAfterSubcommandAreParsed(t *testing.T) {
	// pflag parses flags and positional args in an interspersed fashion,
	// so `homelab-deploy plan --verbose` works the same as
	// `homelab-deploy --verbose plan` — pinning this down so a future
	// refactor doesn't quietly regress back to the old stdlib-flag
	// behavior.
	gf, remaining, err := parseFlags([]string{"plan", "--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if !gf.verbose {
		t.Error("flags after the subcommand must be parsed as global flags")
	}
	if len(remaining) != 1 || remaining[0] != "plan" {
		t.Errorf("remaining = %v, want [plan]", remaining)
	}
}

func TestParseFlags_FlagAfterDirArg(t *testing.T) {
	// e.g. `homelab-deploy services/foo apply --verbose` — a flag after
	// the directory and command args must still be picked up.
	gf, remaining, err := parseFlags([]string{"services/foo", "apply", "--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if !gf.verbose {
		t.Error("expected verbose=true")
	}
	if len(remaining) != 2 || remaining[0] != "services/foo" || remaining[1] != "apply" {
		t.Errorf("remaining = %v, want [services/foo apply]", remaining)
	}
}

func TestParseFlags_HelpReturnsErrHelp(t *testing.T) {
	_, _, err := parseFlags([]string{"--help"})
	if !errors.Is(err, pflag.ErrHelp) {
		t.Errorf("expected pflag.ErrHelp, got %v", err)
	}
}

func TestParseFlags_UnknownFlagErrors(t *testing.T) {
	_, _, err := parseFlags([]string{"--not-a-real-flag", "plan"})
	if err == nil {
		t.Fatal("expected an error for an unknown flag")
	}
	if errors.Is(err, pflag.ErrHelp) {
		t.Error("an unknown flag should be a real error, not pflag.ErrHelp")
	}
}

func TestParseFlags_NoArgsAtAll(t *testing.T) {
	gf, remaining, err := parseFlags([]string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("remaining = %v, want empty", remaining)
	}
	if gf.workingDir != "" {
		t.Errorf("expected default workingDir, got %q", gf.workingDir)
	}
}

func TestParseFlags_SharedEnvFile(t *testing.T) {
	gf, _, err := parseFlags([]string{"--shared-env-file", "/repo/.env.shared", "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if gf.sharedEnvFile != "/repo/.env.shared" {
		t.Errorf("sharedEnvFile = %q, want /repo/.env.shared", gf.sharedEnvFile)
	}
}

func TestParseFlags_MigrateBackend(t *testing.T) {
	gf, remaining, err := parseFlags([]string{"--migrate-backend", "init"})
	if err != nil {
		t.Fatal(err)
	}
	if !gf.migrateBackend {
		t.Error("expected migrateBackend = true")
	}
	if len(remaining) != 1 || remaining[0] != "init" {
		t.Errorf("remaining = %v, want [init]", remaining)
	}
}

func TestParseFlags_MigrateBackendWithDirTarget(t *testing.T) {
	gf, remaining, err := parseFlags([]string{"--migrate-backend", "services/a", "init"})
	if err != nil {
		t.Fatal(err)
	}
	if !gf.migrateBackend {
		t.Error("expected migrateBackend = true")
	}
	if len(remaining) != 2 || remaining[0] != "services/a" || remaining[1] != "init" {
		t.Errorf("remaining = %v, want [services/a init]", remaining)
	}
}

func TestParseFlags_AllFlagsIncludingBackendOnes(t *testing.T) {
	gf, remaining, err := parseFlags([]string{
		"-C", "/repo",
		"--env-file", "/repo/.env",
		"--shared-env-file", "/repo/.env.shared",
		"--migrate-backend",
		"init",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gf.workingDir != "/repo" || gf.envFile != "/repo/.env" || gf.sharedEnvFile != "/repo/.env.shared" || !gf.migrateBackend {
		t.Errorf("unexpected parsed flags: %+v", gf)
	}
	if len(remaining) != 1 || remaining[0] != "init" {
		t.Errorf("remaining = %v, want [init]", remaining)
	}
}

func TestConfirmMigration_TypingYesConfirms(t *testing.T) {
	var out bytes.Buffer
	backend := tf.BackendConfig{Type: "s3", Settings: map[string]string{"bucket": "my-bucket", "key": "{dir}.tfstate"}}

	confirmed, err := confirmMigration(&out, strings.NewReader("yes\n"), backend, []string{"services/a"})
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Error("typing 'yes' should confirm")
	}
}

func TestConfirmMigration_AnythingElseDeclines(t *testing.T) {
	var out bytes.Buffer
	backend := tf.BackendConfig{Type: "s3", Settings: map[string]string{"key": "{dir}.tfstate"}}

	for _, input := range []string{"y\n", "Yes\n", "YES\n", "no\n", "\n", "sure\n"} {
		confirmed, err := confirmMigration(&out, strings.NewReader(input), backend, []string{"services/a"})
		if err != nil {
			t.Fatal(err)
		}
		if confirmed {
			t.Errorf("input %q should NOT confirm — only the exact literal word \"yes\" should", input)
		}
	}
}

func TestConfirmMigration_ShowsResolvedSettingsPerDirectory(t *testing.T) {
	// The preview must show the ACTUAL resolved value for each
	// directory (with {dir} substituted), not the raw template — that's
	// what makes the confirmation meaningful rather than just showing
	// back the config the person already wrote.
	var out bytes.Buffer
	backend := tf.BackendConfig{Type: "s3", Settings: map[string]string{"bucket": "my-bucket", "key": "{dir}.tfstate"}}

	confirmMigration(&out, strings.NewReader("no\n"), backend, []string{"services/authentik", "config/vault"})

	preview := out.String()
	for _, want := range []string{
		"s3",
		"services/authentik",
		"services/authentik.tfstate", // resolved, not the "{dir}.tfstate" template
		"config/vault",
		"config/vault.tfstate",
		"my-bucket",
	} {
		if !strings.Contains(preview, want) {
			t.Errorf("preview missing %q:\n%s", want, preview)
		}
	}
	if strings.Contains(preview, "{dir}") {
		t.Errorf("preview should show resolved values, not the raw {dir} template:\n%s", preview)
	}
}

func TestConfirmMigration_EOFOnStdinDeclinesRatherThanErrors(t *testing.T) {
	var out bytes.Buffer
	backend := tf.BackendConfig{Type: "s3", Settings: map[string]string{"key": "{dir}.tfstate"}}

	confirmed, err := confirmMigration(&out, strings.NewReader(""), backend, []string{"services/a"})
	if err != nil {
		t.Fatalf("EOF on stdin should decline, not error: %v", err)
	}
	if confirmed {
		t.Error("EOF on stdin should never be treated as confirmation")
	}
}

func TestSortedKeys(t *testing.T) {
	got := sortedKeys(map[string]string{"z": "1", "a": "2", "m": "3"})
	want := []string{"a", "m", "z"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}

func TestConfirmUnsavedPlanCLI_TypingYesConfirms(t *testing.T) {
	var out bytes.Buffer
	confirm := confirmUnsavedPlanCLI(bufio.NewReader(strings.NewReader("yes\n")))
	// confirmUnsavedPlanCLI prints via fmt.Print* (stdout), not an
	// injected writer, so redirect stdout for the duration of this call
	// to capture what a person would actually see.
	restore := redirectStdout(t, &out)
	confirmed := confirm("services/tfc", "Plan: 1 to add, 0 to change, 0 to destroy.")
	restore()

	if !confirmed {
		t.Error("typing 'yes' should confirm")
	}
	if !strings.Contains(out.String(), "services/tfc") {
		t.Errorf("prompt should name the directory, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "1 to add") {
		t.Errorf("prompt should show the actual plan text, got: %q", out.String())
	}
}

func TestConfirmUnsavedPlanCLI_AnythingElseDeclines(t *testing.T) {
	for _, input := range []string{"y\n", "Yes\n", "YES\n", "no\n", "\n"} {
		var out bytes.Buffer
		confirm := confirmUnsavedPlanCLI(bufio.NewReader(strings.NewReader(input)))
		restore := redirectStdout(t, &out)
		confirmed := confirm("services/tfc", "Plan: 1 to add, 0 to change, 0 to destroy.")
		restore()
		if confirmed {
			t.Errorf("input %q should NOT confirm — only the exact literal word \"yes\" should", input)
		}
	}
}

// redirectStdout temporarily points os.Stdout at w, returning a func
// that restores it — used because confirmUnsavedPlanCLI (like
// confirmMigration's own printing style elsewhere in this file) writes
// straight to fmt.Print*, not an injected io.Writer.
func redirectStdout(t *testing.T, w *bytes.Buffer) func() {
	t.Helper()
	r, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = pw
	done := make(chan struct{})
	go func() {
		io.Copy(w, r)
		close(done)
	}()
	return func() {
		pw.Close()
		os.Stdout = old
		<-done
	}
}

func TestClassifyDirArg_RealDirectoryIsNil(t *testing.T) {
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := classifyDirArg("services/a", info, nil); err != nil {
		t.Errorf("expected nil for a real directory, got %v", err)
	}
}

func TestClassifyDirArg_FileNotDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got := classifyDirArg("services/a", info, nil)
	if got == nil || !strings.Contains(got.Error(), "not a directory") {
		t.Errorf("expected a 'not a directory' error, got %v", got)
	}
}

func TestClassifyDirArg_MissingPathLikeArgNamesTheDirectory(t *testing.T) {
	_, statErr := os.Stat(filepath.Join(t.TempDir(), "does-not-exist"))
	got := classifyDirArg("services/typo", nil, statErr)
	if got == nil || !strings.Contains(got.Error(), "no such directory") || !strings.Contains(got.Error(), "services/typo") {
		t.Errorf("expected a 'no such directory' error naming the arg, got %v", got)
	}
	if !errors.Is(got, statErr) {
		t.Errorf("expected the original stat error to be wrapped, got %v", got)
	}
}

func TestClassifyDirArg_TypoedCommandGetsUsageText(t *testing.T) {
	_, statErr := os.Stat(filepath.Join(t.TempDir(), "does-not-exist"))
	got := classifyDirArg("plna", nil, statErr)
	if got == nil || !strings.Contains(got.Error(), `unknown command "plna"`) {
		t.Errorf("expected an 'unknown command' error, got %v", got)
	}
	if !strings.Contains(got.Error(), "commands:") {
		t.Errorf("expected the usage text to follow, got %v", got)
	}
}
