package cmd

import (
	"context"
	"errors"
	"io"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	sdkcfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
)

type mockSTSClient struct {
	getCallerIdentity func(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
	assumeRole        func(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error)
}

func (m mockSTSClient) GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if m.getCallerIdentity == nil {
		return nil, errors.New("unexpected GetCallerIdentity call")
	}
	return m.getCallerIdentity(ctx, in, optFns...)
}

func (m mockSTSClient) AssumeRole(ctx context.Context, in *sts.AssumeRoleInput, optFns ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	if m.assumeRole == nil {
		return nil, errors.New("unexpected AssumeRole call")
	}
	return m.assumeRole(ctx, in, optFns...)
}

type errCredentialsProvider struct{ err error }

func (p errCredentialsProvider) Retrieve(context.Context) (sdkaws.Credentials, error) {
	return sdkaws.Credentials{}, p.err
}

func resetRootSeams(t *testing.T) {
	t.Helper()
	oldLoadDefault := loadDefaultConfig
	oldLoadAWS := loadAWSConfigFn
	oldAssume := assumeAdminRoleFn
	oldSTS := newSTSClient
	oldTagging := newTaggingClient
	oldBudgets := newBudgetsClient
	oldCE := newCEClient
	oldOut := rootCmd.OutOrStdout()
	oldErr := rootCmd.ErrOrStderr()
	t.Cleanup(func() {
		loadDefaultConfig = oldLoadDefault
		loadAWSConfigFn = oldLoadAWS
		assumeAdminRoleFn = oldAssume
		newSTSClient = oldSTS
		newTaggingClient = oldTagging
		newBudgetsClient = oldBudgets
		newCEClient = oldCE
		rootCmd.SetOut(oldOut)
		rootCmd.SetErr(oldErr)
		rootCmd.SetArgs(nil)
	})
}

func TestExecuteHelp(t *testing.T) {
	resetRootSeams(t)
	rootCmd.SetArgs([]string{"--help"})
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)

	if code := Execute(); code != exitOK {
		t.Fatalf("Execute() code = %d, want %d", code, exitOK)
	}
}

func TestLoadAWSConfigWithProfile(t *testing.T) {
	resetRootSeams(t)
	called := false
	loadDefaultConfig = func(context.Context, ...func(*sdkcfg.LoadOptions) error) (sdkaws.Config, error) {
		called = true
		return sdkaws.Config{Region: testRegion}, nil
	}

	cfg, err := loadAWSConfig(context.Background(), "dev", testRegion)
	if err != nil {
		t.Fatalf("loadAWSConfig: %v", err)
	}
	if !called || cfg.Region != testRegion {
		t.Fatalf("unexpected result: called=%v region=%q", called, cfg.Region)
	}
}

