package cmd

import (
	"context"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"log/slog"
)

func TestRawCredsToEnv(t *testing.T) {
	creds := rawCreds{
		AccessKeyID:     "AKIA123",
		SecretAccessKey: "secret",
		SessionToken:    "token",
		Region:          testRegion,
	}

	env := creds.toEnv()
	if env["AWS_ACCESS_KEY_ID"] != creds.AccessKeyID {
		t.Fatalf("access key: want %q got %q", creds.AccessKeyID, env["AWS_ACCESS_KEY_ID"])
	}
	if env["AWS_SECRET_ACCESS_KEY"] != creds.SecretAccessKey {
		t.Fatalf("secret key: want %q got %q", creds.SecretAccessKey, env["AWS_SECRET_ACCESS_KEY"])
	}
	if env["AWS_SESSION_TOKEN"] != creds.SessionToken {
		t.Fatalf("session token: want %q got %q", creds.SessionToken, env["AWS_SESSION_TOKEN"])
	}
	if env["AWS_REGION"] != creds.Region || env["AWS_DEFAULT_REGION"] != creds.Region {
		t.Fatalf("region env mismatch: %#v", env)
	}
}

func TestNewLoggerLevelMapping(t *testing.T) {
	ctx := context.Background()
	if !newLogger("debug").Enabled(ctx, slog.LevelDebug) {
		t.Fatal("debug logger should enable debug level")
	}
	if newLogger("info").Enabled(ctx, slog.LevelDebug) {
		t.Fatal("info logger should not enable debug level")
	}
	if !newLogger("warn").Enabled(ctx, slog.LevelWarn) {
		t.Fatal("warn logger should enable warn level")
	}
	if !newLogger("error").Enabled(ctx, slog.LevelError) {
		t.Fatal("error logger should enable error level")
	}
}

func TestLoadAWSConfigRequiresCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")

	_, err := loadAWSConfig(context.Background(), "", testRegion)
	if err == nil {
		t.Fatal("expected missing credentials error, got nil")
	}
	if !strings.Contains(err.Error(), "no AWS credentials") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAWSConfigFromEnv(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "env-secret")
	t.Setenv("AWS_SESSION_TOKEN", "env-token")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	cfg, err := loadAWSConfig(context.Background(), "", "us-west-2")
	if err != nil {
		t.Fatalf("loadAWSConfig: %v", err)
	}
	if cfg.Region != "us-west-2" {
		t.Fatalf("region: want us-west-2 got %q", cfg.Region)
	}
	creds, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve credentials: %v", err)
	}
	if creds.AccessKeyID != "AKIAENV" {
		t.Fatalf("access key: want AKIAENV got %q", creds.AccessKeyID)
	}
}

func TestAssumeAdminRoleAlreadyAssumedSkipsSTS(t *testing.T) {
	d.log = newLogger("error")
	cfg := sdkaws.Config{
		Region:      testRegion,
		Credentials: credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
	}

	assumedCfg, creds, err := assumeAdminRole(
		context.Background(),
		cfg,
		"arn:aws:sts::123456789012:assumed-role/platform-admin/session",
		"123456789012",
		testRegion,
	)
	if err != nil {
		t.Fatalf("assumeAdminRole: %v", err)
	}
	if assumedCfg.Region != cfg.Region {
		t.Fatalf("region: want %q got %q", cfg.Region, assumedCfg.Region)
	}
	if creds.AccessKeyID != "AKIA" || creds.SecretAccessKey != "secret" || creds.SessionToken != "token" {
		t.Fatalf("unexpected creds: %#v", creds)
	}
}

func TestAssumeAdminRoleRootUsesTempUserBridge(t *testing.T) {
	d.log = newLogger("error")
	cfg := sdkaws.Config{
		Region:      testRegion,
		Credentials: credentials.NewStaticCredentialsProvider("ROOTKEY", "secret", "token"),
	}

	// Inject a stub that records it was called and returns known assumed-role creds.
	bridgeCalled := false
	orig := assumeRoleViaTempUserFn
	t.Cleanup(func() { assumeRoleViaTempUserFn = orig })
	assumeRoleViaTempUserFn = func(_ context.Context, _ sdkaws.Config, _ string, region string) (sdkaws.Config, rawCreds, error) {
		bridgeCalled = true
		rc := rawCreds{AccessKeyID: "ASSUMED", SecretAccessKey: "assumed-secret", SessionToken: "assumed-token", Region: region}
		bridgeCfg := sdkaws.Config{
			Region:      region,
			Credentials: credentials.NewStaticCredentialsProvider(rc.AccessKeyID, rc.SecretAccessKey, rc.SessionToken),
		}
		return bridgeCfg, rc, nil
	}

	assumedCfg, creds, err := assumeAdminRole(
		context.Background(),
		cfg,
		"arn:aws:iam::123456789012:root",
		"123456789012",
		testRegion,
	)
	if err != nil {
		t.Fatalf("assumeAdminRole: %v", err)
	}
	if !bridgeCalled {
		t.Fatal("expected temp-user bridge to be called for root caller")
	}
	if assumedCfg.Region != testRegion {
		t.Fatalf("region: want %q got %q", testRegion, assumedCfg.Region)
	}
	if creds.AccessKeyID != "ASSUMED" {
		t.Fatalf("access key: want ASSUMED got %q", creds.AccessKeyID)
	}
}
