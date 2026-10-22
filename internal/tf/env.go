package tf

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvFileName is the gitignored, secrets-bearing config file: plain
// KEY=value lines, split into sections, used across every root module
// instead of duplicating values in each directory's terraform.tfvars.
//
// This is read only by this binary, so it's not a shell script — no
// bash subprocess, no quoting rules. The format:
//
//	[tfvars]
//	authentik_token=abc123
//	authentik_config={"issuer": "https://auth.example.lan"}
//
//	[env]
//	AWS_PROFILE=homelab
//
//	[backend]
//	type=s3
//	bucket=my-tfstate-bucket
//	region=us-east-1
//
// Lines under [tfvars] are set as TF_VAR_<key> — you write the bare
// Terraform variable name, the app adds the prefix Terraform actually
// requires. Lines under [env] are set as literal environment variables
// (credentials, PATH-ish things a subprocess needs directly, anything
// that isn't a Terraform input). [tfvars] is the default section, since
// most entries are Terraform variables — content before any [section]
// header is treated as [tfvars]. Lines under [backend] configure a
// shared remote backend (see backend.go) rather than becoming
// environment variables at all — "type" selects the backend type,
// everything else becomes a `-backend-config` setting at init time.
//
// A value is everything after the first "=" on the line. Leading/
// trailing whitespace around it is trimmed, and if what remains starts
// and ends with a matching pair of quote characters (" or '), that one
// outer pair is stripped — the same convention most .env-style tools
// use, and specifically so values carried over from the old env.sh
// (which needed quoting for the shell) don't silently end up with
// literal quote characters baked into the string Terraform receives.
// No other escaping happens inside the value. JSON-shaped values are
// unaffected: they start with "{" or "[", not a bare quote, so this
// never touches them.
//
// [tfvars] intentionally does NOT become `-var` flags passed to
// terraform: Terraform errors on `-var`/`-var-file` for a variable a
// given root module doesn't declare, which is exactly wrong for a value
// shared across many directories that each only declare some of them.
// TF_VAR_* environment variables are silently ignored by modules that
// don't declare them, which is why this stays env-var-based rather than
// switching to `-var`.
const EnvFileName = ".env"

// SharedEnvFileName is EnvFileName's committed counterpart: same format,
// same sections, but meant to hold non-secret config (a bucket name or
// region isn't a secret; the credentials to access that bucket are) so
// it can safely go in version control. Loaded before EnvFileName, so a
// value present in both is a local override of the shared default —
// secrets win, which matters for [backend]: the bulk of the settings
// (bucket, region) live here, but a directory could still override or
// add a credential-bearing setting from the gitignored file if a
// backend type ever needed one inline.
const SharedEnvFileName = ".env.shared"

type section int

const (
	sectionTFVars section = iota
	sectionEnv
	sectionBackend
)

// BackendConfig is a [backend] section's content: Type selects which
// Terraform backend ("s3", "consul", "azurerm", ...), Settings holds
// every other key as a raw `-backend-config` setting passed at init
// time. It's deliberately generic — a type plus a string map — rather
// than modeling any specific backend's schema, since Terraform supports
// many backend types with entirely different settings and this doesn't
// need to know which one a given homelab uses.
type BackendConfig struct {
	Type     string
	Settings map[string]string
}

// IsZero reports whether no [backend] section was found anywhere.
func (b BackendConfig) IsZero() bool {
	return b.Type == "" && len(b.Settings) == 0
}

// Merge layers other on top of b: other's Type wins if set, and each of
// other's Settings overwrites b's for the same key. Used to combine the
// shared (committed) and secret (gitignored) files' [backend] sections,
// in that order, so a secret file can add or override a specific
// setting without needing to restate the whole section.
func (b *BackendConfig) Merge(other BackendConfig) {
	if other.Type != "" {
		b.Type = other.Type
	}
	if len(other.Settings) == 0 {
		return
	}
	if b.Settings == nil {
		b.Settings = make(map[string]string, len(other.Settings))
	}
	for k, v := range other.Settings {
		b.Settings[k] = v
	}
}

// LoadEnvFile reads EnvFileName from repoRoot and applies it to this
// process's environment (subprocesses, including terraform, inherit it).
// found is false if the file doesn't exist — same "warn and continue"
// behavior as the original bash script had for a missing env.sh. backend
// is the zero value if the file has no [backend] section.
//
// Intended to be called at most once per process. Every value it sets
// via os.Setenv stays set for the rest of the process's life — fine for
// the CLI, which loads once and exits, and for `serve`, which also
// loads once at startup today. If `serve` ever grows a config-reload
// feature, a second call here would leave stale keys from the first
// load coexisting with (or shadowed by) the new ones rather than
// cleanly replacing them; that would need its own handling, not
// something to guard against speculatively here.
func LoadEnvFile(repoRoot string) (found bool, backend BackendConfig, err error) {
	return LoadEnvFileAt(filepath.Join(repoRoot, EnvFileName))
}

// LoadEnvFileAt is LoadEnvFile against an explicit path rather than
// repoRoot-joined-with-EnvFileName — what the CLI's --env-file and
// --shared-env-file flags use to point at a file somewhere other than
// the default location.
func LoadEnvFileAt(path string) (found bool, backend BackendConfig, err error) {
	f, openErr := os.Open(path)
	if openErr != nil {
		if os.IsNotExist(openErr) {
			return false, BackendConfig{}, nil
		}
		return false, BackendConfig{}, openErr
	}
	defer f.Close()

	sec := sectionTFVars // most entries are Terraform vars — default to that
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if newSec, ok := parseSectionHeader(trimmed); ok {
			sec = newSec
			continue
		}

		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			return true, backend, fmt.Errorf("%s:%d: expected KEY=value or [section], got %q", path, lineNo, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return true, backend, fmt.Errorf("%s:%d: empty key in %q", path, lineNo, line)
		}
		value = unquote(strings.TrimSpace(value))

		switch sec {
		case sectionBackend:
			if strings.EqualFold(key, "type") {
				backend.Type = value
			} else {
				if backend.Settings == nil {
					backend.Settings = map[string]string{}
				}
				backend.Settings[key] = value
			}
		default:
			envKey := key
			if sec == sectionTFVars {
				envKey = "TF_VAR_" + key
			}
			if setErr := os.Setenv(envKey, value); setErr != nil {
				return true, backend, fmt.Errorf("%s:%d: setting %s: %w", path, lineNo, envKey, setErr)
			}
		}
	}
	return true, backend, sc.Err()
}

// unquote strips one matching outer pair of " or ' quote characters, if
// present. "http://x" -> http://x, 'http://x' -> http://x, {"a":1} is
// left untouched (starts with "{", not a quote), and a lone quote
// character ("` by itself) is left untouched since there's no pair to
// strip.
func unquote(v string) string {
	if len(v) < 2 {
		return v
	}
	first, last := v[0], v[len(v)-1]
	if (first == '"' || first == '\'') && first == last {
		return v[1 : len(v)-1]
	}
	return v
}

func parseSectionHeader(trimmed string) (section, bool) {
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1])) {
	case "tfvars":
		return sectionTFVars, true
	case "env":
		return sectionEnv, true
	case "backend":
		return sectionBackend, true
	default:
		return 0, false
	}
}

// WarnMissingEnv prints a warning when the given env file path is
// absent, mirroring the original script's behavior for a missing
// env.sh.
func WarnMissingEnv(w *bufio.Writer, path string) {
	fmt.Fprintf(w, "warning: %s not found - copy %s.example (if present) and fill in values, or export TF_VAR_* yourself\n", path, path)
	w.Flush()
}