func TestLoadAWSConfigWrapsLoaderError(t *testing.T) {
	resetRootSeams(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "env-secret")
	loadDefaultConfig = func(context.Context, ...func(*sdkcfg.LoadOptions) error) (sdkaws.Config, error) {
		return sdkaws.Config{}, errors.New("loader failed")
	}

	_, err := loadAWSConfig(context.Background(), "", testRegion)
	if err == nil || err.Error() != "loading AWS config: loader failed" {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestPersistentPreRunESuccess(t *testing.T) {
	resetRootSeams(t)
	d.region = testRegion
	d.logLevel = "error"
	loadAWSConfigFn = func(context.Context, string, string) (sdkaws.Config, error) {
		return sdkaws.Config{Region: testRegion}, nil
	}
	newSTSClient = func(sdkaws.Config) stsAPI {
		return mockSTSClient{getCallerIdentity: func(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
			return &sts.GetCallerIdentityOutput{
				Account: sdkaws.String(testAccountID),
				Arn:     sdkaws.String(testUserARN),
			}, nil
		}}
	}
	assumeAdminRoleFn = func(context.Context, sdkaws.Config, string, string, string) (sdkaws.Config, rawCreds, error) {
		return sdkaws.Config{Region: testRegion}, rawCreds{AccessKeyID: "AKIA", Region: testRegion}, nil
	}
	newTaggingClient = func(sdkaws.Config, ...func(*resourcegroupstaggingapi.Options)) *resourcegroupstaggingapi.Client {
		return &resourcegroupstaggingapi.Client{}
	}
	newBudgetsClient = func(sdkaws.Config, ...func(*budgets.Options)) *budgets.Client {
		return &budgets.Client{}
	}
	newCEClient = func(sdkaws.Config, ...func(*costexplorer.Options)) *costexplorer.Client {
		return &costexplorer.Client{}
	}

	if err := rootCmd.PersistentPreRunE(rootCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}
	if d.accountID != testAccountID || d.callerARN == "" {
		t.Fatalf("identity not captured: account=%q arn=%q", d.accountID, d.callerARN)
	}
	if d.creds.AccessKeyID != "AKIA" || d.tagging == nil || d.budgets == nil || d.ce == nil {
		t.Fatalf("deps not initialised: creds=%#v tagging=%v budgets=%v ce=%v", d.creds, d.tagging, d.budgets, d.ce)
	}
}

func TestPersistentPreRunEIdentityError(t *testing.T) {
	resetRootSeams(t)
	d.region = testRegion
	d.logLevel = "error"
	loadAWSConfigFn = func(context.Context, string, string) (sdkaws.Config, error) {
		return sdkaws.Config{Region: testRegion}, nil
	}
	newSTSClient = func(sdkaws.Config) stsAPI {
		return mockSTSClient{getCallerIdentity: func(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
			return nil, errors.New("identity failed")
		}}
	}

	err := rootCmd.PersistentPreRunE(rootCmd, nil)
	if err == nil || err.Error() != "verifying AWS credentials: identity failed" {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestAssumeAdminRoleInitialCredentialError(t *testing.T) {
	d.log = newLogger("error")
	cfg := sdkaws.Config{Credentials: errCredentialsProvider{err: errors.New("retrieve failed")}}

	_, _, err := assumeAdminRole(context.Background(), cfg, testUserARN, testAccountID, testRegion)
	if err == nil || err.Error() != "retrieving initial credentials: retrieve failed" {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestAssumeAdminRoleAssumeRoleError(t *testing.T) {
	resetRootSeams(t)
	d.log = newLogger("error")
	cfg := sdkaws.Config{
		Region:      testRegion,
		Credentials: credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
	}
	newSTSClient = func(sdkaws.Config) stsAPI {
		return mockSTSClient{assumeRole: func(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
			return nil, errors.New("assume failed")
		}}
	}

	_, _, err := assumeAdminRole(context.Background(), cfg, testUserARN, testAccountID, testRegion)
	if err == nil || err.Error() != "assuming platform-admin role: assume failed" {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestAssumeAdminRoleSuccess(t *testing.T) {
	resetRootSeams(t)
	d.log = newLogger("error")
	cfg := sdkaws.Config{
		Region:      testRegion,
		Credentials: credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
	}
	loadDefaultConfig = func(context.Context, ...func(*sdkcfg.LoadOptions) error) (sdkaws.Config, error) {
		return sdkaws.Config{Region: testRegion}, nil
	}
	newSTSClient = func(sdkaws.Config) stsAPI {
		return mockSTSClient{assumeRole: func(_ context.Context, input *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
			if sdkaws.ToString(input.RoleArn) != "arn:aws:iam::123456789012:role/platform-admin" {
				t.Fatalf("role arn: %q", sdkaws.ToString(input.RoleArn))
			}
			return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
				AccessKeyId:     sdkaws.String("ASSUMED"),
				SecretAccessKey: sdkaws.String("assumed-secret"),
				SessionToken:    sdkaws.String("assumed-token"),
			}}, nil
		}}
	}

	assumedCfg, creds, err := assumeAdminRole(context.Background(), cfg, testUserARN, testAccountID, testRegion)
	if err != nil {
		t.Fatalf("assumeAdminRole: %v", err)
	}
	if assumedCfg.Region != testRegion {
		t.Fatalf("region: want %q got %q", testRegion, assumedCfg.Region)
	}
	if creds.AccessKeyID != "ASSUMED" || creds.Region != testRegion {
		t.Fatalf("unexpected creds: %#v", creds)
	}
}
