package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	sdkcfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	platformui "github.com/ffreis/platform-org/internal/ui"
)

const (
	exitOK    = 0
	exitError = 1
)

type stsAPI interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
	AssumeRole(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error)
}

var (
	loadDefaultConfig       = sdkcfg.LoadDefaultConfig
	loadAWSConfigFn         = loadAWSConfig
	assumeAdminRoleFn       = assumeAdminRole
	assumeRoleViaTempUserFn = assumeRoleViaTempUser
	newSTSClient            = func(cfg sdkaws.Config) stsAPI { return sts.NewFromConfig(cfg) }
	newTaggingClient        = resourcegroupstaggingapi.NewFromConfig
	newBudgetsClient        = budgets.NewFromConfig
	newCEClient             = costexplorer.NewFromConfig
)

// deps holds shared state built once in PersistentPreRunE and read by all
// subcommands. Package-scoped for simplicity within this single-package CLI.
var d struct {
	profile   string
	region    string
	logLevel  string
	env       string
	uiMode    string
	org       string
	accountID string
	callerARN string
	creds     rawCreds
	awsCfg    sdkaws.Config
	tagging   *resourcegroupstaggingapi.Client
	budgets   *budgets.Client
	ce        *costexplorer.Client
	log       *slog.Logger
	ui        *platformui.Presenter
}

// rawCreds are the actual static credentials injected into terraform's env.
type rawCreds struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Region          string
}

// toEnv returns the five AWS_* env vars suitable for injecting into a subprocess.
func (c rawCreds) toEnv() map[string]string {
	return map[string]string{
		"AWS_ACCESS_KEY_ID":     c.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY": c.SecretAccessKey,
		"AWS_SESSION_TOKEN":     c.SessionToken,
		"AWS_REGION":            c.Region,
		"AWS_DEFAULT_REGION":    c.Region,
	}
}

// localCommandAnnotation is the annotation key used to mark commands that
// do not require AWS credentials so PersistentPreRunE can skip role assumption.
const localCommandAnnotation = "local"

var rootCmd = &cobra.Command{
	Use:           "platform-org",
	Short:         "Manage the platform organization Terraform stack",
	SilenceErrors: true,
	SilenceUsage:  true,
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()

		presenter, err := platformui.New(d.uiMode)
		if err != nil {
			return err
		}
		d.ui = presenter
		ctx = platformui.WithPresenter(ctx, presenter)
		cmd.SetContext(ctx)

		d.log = newLogger(d.logLevel)

		// Skip AWS credential loading for local commands (e.g. version) so
		// they work without any AWS configuration.
		if cmd.Annotations[localCommandAnnotation] == "true" {
			return nil
		}

		awsCfg, err := loadAWSConfigFn(ctx, d.profile, d.region)
		if err != nil {
			return err
		}

		stsClient := newSTSClient(awsCfg)
		identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		if err != nil {
			return fmt.Errorf("verifying AWS credentials: %w", err)
		}
		d.accountID = sdkaws.ToString(identity.Account)
		d.callerARN = sdkaws.ToString(identity.Arn)

		d.log.Info("credentials verified",
			"account_id", d.accountID,
			"caller_arn", d.callerARN,
			"region", d.region,
		)

		assumedCfg, assumedCreds, err := assumeAdminRoleFn(ctx, awsCfg, d.callerARN, d.accountID, d.region)
		if err != nil {
			return err
		}

		d.creds = assumedCreds
		d.awsCfg = assumedCfg
		d.tagging = newTaggingClient(assumedCfg)
		d.budgets = newBudgetsClient(assumedCfg)
		d.ce = newCEClient(assumedCfg)

		return nil
	},
}

// Execute runs the root Cobra command and returns the process exit code.
func Execute() int {
	return executeCommand(rootCmd, os.Stderr)
}

func executeCommand(cmd *cobra.Command, stderr io.Writer) int {
	if err := cmd.Execute(); err != nil {
		if message := err.Error(); message != "" {
			_, _ = io.WriteString(stderr, "error: "+message+"\n")
		}
		return exitError
	}
	return exitOK
}

