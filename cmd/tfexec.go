package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// stackDir and envsDir define the terraform project layout for this repo.
const (
	stackDirName = "terraform/stack"
	envsDirName  = "terraform/envs"
)

// repoRoot returns the absolute path to the repository root by walking up
// from the current working directory until it finds a .git marker. Returns
// an error if no .git directory is found, so callers get a clear message
// instead of operating on an arbitrary directory.
func repoRoot() (string, error) {
	dir, err := filepath.Abs(".")
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside a git repository (no .git found walking up from %s)", dir)
		}
		dir = parent
	}
}

// stackDir returns the absolute path to the terraform stack directory.
func stackDir() (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, stackDirName), nil
}

// backendArgs builds the -backend-config flags for terraform init.
// If terraform/stack/backend.local.hcl exists (gitignored, contains the real
// bucket/table/region), it is prepended before the env backend config. The
// env-specific terraform/envs/<env>/backend.hcl is passed after and supplies
// the per-environment state key. Later flags take precedence in Terraform, so
// the env key always wins over any key in the local override file.
func backendArgs(stackPath, root, env string) []string {
	envsBackend := filepath.Join(root, envsDirName, env, "backend.hcl")
	// Paths are relative to the stack dir since terraform runs there.
	relEnvs, err := filepath.Rel(stackPath, envsBackend)
	if err != nil {
		relEnvs = filepath.Join("..", envsDirName, env, "backend.hcl")
	}

	args := []string{"-backend-config=" + relEnvs}

	local := filepath.Join(stackPath, "backend.local.hcl")
	if _, err := os.Stat(local); err == nil {
		args = append([]string{"-backend-config=backend.local.hcl"}, args...)
	}

	return args
}

// varFileArgs returns the -var-file flags for terraform plan/apply/destroy.
// Both the committed terraform.tfvars and the generated fetched.auto.tfvars.json
// are included so that required variables (org, accounts, budget_alert_email)
// sourced from the fetched file are always available when running from terraform/stack.
func varFileArgs(stackPath, root, env string) []string {
	envsDir := filepath.Join(root, envsDirName, env)

	relPath := func(name string) string {
		abs := filepath.Join(envsDir, name)
		rel, err := filepath.Rel(stackPath, abs)
		if err != nil {
			rel = filepath.Join("..", envsDirName, env, name)
		}
		return "-var-file=" + rel
	}

	return []string{
		relPath("terraform.tfvars"),
		relPath("fetched.auto.tfvars.json"),
	}
}

// isInitialised reports whether terraform has already been initialised in
// the given stack directory (i.e. .terraform/ exists).
func isInitialised(stackPath string) bool {
	_, err := os.Stat(filepath.Join(stackPath, ".terraform"))
	return err == nil
}

// runOptions configures a terraform subprocess invocation.
type runOptions struct {
	stackPath string
	args      []string
	creds     rawCreds
	stdin     io.Reader // nil for non-interactive; os.Stdin for apply prompts
	stdout    io.Writer // defaults to os.Stdout
	stderr    io.Writer // defaults to os.Stderr
}

// runTerraform executes terraform with the given args in stackPath, streaming
// output to the terminal. Returns terraform's exit code directly.
//
// Exit code 2 from plan means "changes present" — callers must treat this as
// a non-error condition.
func runTerraform(ctx context.Context, opts runOptions) (int, error) {
	if opts.stdout == nil {
		opts.stdout = os.Stdout
	}
	if opts.stderr == nil {
		opts.stderr = os.Stderr
	}

	cmd := exec.CommandContext(ctx, "terraform", opts.args...) //nolint:gosec
	cmd.Dir = opts.stackPath

	// Build subprocess env: parent env + injected AWS creds.
	env := os.Environ()
	for k, v := range opts.creds.toEnv() {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	cmd.Stdin = opts.stdin
	cmd.Stdout = opts.stdout
	cmd.Stderr = opts.stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("running terraform: %w", err)
	}
	return 0, nil
}

// terraformInit runs terraform init in the given stack directory.
// It is called automatically before plan/apply/destroy when .terraform/ is absent.
func terraformInit(ctx context.Context, stackPath, root, env string, creds rawCreds) error {
	args := append([]string{"init"}, backendArgs(stackPath, root, env)...)
	code, err := runTerraform(ctx, runOptions{
		stackPath: stackPath,
		args:      args,
		creds:     creds,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("terraform init exited with code %d", code)
	}
	return nil
}

// ensureInit runs terraform init if the stack has not been initialised yet.
func ensureInit(ctx context.Context, stackPath, root, env string, creds rawCreds) error {
	if isInitialised(stackPath) {
		return nil
	}
	d.log.Info("stack not initialised; running terraform init", "stack", stackPath)
	return terraformInit(ctx, stackPath, root, env, creds)
}

// terraformPlanJSON produces a read-only JSON representation of the current
// stack plan so audit can derive the expected resource inventory directly from
// Terraform configuration/state instead of a hardcoded Go list.
func terraformPlanJSON(ctx context.Context) ([]byte, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	stackPath, err := stackDir()
	if err != nil {
		return nil, err
	}
	if err := ensureInit(ctx, stackPath, root, d.env, d.creds); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}

	planFile, err := os.CreateTemp("/tmp", "platform-org-audit-*.tfplan")
	if err != nil {
		return nil, fmt.Errorf("create plan file: %w", err)
	}
	planPath := planFile.Name()
	if err := planFile.Close(); err != nil {
		return nil, fmt.Errorf("close plan file: %w", err)
	}
	defer func() { _ = os.Remove(planPath) }()

	planArgs := []string{
		"plan",
		"-input=false",
		"-lock=false",
		"-detailed-exitcode",
		"-no-color",
		"-out=" + planPath,
	}
	planArgs = append(planArgs, varFileArgs(stackPath, root, d.env)...)

	var planStdout, planStderr bytes.Buffer
	code, err := runTerraform(ctx, runOptions{
		stackPath: stackPath,
		args:      planArgs,
		creds:     d.creds,
		stdout:    &planStdout,
		stderr:    &planStderr,
	})
	if err != nil {
		return nil, err
	}
	if code != 0 && code != 2 {
		return nil, fmt.Errorf("terraform plan exited with code %d: %s", code, terraformCommandError(planStdout.String(), planStderr.String()))
	}

	var showStdout, showStderr bytes.Buffer
	code, err = runTerraform(ctx, runOptions{
		stackPath: stackPath,
		args:      []string{"show", "-json", planPath},
		creds:     d.creds,
		stdout:    &showStdout,
		stderr:    &showStderr,
	})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("terraform show -json exited with code %d: %s", code, terraformCommandError(showStdout.String(), showStderr.String()))
	}
	return showStdout.Bytes(), nil
}

func terraformCommandError(stdout, stderr string) string {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return msg
	}
	if msg := strings.TrimSpace(stdout); msg != "" {
		return msg
	}
	return "no output"
}
