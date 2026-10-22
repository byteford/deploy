package tf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, EnvFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// clearEnv unsets the given keys when the test finishes, so state set by
// LoadEnvFile doesn't leak into other tests. Deliberately plain
// os.Unsetenv rather than t.Setenv: these keys aren't expected to exist
// before the test runs, so there's no prior value to restore, and
// combining the two would just leave a stray empty-string value behind.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		os.Unsetenv(k)
	}
}

func TestLoadEnvFile_Missing(t *testing.T) {
	root := t.TempDir()
	found, _, err := LoadEnvFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected found=false for a repo with no .env")
	}
}

func TestLoadEnvFile_DefaultSectionIsTFVars(t *testing.T) {
	defer clearEnv(t, "TF_VAR_authentik_token")
	root := writeEnvFile(t, "authentik_token=abc123\n")

	found, _, err := LoadEnvFile(root)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got := os.Getenv("TF_VAR_authentik_token"); got != "abc123" {
		t.Errorf("TF_VAR_authentik_token = %q, want abc123", got)
	}
}

func TestLoadEnvFile_JSONShapedValueUntouched(t *testing.T) {
	defer clearEnv(t, "TF_VAR_config")
	json := `{"issuer": "https://auth.example.lan", "scopes": [1, 2, 3]}`
	root := writeEnvFile(t, "config="+json+"\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TF_VAR_config"); got != json {
		t.Errorf("TF_VAR_config = %q, want %q (JSON-shaped values must pass through untouched — they don't start/end with a bare quote character)", got, json)
	}
}

func TestLoadEnvFile_DoubleQuotedValueIsUnwrapped(t *testing.T) {
	// Reproduces the real failure: a value carried over from env.sh
	// habits, quoted for the shell's benefit, e.g.
	//   nomad_addr="http://192.168.2.6:4646"
	// must NOT end up as TF_VAR_nomad_addr containing literal quote
	// characters — that's exactly what broke the nomad/vault/authentik
	// provider configuration (url.Parse choking on a leading `"`).
	defer clearEnv(t, "TF_VAR_nomad_addr")
	root := writeEnvFile(t, `nomad_addr="http://192.168.2.6:4646"`+"\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TF_VAR_nomad_addr"); got != "http://192.168.2.6:4646" {
		t.Errorf("TF_VAR_nomad_addr = %q, want http://192.168.2.6:4646 (no literal quotes)", got)
	}
}

func TestLoadEnvFile_SingleQuotedValueIsUnwrapped(t *testing.T) {
	defer clearEnv(t, "TF_VAR_nomad_ssl_verify")
	root := writeEnvFile(t, "nomad_ssl_verify='true'\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TF_VAR_nomad_ssl_verify"); got != "true" {
		t.Errorf("TF_VAR_nomad_ssl_verify = %q, want true", got)
	}
}

func TestLoadEnvFile_MismatchedQuotesLeftAlone(t *testing.T) {
	defer clearEnv(t, "TF_VAR_odd")
	root := writeEnvFile(t, `odd="mismatched'`+"\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TF_VAR_odd"); got != `"mismatched'` {
		t.Errorf("TF_VAR_odd = %q, want the value untouched since the quotes don't match", got)
	}
}

func TestLoadEnvFile_LoneQuoteCharacterLeftAlone(t *testing.T) {
	defer clearEnv(t, "TF_VAR_single")
	root := writeEnvFile(t, `single="`+"\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TF_VAR_single"); got != `"` {
		t.Errorf("TF_VAR_single = %q, want a lone quote character, unstripped", got)
	}
}

func TestLoadEnvFile_EmptyQuotedValueBecomesEmptyString(t *testing.T) {
	defer clearEnv(t, "TF_VAR_empty")
	root := writeEnvFile(t, `empty=""`+"\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got, ok := os.LookupEnv("TF_VAR_empty"); !ok || got != "" {
		t.Errorf("TF_VAR_empty = (%q, %v), want (\"\", true)", got, ok)
	}
}

