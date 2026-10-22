package tf

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHasBackendStub_FalseThenTrueAfterWrite(t *testing.T) {
	root := t.TempDir()
	dir := "services/a"
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}

	if HasBackendStub(filepath.Join(root, dir)) {
		t.Fatal("should not have a stub before WriteBackendStub is called")
	}
	if err := WriteBackendStub(filepath.Join(root, dir), "s3"); err != nil {
		t.Fatal(err)
	}
	if !HasBackendStub(filepath.Join(root, dir)) {
		t.Fatal("should have a stub after WriteBackendStub")
	}
}

func TestWriteBackendStub_ContentDeclaresTheGivenType(t *testing.T) {
	root := t.TempDir()
	dir := "services/a"
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteBackendStub(filepath.Join(root, dir), "consul"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, dir, BackendStubFileName))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, `backend "consul"`) {
		t.Errorf("stub content missing the backend type:\n%s", content)
	}
	if !strings.Contains(content, "Do not hand-edit") {
		t.Error("stub should warn against hand-editing it")
	}
}

func TestBackendStubFileName_NotDotPrefixed(t *testing.T) {
	// Terraform's config loader skips files whose name starts with a
	// dot — if this were ever renamed to something like
	// ".deploy-backend.tf" (matching this project's other dotfile
	// conventions), Terraform would silently never load it at all.
	if strings.HasPrefix(BackendStubFileName, ".") {
		t.Fatalf("BackendStubFileName = %q must not start with a dot, or Terraform will ignore it", BackendStubFileName)
	}
	if !strings.HasSuffix(BackendStubFileName, ".tf") {
		t.Fatalf("BackendStubFileName = %q must end in .tf for Terraform to load it", BackendStubFileName)
	}
}

