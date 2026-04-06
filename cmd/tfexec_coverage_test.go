package cmd

import (
	"errors"
	"strings"
	"testing"
)

func TestTerraformCommandErrorUsesStderr(t *testing.T) {
	got := terraformCommandError("stdout output", "  stderr output  ")
	want := "stderr output"
	if got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestTerraformCommandErrorFallsBackToStdout(t *testing.T) {
	got := terraformCommandError("  stdout only  ", "")
	want := "stdout only"
	if got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestTerraformCommandErrorReturnsNoOutputWhenBothEmpty(t *testing.T) {
	got := terraformCommandError("", "  ")
	want := "no output"
	if got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestRepoRootReturnsErrorWhenNotInGitRepo(t *testing.T) {
	// Create a temp directory that is not inside any git repo and chdir into it.
	dir := t.TempDir()
	withWorkingDir(t, dir)

	// Override the walk: if the temp dir is itself inside a git repo on the
	// CI runner, we just skip this test to avoid false positives.
	_, err := repoRoot()
	if err == nil {
		t.Skip("working directory appears to be inside a git repo; skipping not-in-git test")
	}
	if !containsAny(err.Error(), "not inside a git repository", "no .git found") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestStackDirPropagatesRepoRootError(t *testing.T) {
	dir := t.TempDir()
	withWorkingDir(t, dir)

	_, err := stackDir()
	if err == nil {
		t.Skip("working directory appears to be inside a git repo; skipping not-in-git test")
	}
}

func TestBackendArgsOmitsLocalWhenAbsent(t *testing.T) {
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	// No backend.local.hcl written, so only the env backend should appear.
	args := backendArgs(stack, root, testEnv)
	if len(args) != 1 {
		t.Fatalf("expected 1 arg, got %v", args)
	}
	if args[0] != "-backend-config=../envs/prod/backend.hcl" {
		t.Fatalf("unexpected arg: %q", args[0])
	}
}

// containsAny returns true when s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestIsNotFoundErrorNil(t *testing.T) {
	if isNotFoundError(nil) {
		t.Fatal("nil error should not be a not-found error")
	}
}

func TestIsNotFoundErrorNotFound(t *testing.T) {
	if !isNotFoundError(errors.New("resource not found")) {
		t.Fatal("expected not-found error")
	}
}

func TestIsNotFoundErrorDoesNotExist(t *testing.T) {
	if !isNotFoundError(errors.New("The bucket does not exist")) {
		t.Fatal("expected not-found error for 'does not exist'")
	}
}

func TestIsNotFoundErrorCannotBeFound(t *testing.T) {
	if !isNotFoundError(errors.New("the resource cannot be found here")) {
		t.Fatal("expected not-found error for 'cannot be found'")
	}
}

func TestIsNotFoundErrorOtherError(t *testing.T) {
	if isNotFoundError(errors.New("access denied")) {
		t.Fatal("access denied should not be classified as not-found")
	}
}
