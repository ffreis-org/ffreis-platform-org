package cmd

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/spf13/cobra"
)

const testExecuteCommandCodeErrorf = "executeCommand() code = %d, want %d"

func TestExecuteCommandWithError(t *testing.T) {
	cmd := &cobra.Command{
		Use:           "fail-cmd",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return errors.New("something went wrong")
		},
	}

	var stderr bytes.Buffer
	code := executeCommand(cmd, &stderr)
	if code != exitError {
		t.Fatalf(testExecuteCommandCodeErrorf, code, exitError)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("something went wrong")) {
		t.Fatalf("expected error message in stderr, got: %q", stderr.String())
	}
}

func TestExecuteCommandWithEmptyErrorMessage(t *testing.T) {
	// When the error message is empty, it should still return exitError but
	// not write anything to stderr.
	cmd := &cobra.Command{
		Use:           "empty-err-cmd",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return errors.New("")
		},
	}

	var stderr bytes.Buffer
	code := executeCommand(cmd, &stderr)
	if code != exitError {
		t.Fatalf(testExecuteCommandCodeErrorf, code, exitError)
	}
	// Empty message → nothing written to stderr
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr for empty error, got: %q", stderr.String())
	}
}

func TestExecuteCommandSuccess(t *testing.T) {
	cmd := &cobra.Command{
		Use:  "ok-cmd",
		RunE: func(_ *cobra.Command, _ []string) error { return nil },
	}
	code := executeCommand(cmd, io.Discard)
	if code != exitOK {
		t.Fatalf(testExecuteCommandCodeErrorf, code, exitOK)
	}
}

func TestToEnvContainsFiveKeys(t *testing.T) {
	c := rawCreds{
		AccessKeyID:     "ID",
		SecretAccessKey: "SECRET",
		SessionToken:    "TOKEN",
		Region:          "us-east-1",
	}
	env := c.toEnv()
	wantKeys := []string{
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AWS_REGION",
		"AWS_DEFAULT_REGION",
	}
	if len(env) != len(wantKeys) {
		t.Fatalf("toEnv returned %d keys, want %d", len(env), len(wantKeys))
	}
	for _, key := range wantKeys {
		if _, ok := env[key]; !ok {
			t.Errorf("toEnv missing key %q", key)
		}
	}
}