func TestRemoveBackendStub_RemovesExistingAndIsNoOpIfAbsent(t *testing.T) {
	root := t.TempDir()
	dir := "services/a"
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteBackendStub(filepath.Join(root, dir), "s3"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveBackendStub(filepath.Join(root, dir)); err != nil {
		t.Fatal(err)
	}
	if HasBackendStub(filepath.Join(root, dir)) {
		t.Fatal("stub should be gone after RemoveBackendStub")
	}
	// Calling it again (nothing to remove) must not error.
	if err := RemoveBackendStub(filepath.Join(root, dir)); err != nil {
		t.Errorf("RemoveBackendStub on an already-absent stub should be a no-op, got: %v", err)
	}
}

func TestResolveSettings_SubstitutesDirPlaceholder(t *testing.T) {
	settings := map[string]string{
		"bucket": "my-bucket",
		"key":    "{dir}.tfstate",
		"region": "us-east-1",
	}
	resolved := ResolveSettings(settings, "services/authentik")

	if resolved["key"] != "services/authentik.tfstate" {
		t.Errorf(`resolved["key"] = %q, want "services/authentik.tfstate"`, resolved["key"])
	}
	if resolved["bucket"] != "my-bucket" || resolved["region"] != "us-east-1" {
		t.Errorf("settings without {dir} should pass through unchanged, got %+v", resolved)
	}
}

func TestResolveSettings_WorksForDifferentBackendSettingNames(t *testing.T) {
	// The whole point of the {dir} placeholder design: it works no
	// matter which setting name a backend type actually uses for "where
	// does this directory's state live" — s3/azurerm call it "key", the
	// local/consul backends call it "path", gcs calls it "prefix".
	cases := []struct {
		settingName string
		template    string
		want        string
	}{
		{"key", "{dir}.tfstate", "services/a.tfstate"},
		{"path", "/state/{dir}.tfstate", "/state/services/a.tfstate"},
		{"prefix", "tfstate/{dir}", "tfstate/services/a"},
	}
	for _, c := range cases {
		resolved := ResolveSettings(map[string]string{c.settingName: c.template}, "services/a")
		if resolved[c.settingName] != c.want {
			t.Errorf("%s=%q resolved to %q, want %q", c.settingName, c.template, resolved[c.settingName], c.want)
		}
	}
}

func TestResolveSettings_DoesNotMutateInput(t *testing.T) {
	original := map[string]string{"key": "{dir}.tfstate"}
	ResolveSettings(original, "services/a")
	if original["key"] != "{dir}.tfstate" {
		t.Errorf("ResolveSettings must not mutate its input, got %q", original["key"])
	}
}

func TestHasDirPlaceholder(t *testing.T) {
	if !HasDirPlaceholder(map[string]string{"key": "{dir}.tfstate"}) {
		t.Error("expected true when a setting contains {dir}")
	}
	if HasDirPlaceholder(map[string]string{"bucket": "my-bucket", "region": "us-east-1"}) {
		t.Error("expected false when no setting contains {dir}")
	}
	if HasDirPlaceholder(nil) {
		t.Error("expected false for nil settings")
	}
}

func TestMigrateBackend_RejectsMissingDirPlaceholder(t *testing.T) {
	// Without {dir} in some setting, every directory would resolve to
	// the exact same location and silently collide state — this must be
	// rejected before ever touching terraform.
	root := t.TempDir()
	dir := "services/a"
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteBackendStub(filepath.Join(root, dir), "local"); err != nil {
		t.Fatal(err)
	}

	runner, err := NewRunner()
	if err != nil {
		t.Skip("terraform not found on PATH — skipping")
	}
	backend := BackendConfig{Type: "local", Settings: map[string]string{"path": "same-for-everyone.tfstate"}}
	err = runner.MigrateBackend(context.Background(), root, dir, backend)
	if err == nil {
		t.Fatal("expected an error when no setting contains {dir}")
	}
	if !strings.Contains(err.Error(), "{dir}") {
		t.Errorf("error should mention the {dir} placeholder, got: %v", err)
	}
}

func TestBackendInitOptions_NoneWithoutStubOrConfig(t *testing.T) {
	root := t.TempDir()
	dir := "services/a"
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := BackendConfig{Type: "s3", Settings: map[string]string{"key": "{dir}.tfstate"}}

	// Configured, but the directory has not been migrated: no options.
	r := &Runner{backend: cfg}
	opts, err := r.backendInitOptions(root, dir)
	if err != nil || len(opts) != 0 {
		t.Fatalf("no stub: got %d opts, err %v; want 0, nil", len(opts), err)
	}

	// Migrated, but no [backend] configured: no options.
	if err := WriteBackendStub(filepath.Join(root, dir), "s3"); err != nil {
		t.Fatal(err)
	}
	r = &Runner{}
	opts, err = r.backendInitOptions(root, dir)
	if err != nil || len(opts) != 0 {
		t.Fatalf("no config: got %d opts, err %v; want 0, nil", len(opts), err)
	}
}

func TestBackendInitOptions_OnePerSettingWhenMigrated(t *testing.T) {
	root := t.TempDir()
	dir := "services/a"
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteBackendStub(filepath.Join(root, dir), "s3"); err != nil {
		t.Fatal(err)
	}
	r := &Runner{backend: BackendConfig{Type: "s3", Settings: map[string]string{
		"bucket": "b",
		"key":    "{dir}.tfstate",
	}}}
	opts, err := r.backendInitOptions(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 2 {
		t.Fatalf("got %d opts, want 2", len(opts))
	}
}

func TestBackendInitOptions_ErrorsWithoutDirPlaceholder(t *testing.T) {
	root := t.TempDir()
	dir := "services/a"
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteBackendStub(filepath.Join(root, dir), "s3"); err != nil {
		t.Fatal(err)
	}
	r := &Runner{backend: BackendConfig{Type: "s3", Settings: map[string]string{"key": "fixed.tfstate"}}}
	if _, err := r.backendInitOptions(root, dir); err == nil {
		t.Fatal("expected an error when no setting contains {dir}")
	}
}