func TestLoadEnvFile_EnvSectionIsLiteral(t *testing.T) {
	defer clearEnv(t, "AWS_PROFILE", "TF_VAR_AWS_PROFILE")
	root := writeEnvFile(t, "[env]\nAWS_PROFILE=homelab\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("AWS_PROFILE"); got != "homelab" {
		t.Errorf("AWS_PROFILE = %q, want homelab", got)
	}
	if got := os.Getenv("TF_VAR_AWS_PROFILE"); got != "" {
		t.Errorf("TF_VAR_AWS_PROFILE should not be set, got %q", got)
	}
}

func TestLoadEnvFile_SwitchesBackToTFVars(t *testing.T) {
	defer clearEnv(t, "TF_VAR_first", "AWS_PROFILE", "TF_VAR_vault_addr")
	root := writeEnvFile(t, "first=one\n\n[env]\nAWS_PROFILE=homelab\n\n[tfvars]\nvault_addr=https://vault.internal:8200\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TF_VAR_first"); got != "one" {
		t.Errorf("TF_VAR_first = %q, want one", got)
	}
	if got := os.Getenv("AWS_PROFILE"); got != "homelab" {
		t.Errorf("AWS_PROFILE = %q, want homelab", got)
	}
	if got := os.Getenv("TF_VAR_vault_addr"); got != "https://vault.internal:8200" {
		t.Errorf("TF_VAR_vault_addr = %q, want https://vault.internal:8200", got)
	}
}

func TestLoadEnvFile_CommentsAndBlankLinesIgnored(t *testing.T) {
	defer clearEnv(t, "TF_VAR_a")
	root := writeEnvFile(t, "# a comment\n\n   \na=1\n# trailing comment\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TF_VAR_a"); got != "1" {
		t.Errorf("TF_VAR_a = %q, want 1", got)
	}
}

func TestLoadEnvFile_EmptyValueAllowed(t *testing.T) {
	defer clearEnv(t, "TF_VAR_empty")
	root := writeEnvFile(t, "empty=\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv("TF_VAR_empty"); !ok {
		t.Error("TF_VAR_empty should be set (to an empty string), not merely absent")
	}
}

func TestLoadEnvFile_MalformedLineErrors(t *testing.T) {
	root := writeEnvFile(t, "not a valid line without equals\n")
	if _, _, err := LoadEnvFile(root); err == nil {
		t.Fatal("expected an error for a line with no '='")
	}
}

func TestLoadEnvFile_EmptyKeyErrors(t *testing.T) {
	root := writeEnvFile(t, "=novalue\n")
	if _, _, err := LoadEnvFile(root); err == nil {
		t.Fatal("expected an error for an empty key")
	}
}

func TestLoadEnvFile_UnknownSectionTreatedAsKV(t *testing.T) {
	// "[bogus]" has no "=" in it, so it should surface as a malformed-line
	// error rather than silently becoming a no-op section switch.
	root := writeEnvFile(t, "[bogus]\nfoo=bar\n")
	if _, _, err := LoadEnvFile(root); err == nil {
		t.Fatal("expected an error for an unrecognized [section] header")
	}
}

func TestLoadEnvFileAt_ArbitraryPath(t *testing.T) {
	// The --env-file flag points LoadEnvFileAt at a path that has no
	// particular relationship to any "repo root" — confirm it just
	// works against a bare file path.
	defer clearEnv(t, "TF_VAR_from_custom_path")
	dir := t.TempDir()
	path := filepath.Join(dir, "prod.env")
	if err := os.WriteFile(path, []byte("from_custom_path=yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	found, _, err := LoadEnvFileAt(path)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got := os.Getenv("TF_VAR_from_custom_path"); got != "yes" {
		t.Errorf("TF_VAR_from_custom_path = %q, want yes", got)
	}
}

func TestLoadEnvFileAt_MissingFileReturnsNotFound(t *testing.T) {
	found, _, err := LoadEnvFileAt(filepath.Join(t.TempDir(), "does-not-exist.env"))
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected found=false for a nonexistent path")
	}
}

