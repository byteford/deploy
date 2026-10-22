package graph

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluate_NoManualFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "services/a"), 0o755); err != nil {
		t.Fatal(err)
	}
	ms, err := Evaluate(nil, root, "services/a")
	if err != nil {
		t.Fatal(err)
	}
	if ms.HasCheck || ms.Instructions != "" {
		t.Errorf("expected no manual step, got %+v", ms)
	}
}

func TestEvaluate_CheckSatisfied(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "services/a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(dir, ".manual-check"), "#!/bin/sh\necho already unsealed\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dir, ".manual-before"), []byte("unseal vault"), 0o644); err != nil {
		t.Fatal(err)
	}

	ms, err := Evaluate(nil, root, "services/a")
	if err != nil {
		t.Fatal(err)
	}
	if !ms.HasCheck || !ms.Satisfied {
		t.Fatalf("expected satisfied check, got %+v", ms)
	}
	if ms.Instructions != "unseal vault" {
		t.Errorf("expected instructions to still be read even when satisfied, got %q", ms.Instructions)
	}
}

func TestEvaluate_CheckNotSatisfied(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "services/a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(dir, ".manual-check"), "#!/bin/sh\necho not yet\nexit 1\n")

	ms, err := Evaluate(nil, root, "services/a")
	if err != nil {
		t.Fatal(err)
	}
	if ms.Satisfied {
		t.Fatal("expected not satisfied")
	}
	if ms.CheckOutput == "" {
		t.Error("expected check output to be captured even on non-zero exit")
	}
}

func TestEvaluate_CheckNotExecutable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "services/a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".manual-check"), []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ms, err := Evaluate(nil, root, "services/a")
	if err != nil {
		t.Fatal(err)
	}
	if ms.Satisfied {
		t.Fatal("a non-executable .manual-check must never count as satisfied")
	}
	if ms.CheckErr == nil {
		t.Error("expected CheckErr to explain the non-executable file")
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
