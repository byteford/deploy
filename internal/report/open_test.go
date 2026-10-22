package report

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeExe creates an executable shell script at dir/name that runs
// script — a stand-in for a real opener binary (wslview, cmd.exe, ...)
// so tests can verify the actual exec.Command invocation happened
// (arguments and all) without needing that binary, or that OS, for
// real.
func writeFakeExe(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func notFoundLookPath(string) (string, error) { return "", exec.ErrNotFound }

func TestOpenWith_WSLPrefersWslview(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked.txt")
	wslview := writeFakeExe(t, dir, "wslview", `printf '%s\n' "$*" > `+marker)

	lookPath := func(name string) (string, error) {
		if name == "wslview" {
			return wslview, nil
		}
		return "", exec.ErrNotFound
	}
	toWinPath := func(string) (string, error) {
		t.Fatal("toWinPath should not be consulted when wslview is available")
		return "", nil
	}

	if err := openWith("/tmp/report.html", true, "linux", lookPath, toWinPath); err != nil {
		t.Fatalf("openWith returned %v, want nil", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("wslview was never invoked: %v", err)
	}
	if strings.TrimSpace(string(got)) != "/tmp/report.html" {
		t.Errorf("wslview invoked with %q, want the abs path", got)
	}
}

func TestOpenWith_WSLFallsBackToCmdExeWhenNoWslview(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked.txt")
	cmdExe := writeFakeExe(t, dir, "cmd.exe", `printf '%s\n' "$*" > `+marker)

	lookPath := func(name string) (string, error) {
		if name == "cmd.exe" {
			return cmdExe, nil
		}
		return "", exec.ErrNotFound
	}
	toWinPath := func(string) (string, error) { return `C:\fake\report.html`, nil }

	if err := openWith("/tmp/report.html", true, "linux", lookPath, toWinPath); err != nil {
		t.Fatalf("openWith returned %v, want nil", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("cmd.exe was never invoked: %v", err)
	}
	// Args are "/c", "start", "", winPath — the empty string is the
	// `start` command's window-title placeholder, hence the double space.
	want := `/c start  C:\fake\report.html`
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("cmd.exe invoked with %q, want %q", strings.TrimSpace(string(got)), want)
	}
}

func TestOpenWith_WSLFallsBackToExplorerAndIgnoresItsExitCode(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked.txt")
	explorer := writeFakeExe(t, dir, "explorer.exe", `printf '%s\n' "$*" > `+marker+"\nexit 1")

	lookPath := func(name string) (string, error) {
		if name == "explorer.exe" {
			return explorer, nil
		}
		return "", exec.ErrNotFound
	}
	toWinPath := func(string) (string, error) { return `C:\fake\report.html`, nil }

	err := openWith("/tmp/report.html", true, "linux", lookPath, toWinPath)
	if err != nil {
		t.Fatalf("openWith returned %v, want nil (explorer.exe's exit code must be ignored)", err)
	}
	got, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("explorer.exe was never invoked: %v", rerr)
	}
	if strings.TrimSpace(string(got)) != `C:\fake\report.html` {
		t.Errorf("explorer.exe invoked with %q, want the windows path", got)
	}
}

func TestOpenWith_WSLWithNoWindowsIntegrationReturnsErrNoOpenerWhenLinuxOpenersAreAlsoMissing(t *testing.T) {
	toWinPath := func(string) (string, error) { return "", errors.New("wslpath not found") }

	err := openWith("/tmp/report.html", true, "linux", notFoundLookPath, toWinPath)
	if !errors.Is(err, errNoOpener) {
		t.Errorf("expected errNoOpener, got %v", err)
	}
}

func TestOpenWith_WSLFallsThroughToXdgOpenWhenNoWindowsIntegration(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked.txt")
	xdgOpen := writeFakeExe(t, dir, "xdg-open", `printf '%s\n' "$*" > `+marker)

	lookPath := func(name string) (string, error) {
		if name == "xdg-open" {
			return xdgOpen, nil
		}
		return "", exec.ErrNotFound
	}
	toWinPath := func(string) (string, error) { return "", errors.New("no wslpath") }

	if err := openWith("/tmp/report.html", true, "linux", lookPath, toWinPath); err != nil {
		t.Fatalf("openWith returned %v, want nil", err)
	}
	waitForMarker(t, marker, "/tmp/report.html")
}

func TestOpenWith_NonWSLLinuxUsesXdgOpen(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked.txt")
	xdgOpen := writeFakeExe(t, dir, "xdg-open", `printf '%s\n' "$*" > `+marker)

	lookPath := func(name string) (string, error) {
		if name == "xdg-open" {
			return xdgOpen, nil
		}
		return "", exec.ErrNotFound
	}

	if err := openWith("/tmp/report.html", false, "linux", lookPath, toWindowsPath); err != nil {
		t.Fatalf("openWith returned %v, want nil", err)
	}
	waitForMarker(t, marker, "/tmp/report.html")
}

func TestOpenWith_NonWSLLinuxNoOpenerReturnsErrNoOpener(t *testing.T) {
	err := openWith("/tmp/report.html", false, "linux", notFoundLookPath, toWindowsPath)
	if !errors.Is(err, errNoOpener) {
		t.Errorf("expected errNoOpener, got %v", err)
	}
}

func TestOpenWith_DarwinAttemptsTheLiteralOpenCommand(t *testing.T) {
	// The darwin branch doesn't consult the injected lookPath at all —
	// it always attempts the literal "open" command, exactly like the
	// original bash version did. This test machine isn't macOS and
	// doesn't have an "open" binary, so this just confirms that failure
	// surfaces as a real error rather than being silently swallowed —
	// it is NOT asserting darwin's actual behavior on real macOS.
	lookPath := func(string) (string, error) {
		t.Fatal("the darwin branch should never consult lookPath")
		return "", nil
	}
	if err := openWith("/tmp/report.html", false, "darwin", lookPath, toWindowsPath); err == nil {
		t.Error("expected an error since \"open\" isn't installed on this test machine")
	}
}

// waitForMarker polls for marker's existence — needed for anything that
// goes through cmd.Start() (fire-and-forget, matching the bash `&`),
// which returns before the child process necessarily got scheduled.
func waitForMarker(t *testing.T, marker, wantArgs string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			if strings.TrimSpace(string(data)) != wantArgs {
				t.Errorf("invoked with %q, want %q", strings.TrimSpace(string(data)), wantArgs)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("opener was never invoked")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIsWSLVersionString(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    bool
	}{
		{"wsl2", "Linux version 5.15.90.1-microsoft-standard-WSL2 (oe-user@oe-host)", true},
		{"wsl1_capitalized", "Linux version 4.4.0-19041-Microsoft (Microsoft@Microsoft.com)", true},
		{"native_linux", "Linux version 6.6.87.2-generic (buildd@lcy02-amd64-039)", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isWSLVersionString(c.version); got != c.want {
				t.Errorf("isWSLVersionString(%q) = %v, want %v", c.version, got, c.want)
			}
		})
	}
}

func TestToWindowsPath_UsesWslpathOutput(t *testing.T) {
	dir := t.TempDir()
	writeFakeExe(t, dir, "wslpath", `printf '%s\n' 'C:\Users\me\report.html'`)
	t.Setenv("PATH", dir)

	got, err := toWindowsPath("/home/me/report.html")
	if err != nil {
		t.Fatal(err)
	}
	if got != `C:\Users\me\report.html` {
		t.Errorf("got %q", got)
	}
}

func TestToWindowsPath_PropagatesCommandError(t *testing.T) {
	dir := t.TempDir()
	writeFakeExe(t, dir, "wslpath", `exit 1`)
	t.Setenv("PATH", dir)

	if _, err := toWindowsPath("/home/me/report.html"); err == nil {
		t.Error("expected an error when wslpath fails")
	}
}
