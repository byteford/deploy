package report

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Open opens path in a browser, replicating the bash script's WSL-aware
// fallback chain: wslview, then cmd.exe, then explorer.exe, then
// xdg-open/open on native Linux/macOS.
func Open(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return openWith(abs, isWSL(), runtime.GOOS, exec.LookPath, toWindowsPath)
}

// openWith holds the actual fallback-chain decision logic, taking the
// environment-dependent bits (WSL detection, OS, executable lookup, and
// unix-to-Windows path translation) as parameters rather than calling
// them directly — lets tests exercise every branch of the chain by
// substituting fake lookups instead of needing to actually run inside
// WSL, on macOS, or have any of these openers installed for real.
func openWith(abs string, wsl bool, goos string, lookPath func(string) (string, error), toWinPath func(string) (string, error)) error {
	if wsl {
		if p, err := lookPath("wslview"); err == nil {
			return exec.Command(p, abs).Run()
		}
		if winPath, err := toWinPath(abs); err == nil {
			if p, err := lookPath("cmd.exe"); err == nil {
				return exec.Command(p, "/c", "start", "", winPath).Run()
			}
			if p, err := lookPath("explorer.exe"); err == nil {
				// explorer.exe often exits non-zero even on success —
				// same caveat the bash version notes; don't treat as fatal.
				_ = exec.Command(p, winPath).Run()
				return nil
			}
		}
	}

	switch goos {
	case "darwin":
		return exec.Command("open", abs).Run()
	default:
		if p, err := lookPath("xdg-open"); err == nil {
			cmd := exec.Command(p, abs)
			return cmd.Start() // fire-and-forget, matching the bash `&`
		}
	}
	return errNoOpener
}

func isWSL() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return isWSLVersionString(string(data))
}

// isWSLVersionString is the pure detection logic behind isWSL — WSL's
// reported kernel version string contains "microsoft" (capitalization
// varies: "Microsoft" on WSL1, "microsoft" on WSL2) — split out so it's
// testable without needing to actually run inside WSL or fake
// /proc/version on disk.
func isWSLVersionString(versionString string) bool {
	return strings.Contains(strings.ToLower(versionString), "microsoft")
}

func toWindowsPath(unixPath string) (string, error) {
	out, err := exec.Command("wslpath", "-w", unixPath).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

type openErr string

func (e openErr) Error() string { return string(e) }

const errNoOpener = openErr("don't know how to open a browser here")