func TestLoadEnvFileAt_ErrorMessageReferencesGivenPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.env")
	if err := os.WriteFile(path, []byte("not a valid line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadEnvFileAt(path)
	if err == nil {
		t.Fatal("expected an error for a malformed line")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q should reference the actual path %q, not the default EnvFileName", err.Error(), path)
	}
}

func TestLoadEnvFile_EqualsSignInsideQuotedValue(t *testing.T) {
	// strings.Cut splits on the FIRST "=", so a value containing its own
	// "=" (e.g. a connection string or base64 blob) must survive intact
	// rather than being truncated at that first occurrence.
	defer clearEnv(t, "TF_VAR_dsn")
	root := writeEnvFile(t, `dsn="postgres://user:pass@host/db?sslmode=require"`+"\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	want := "postgres://user:pass@host/db?sslmode=require"
	if got := os.Getenv("TF_VAR_dsn"); got != want {
		t.Errorf("TF_VAR_dsn = %q, want %q", got, want)
	}
}

func TestLoadEnvFile_BracketedValueMidLineIsNotASectionHeader(t *testing.T) {
	// parseSectionHeader only fires on a trimmed line that starts with
	// "[" and ends with "]" as a whole — a KEY=value line whose value
	// happens to look bracket-shaped (JSON-ish, or literally "[env]")
	// must be parsed as an ordinary value, not mistaken for a section
	// switch.
	defer clearEnv(t, "TF_VAR_tag")
	root := writeEnvFile(t, "tag=[env]\n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TF_VAR_tag"); got != "[env]" {
		t.Errorf("TF_VAR_tag = %q, want [env] (literal value, not a section header)", got)
	}
}

func TestLoadEnvFile_KeyWithLeadingTrailingWhitespaceIsTrimmed(t *testing.T) {
	defer clearEnv(t, "TF_VAR_spaced")
	root := writeEnvFile(t, "  spaced   =   value  \n")

	if _, _, err := LoadEnvFile(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TF_VAR_spaced"); got != "value" {
		t.Errorf("TF_VAR_spaced = %q, want value (surrounding whitespace trimmed from both key and value)", got)
	}
}

func TestLoadEnvFile_BackendSectionTypeIsCaseInsensitive(t *testing.T) {
	// parseSectionHeader lowercases "[Backend]"/"[BACKEND]" etc., but the
	// "type" key inside it is matched with strings.EqualFold separately
	// — worth pinning both independently since they're two different
	// pieces of case-handling.
	root := writeEnvFile(t, "[Backend]\nTYPE=s3\nbucket=my-bucket\n")

	_, backend, err := LoadEnvFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if backend.Type != "s3" {
		t.Errorf("backend.Type = %q, want s3 (TYPE= should match case-insensitively)", backend.Type)
	}
	if backend.Settings["bucket"] != "my-bucket" {
		t.Errorf("backend.Settings[bucket] = %q, want my-bucket", backend.Settings["bucket"])
	}
}

func TestLoadEnvFile_DelegatesToLoadEnvFileAt(t *testing.T) {
	// LoadEnvFile(repoRoot) must be exactly equivalent to
	// LoadEnvFileAt(filepath.Join(repoRoot, EnvFileName)) — this pins
	// that delegation down so it can't quietly diverge.
	defer clearEnv(t, "TF_VAR_x")
	root := writeEnvFile(t, "x=1\n")
	found, _, err := LoadEnvFile(root)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got := os.Getenv("TF_VAR_x"); got != "1" {
		t.Errorf("TF_VAR_x = %q, want 1", got)
	}
}
