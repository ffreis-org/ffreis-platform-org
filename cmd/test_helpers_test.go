package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	testEnv             = "prod"
	testRegion          = "us-east-1"
	testAccountID       = "123456789012"
	testUserARN         = "arn:aws:iam::123456789012:user/tester"
	terraformDirName    = ".terraform"
	traceFileName       = "trace.txt"
	errReadTraceFile    = "read trace file: %v"
	errMkdirTerraform   = "mkdir .terraform: %v"
	errUnexpectedError  = "unexpected error: %v"
	errUnexpectedOutput = "unexpected output: %q"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})
}

func setStdinText(t *testing.T, text string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin-*.txt")
	if err != nil {
		t.Fatalf("create temp stdin: %v", err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("write stdin text: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seek stdin text: %v", err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = old
		_ = f.Close()
	})
}

func setupFakeTerraform(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "terraform")
	content := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake terraform: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return script
}

func initRepoLayout(t *testing.T, root, env string) string {
	t.Helper()
	stack := filepath.Join(root, stackDirName)
	// Create a .git marker so repoRoot() can identify the repo root.
	writeFile(t, filepath.Join(root, ".git"), "gitdir: fake\n")
	writeFile(t, filepath.Join(root, envsDirName, env, "backend.hcl"), "bucket = \"state\"\n")
	writeFile(t, filepath.Join(root, envsDirName, env, "terraform.tfvars"), "org = \"ffreis\"\n")
	if err := os.MkdirAll(stack, 0o755); err != nil {
		t.Fatalf("mkdir stack: %v", err)
	}
	return stack
}
