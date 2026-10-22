package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestExists(t *testing.T) {
	root := t.TempDir()
	if Exists(root) {
		t.Fatal("should not exist before the file is written")
	}
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Exists(root) {
		t.Fatal("should exist once the file is written")
	}
}

func TestLoad_MissingFileReturnsNilNil(t *testing.T) {
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatal("expected nil Manifest for a missing file, not an error")
	}
}

func TestLoad_HTTPSGitURL(t *testing.T) {
	root := writeManifest(t, "services-web=https://github.com/acme/services-web.git\n")
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e := m.Entries["services-web"]
	if !e.IsGit {
		t.Fatal("expected IsGit = true for an https:// URL")
	}
	if e.URL != "https://github.com/acme/services-web.git" {
		t.Errorf("URL = %q", e.URL)
	}
	if e.Ref != "" {
		t.Errorf("Ref = %q, want empty", e.Ref)
	}
	if e.LocalPath != filepath.Join(root, CheckoutsDirName, "services-web") {
		t.Errorf("LocalPath = %q, want %q", e.LocalPath, filepath.Join(root, CheckoutsDirName, "services-web"))
	}
}

func TestLoad_SCPStyleGitURL(t *testing.T) {
	root := writeManifest(t, "services-auth=git@github.com:acme/services-auth.git\n")
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e := m.Entries["services-auth"]
	if !e.IsGit {
		t.Fatal("expected IsGit = true for an SCP-style URL")
	}
	if e.URL != "git@github.com:acme/services-auth.git" {
		t.Errorf("URL = %q", e.URL)
	}
}

func TestLoad_RefAfterHash(t *testing.T) {
	root := writeManifest(t, "config-vault=git@github.com:acme/config-vault.git#v2.1.0\n")
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e := m.Entries["config-vault"]
	if e.URL != "git@github.com:acme/config-vault.git" {
		t.Errorf("URL = %q, want no #ref suffix", e.URL)
	}
	if e.Ref != "v2.1.0" {
		t.Errorf("Ref = %q, want v2.1.0", e.Ref)
	}
}

func TestLoad_TFRootSubpath(t *testing.T) {
	root := writeManifest(t, "infra-host=git@github.com:acme/infra-host.git#v3 deploy/tf\n")
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e := m.Entries["infra-host"]
	if e.TFRoot != "deploy/tf" {
		t.Errorf("TFRoot = %q, want deploy/tf", e.TFRoot)
	}
	wantWorkDir := filepath.Join(root, CheckoutsDirName, "infra-host", "deploy", "tf")
	if e.WorkDir() != wantWorkDir {
		t.Errorf("WorkDir() = %q, want %q", e.WorkDir(), wantWorkDir)
	}
}

func TestLoad_NoTFRootWorkDirEqualsLocalPath(t *testing.T) {
	root := writeManifest(t, "config-vault=git@github.com:acme/config-vault.git#v2.1.0\n")
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e := m.Entries["config-vault"]
	if e.WorkDir() != e.LocalPath {
		t.Errorf("WorkDir() = %q, want it to equal LocalPath %q when TFRoot is unset", e.WorkDir(), e.LocalPath)
	}
}

func TestLoad_LocalAbsolutePath(t *testing.T) {
	root := writeManifest(t, "legacy-service=/opt/manual-checkouts/legacy-service\n")
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e := m.Entries["legacy-service"]
	if e.IsGit {
		t.Fatal("expected IsGit = false for a local path")
	}
	if e.LocalPath != "/opt/manual-checkouts/legacy-service" {
		t.Errorf("LocalPath = %q", e.LocalPath)
	}
}

func TestLoad_LocalRelativePathResolvedAgainstRoot(t *testing.T) {
	root := writeManifest(t, "old-thing=../checked-out-elsewhere\n")
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e := m.Entries["old-thing"]
	want := filepath.Join(root, "../checked-out-elsewhere")
	if e.LocalPath != want {
		t.Errorf("LocalPath = %q, want %q", e.LocalPath, want)
	}
}

func TestLoad_LocalPathWithTFRoot(t *testing.T) {
	root := writeManifest(t, "old-thing=/opt/old-thing deploy/tf\n")
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e := m.Entries["old-thing"]
	if e.TFRoot != "deploy/tf" {
		t.Errorf("TFRoot = %q", e.TFRoot)
	}
	if e.WorkDir() != filepath.Join("/opt/old-thing", "deploy", "tf") {
		t.Errorf("WorkDir() = %q", e.WorkDir())
	}
}

func TestLoad_RefOnLocalPathErrors(t *testing.T) {
	root := writeManifest(t, "old-thing=/opt/old-thing#main\n")
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected an error: #ref only makes sense for a git URL")
	}
}

func TestLoad_CommentsAndBlankLinesIgnored(t *testing.T) {
	root := writeManifest(t, "# a comment\n\nservices-a=git@github.com:acme/a.git\n# trailing\n")
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(m.Entries))
	}
}

