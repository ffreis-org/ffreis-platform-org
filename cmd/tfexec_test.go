package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendArgsIncludesLocalOverride(t *testing.T) {
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	writeFile(t, filepath.Join(stack, "backend.local.hcl"), "bucket = \"local\"\n")

	args := backendArgs(stack, root, testEnv)
	want := []string{
		"-backend-config=backend.local.hcl",
		"-backend-config=../envs/prod/backend.hcl",
	}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("backend args: want %v got %v", want, args)
	}
}

func TestVarFileArgsBuildsRelativePaths(t *testing.T) {
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)

	got := varFileArgs(stack, root, testEnv)
	want := []string{
		"-var-file=../envs/prod/terraform.tfvars",
		"-var-file=../envs/prod/fetched.auto.tfvars.json",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("var file args: want %v got %v", want, got)
	}
}

func TestIsInitialised(t *testing.T) {
	stack := t.TempDir()
	if isInitialised(stack) {
		t.Fatal("expected stack without .terraform to be uninitialised")
	}
	if err := os.MkdirAll(filepath.Join(stack, terraformDirName), 0o755); err != nil {
		t.Fatalf(errMkdirTerraform, err)
	}
	if !isInitialised(stack) {
		t.Fatal("expected stack with .terraform to be initialised")
	}
}

func TestRunTerraformStreamsAndReturnsExitCode(t *testing.T) {
	setupFakeTerraform(t, `printf 'stdout:%s\n' "$*"; printf 'stderr:%s\n' "$AWS_ACCESS_KEY_ID" >&2; exit 2`)

	var stdout, stderr bytes.Buffer
	code, err := runTerraform(context.Background(), runOptions{
		stackPath: t.TempDir(),
		args:      []string{"plan", "-no-color"},
		creds: rawCreds{
			AccessKeyID:     "AKIAOUT",
			SecretAccessKey: "secret",
			SessionToken:    "token",
			Region:          testRegion,
		},
		stdout: &stdout,
		stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("runTerraform: %v", err)
	}
	if code != 2 {
		t.Fatalf("exit code: want 2 got %d", code)
	}
	if !strings.Contains(stdout.String(), "stdout:plan -no-color") {
		t.Fatalf("stdout missing args: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "stderr:AKIAOUT") {
		t.Fatalf("stderr missing env injection: %q", stderr.String())
	}
}

func TestTerraformInitUsesBackendArgs(t *testing.T) {
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	writeFile(t, filepath.Join(stack, "backend.local.hcl"), "bucket = \"local\"\n")
	traceFile := filepath.Join(t.TempDir(), traceFileName)
	t.Setenv("TRACE_FILE", traceFile)
	setupFakeTerraform(t, `printf '%s\n' "$*" > "$TRACE_FILE"`)

	err := terraformInit(context.Background(), stack, root, testEnv, rawCreds{Region: testRegion})
	if err != nil {
		t.Fatalf("terraformInit: %v", err)
	}
	got, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf(errReadTraceFile, err)
	}
	want := "init -backend-config=backend.local.hcl -backend-config=../envs/prod/backend.hcl\n"
	if string(got) != want {
		t.Fatalf("terraform init args: want %q got %q", want, string(got))
	}
}

func TestEnsureInitSkipsWhenAlreadyInitialised(t *testing.T) {
	stack := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stack, terraformDirName), 0o755); err != nil {
		t.Fatalf(errMkdirTerraform, err)
	}
	if err := ensureInit(context.Background(), stack, t.TempDir(), testEnv, rawCreds{Region: testRegion}); err != nil {
		t.Fatalf("ensureInit should skip: %v", err)
	}
}

func TestEnsureInitRunsInitWhenNeeded(t *testing.T) {
	d.log = newLogger("error")
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	traceFile := filepath.Join(t.TempDir(), traceFileName)
	t.Setenv("TRACE_FILE", traceFile)
	setupFakeTerraform(t, `printf '%s\n' "$1" > "$TRACE_FILE"`)

	if err := ensureInit(context.Background(), stack, root, testEnv, rawCreds{Region: testRegion}); err != nil {
		t.Fatalf("ensureInit: %v", err)
	}
	got, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf(errReadTraceFile, err)
	}
	if string(got) != "init\n" {
		t.Fatalf("expected terraform init to run, got %q", string(got))
	}
}