// loadAWSConfig builds an aws.Config. When profile is set, it uses a named
// profile. When AWS_ACCESS_KEY_ID is set, it picks up ambient env vars.
// Any other case is an error — no silent IMDS fallback.
func loadAWSConfig(ctx context.Context, profile, region string) (sdkaws.Config, error) {
	opts := []func(*sdkcfg.LoadOptions) error{
		sdkcfg.WithRegion(region),
	}
	switch {
	case profile != "":
		opts = append(opts, sdkcfg.WithSharedConfigProfile(profile))
	case os.Getenv("AWS_ACCESS_KEY_ID") != "":
		// SDK picks up env vars automatically — no extra option needed.
	default:
		return sdkaws.Config{}, errors.New(
			"no AWS credentials: set --profile or AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY",
		)
	}
	cfg, err := loadDefaultConfig(ctx, opts...)
	if err != nil {
		return sdkaws.Config{}, fmt.Errorf("loading AWS config: %w", err)
	}
	return cfg, nil
}

// assumeAdminRole assumes the platform-admin IAM role and returns a new
// aws.Config and the raw static credentials for subprocess injection.
// If already running as platform-admin (assumed-role ARN) it is a no-op.
func assumeAdminRole(ctx context.Context, cfg sdkaws.Config, callerARN, accountID, region string) (sdkaws.Config, rawCreds, error) {
	initCreds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return sdkaws.Config{}, rawCreds{}, fmt.Errorf("retrieving initial credentials: %w", err)
	}
	initial := rawCreds{
		AccessKeyID:     initCreds.AccessKeyID,
		SecretAccessKey: initCreds.SecretAccessKey,
		SessionToken:    initCreds.SessionToken,
		Region:          region,
	}

	if strings.Contains(callerARN, "assumed-role/platform-admin/") {
		d.log.Debug("already running as platform-admin; skipping assumption")
		return cfg, initial, nil
	}

	roleARN := fmt.Sprintf("arn:aws:iam::%s:role/platform-admin", accountID)

	if strings.HasSuffix(callerARN, ":root") {
		d.log.Info("running as root; using temp-user bridge to assume platform-admin")
		return assumeRoleViaTempUserFn(ctx, cfg, roleARN, region)
	}

	stsClient := newSTSClient(cfg)
	out, err := stsClient.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         sdkaws.String(roleARN),
		RoleSessionName: sdkaws.String("platform-org-cli"),
		DurationSeconds: sdkaws.Int32(3600),
	})
	if err != nil {
		return sdkaws.Config{}, rawCreds{}, fmt.Errorf("assuming platform-admin role: %w", err)
	}

	cred := out.Credentials
	rc := rawCreds{
		AccessKeyID:     sdkaws.ToString(cred.AccessKeyId),
		SecretAccessKey: sdkaws.ToString(cred.SecretAccessKey),
		SessionToken:    sdkaws.ToString(cred.SessionToken),
		Region:          region,
	}

	assumedCfg, err := loadDefaultConfig(ctx,
		sdkcfg.WithRegion(region),
		sdkcfg.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(rc.AccessKeyID, rc.SecretAccessKey, rc.SessionToken),
		),
	)
	if err != nil {
		return sdkaws.Config{}, rawCreds{}, fmt.Errorf("building assumed-role config: %w", err)
	}

	d.log.Info("assumed platform-admin role", "caller_arn", roleARN)
	return assumedCfg, rc, nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func init() {
	f := rootCmd.PersistentFlags()
	f.StringVar(&d.profile, "profile", "", "AWS named profile (or use AWS_ACCESS_KEY_ID env vars)")
	f.StringVar(&d.region, "region", "us-east-1", "AWS region")
	f.StringVar(&d.logLevel, "log-level", "info", "Log level: debug, info, warn, error")
	f.StringVar(&d.env, "env", "prod", "Environment: prod, staging, dev")
	f.StringVar(&d.uiMode, "ui", "auto", "UI mode: auto, plain, rich")
	f.StringVar(&d.org, "org", "ffreis", "Organisation name (used to construct resource names)")

	rootCmd.AddCommand(versionCmd)
}