func TestLoad_DuplicateNameErrors(t *testing.T) {
	root := writeManifest(t, "services-a=git@github.com:acme/a.git\nservices-a=git@github.com:acme/b.git\n")
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected an error for a duplicate name")
	}
}

func TestLoad_MalformedLineErrors(t *testing.T) {
	root := writeManifest(t, "not a valid line\n")
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected an error for a line with no '='")
	}
}

func TestLoad_EmptyNameErrors(t *testing.T) {
	root := writeManifest(t, "=git@github.com:acme/a.git\n")
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected an error for an empty name")
	}
}

func TestLoad_NoLocationErrors(t *testing.T) {
	root := writeManifest(t, "services-a=\n")
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected an error for a name with no URL/path")
	}
}

func TestLoad_TooManyFieldsErrors(t *testing.T) {
	root := writeManifest(t, "services-a=git@github.com:acme/a.git deploy/tf extra-field\n")
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected an error for too many fields")
	}
}

func TestLoad_OrderPreserved(t *testing.T) {
	root := writeManifest(t, "z=git@github.com:acme/z.git\na=git@github.com:acme/a.git\nm=git@github.com:acme/m.git\n")
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"z", "a", "m"} // file order, NOT alphabetical
	if len(m.Order) != len(want) {
		t.Fatalf("Order = %v, want %v", m.Order, want)
	}
	for i := range want {
		if m.Order[i] != want[i] {
			t.Errorf("Order[%d] = %q, want %q", i, m.Order[i], want[i])
		}
	}
}

func TestLooksLikeGitURL(t *testing.T) {
	gitURLs := []string{
		"https://github.com/acme/repo.git",
		"http://internal-git.lan/repo.git",
		"ssh://git@github.com/acme/repo.git",
		"git://github.com/acme/repo.git",
		"file:///tmp/some-bare-repo.git",
		"git@github.com:acme/repo.git",
	}
	for _, u := range gitURLs {
		if !looksLikeGitURL(u) {
			t.Errorf("looksLikeGitURL(%q) = false, want true", u)
		}
	}

	localPaths := []string{
		"/opt/manual-checkouts/legacy-service",
		"../checked-out-elsewhere",
		"relative/path",
		"services/a",
	}
	for _, p := range localPaths {
		if looksLikeGitURL(p) {
			t.Errorf("looksLikeGitURL(%q) = true, want false", p)
		}
	}
}

func TestResolver_NotSyncedHint_GitEntryMissing(t *testing.T) {
	root := writeManifest(t, "vault=git@github.com:acme/vault.git\n")
	r, err := NewResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	hint := r.NotSyncedHint(root, "vault")
	if hint == "" {
		t.Fatal("expected a non-empty hint for a not-yet-cloned git entry")
	}
	if !strings.Contains(hint, "vault") || !strings.Contains(hint, "sync") {
		t.Errorf("hint should name the entry and mention sync, got: %q", hint)
	}
}

func TestResolver_NotSyncedHint_GitEntryPresent(t *testing.T) {
	root := writeManifest(t, "vault=git@github.com:acme/vault.git\n")
	// Create the checkout directory so it "looks" synced.
	if err := os.MkdirAll(filepath.Join(root, CheckoutsDirName, "vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := NewResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	if hint := r.NotSyncedHint(root, "vault"); hint != "" {
		t.Errorf("expected no hint once the checkout exists, got: %q", hint)
	}
}

func TestResolver_NotSyncedHint_LocalEntryNeverHints(t *testing.T) {
	root := writeManifest(t, "vault=/opt/manual-checkouts/vault\n")
	r, err := NewResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	// Even though /opt/manual-checkouts/vault doesn't exist, a
	// local-path entry never gets a "not synced" hint — that's the
	// separate "path doesn't exist" error graph.Discover already
	// produces for local entries specifically (see internal/graph).
	if hint := r.NotSyncedHint(root, "vault"); hint != "" {
		t.Errorf("a local-path entry must never get a not-synced hint, got: %q", hint)
	}
}

func TestResolver_NotSyncedHint_SingleRepoModeNeverHints(t *testing.T) {
	root := t.TempDir() // no .deploy-repos at all
	r, err := NewResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	if hint := r.NotSyncedHint(root, "services/anything"); hint != "" {
		t.Errorf("single-repo mode (nil manifest) must never hint, got: %q", hint)
	}
}

func TestResolver_NotSyncedHint_UnknownNameNeverHints(t *testing.T) {
	root := writeManifest(t, "vault=git@github.com:acme/vault.git\n")
	r, err := NewResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	if hint := r.NotSyncedHint(root, "not-in-the-manifest"); hint != "" {
		t.Errorf("a name not in the manifest must never hint, got: %q", hint)
	}
}
