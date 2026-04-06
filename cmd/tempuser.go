package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	sdkcfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const (
	tempUserName   = "platform-bootstrap-temp"
	tempPolicyName = "platform-bootstrap-temp-policy"
)

// iamAPI is the minimal IAM surface needed for the temp-user bridge.
type iamAPI interface {
	GetUser(context.Context, *iam.GetUserInput, ...func(*iam.Options)) (*iam.GetUserOutput, error)
	CreateUser(context.Context, *iam.CreateUserInput, ...func(*iam.Options)) (*iam.CreateUserOutput, error)
	PutUserPolicy(context.Context, *iam.PutUserPolicyInput, ...func(*iam.Options)) (*iam.PutUserPolicyOutput, error)
	CreateAccessKey(context.Context, *iam.CreateAccessKeyInput, ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
	ListAccessKeys(context.Context, *iam.ListAccessKeysInput, ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error)
	DeleteAccessKey(context.Context, *iam.DeleteAccessKeyInput, ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error)
	DeleteUserPolicy(context.Context, *iam.DeleteUserPolicyInput, ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error)
	DeleteUser(context.Context, *iam.DeleteUserInput, ...func(*iam.Options)) (*iam.DeleteUserOutput, error)
}

type tempUserData struct {
	userName        string
	accessKeyID     string
	secretAccessKey string
}

var (
	newIAMClientFn = func(cfg sdkaws.Config) iamAPI { return iam.NewFromConfig(cfg) }

	// tempUserRetryDelays are the waits between AssumeRole attempts when using
	// fresh temp-user credentials. IAM credential and policy propagation takes
	// 5–15 seconds; retrying with increasing backoff avoids a hard failure on
	// the first attempt.
	tempUserRetryDelays = []time.Duration{
		3 * time.Second,
		5 * time.Second,
		8 * time.Second,
		10 * time.Second,
		12 * time.Second,
		15 * time.Second,
	}
)

// assumeRoleViaTempUser is the root→platform-admin bridge.
// AWS root cannot call sts:AssumeRole directly. This function creates a
// short-lived IAM user, uses its credentials to assume the target role with
// retry-backoff for IAM propagation delay, then deletes the user on exit.
func assumeRoleViaTempUser(ctx context.Context, initialCfg sdkaws.Config, roleARN, region string) (sdkaws.Config, rawCreds, error) {
	iamClient := newIAMClientFn(initialCfg)

	u, err := createTempUser(ctx, iamClient, roleARN)
	if err != nil {
		return sdkaws.Config{}, rawCreds{}, fmt.Errorf("creating temp user: %w", err)
	}
	d.log.Info("created temp user for role assumption", "user", u.userName)

	defer func() {
		if delErr := deleteTempUser(context.Background(), iamClient, u); delErr != nil {
			d.log.Warn("failed to delete temp user", "user", u.userName, "error", delErr)
		} else {
			d.log.Info("deleted temp user", "user", u.userName)
		}
	}()

	assumedCfg, creds, err := assumeRoleWithTempUserBackoff(ctx, u, roleARN, region)
	if err != nil {
		return sdkaws.Config{}, rawCreds{}, fmt.Errorf("assuming role via temp user: %w", err)
	}

	return assumedCfg, creds, nil
}

func assumeRoleWithTempUserBackoff(ctx context.Context, u tempUserData, roleARN, region string) (sdkaws.Config, rawCreds, error) {
	tempCfg, err := loadDefaultConfig(ctx,
		sdkcfg.WithRegion(region),
		sdkcfg.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(u.accessKeyID, u.secretAccessKey, ""),
		),
	)
	if err != nil {
		return sdkaws.Config{}, rawCreds{}, fmt.Errorf("building temp user config: %w", err)
	}

	stsClient := sts.NewFromConfig(tempCfg)

	var lastErr error
	for attempt := 0; ; attempt++ {
		out, assumeErr := stsClient.AssumeRole(ctx, &sts.AssumeRoleInput{
			RoleArn:         sdkaws.String(roleARN),
			RoleSessionName: sdkaws.String("platform-org-cli"),
			DurationSeconds: sdkaws.Int32(3600),
		})
		if assumeErr == nil {
			rc := rawCreds{
				AccessKeyID:     sdkaws.ToString(out.Credentials.AccessKeyId),
				SecretAccessKey: sdkaws.ToString(out.Credentials.SecretAccessKey),
				SessionToken:    sdkaws.ToString(out.Credentials.SessionToken),
				Region:          region,
			}
			assumedCfg, cfgErr := loadDefaultConfig(ctx,
				sdkcfg.WithRegion(region),
				sdkcfg.WithCredentialsProvider(
					credentials.NewStaticCredentialsProvider(rc.AccessKeyID, rc.SecretAccessKey, rc.SessionToken),
				),
			)
			if cfgErr != nil {
				return sdkaws.Config{}, rawCreds{}, fmt.Errorf("building assumed-role config: %w", cfgErr)
			}
			return assumedCfg, rc, nil
		}

		if !isTempUserPropagationErr(assumeErr) || attempt >= len(tempUserRetryDelays) {
			return sdkaws.Config{}, rawCreds{}, assumeErr
		}
		lastErr = assumeErr
		delay := tempUserRetryDelays[attempt]
		d.log.Info("waiting for IAM propagation", "attempt", attempt+1, "delay", delay)
		select {
		case <-ctx.Done():
			return sdkaws.Config{}, rawCreds{}, fmt.Errorf("context cancelled waiting for IAM propagation: %w (last: %v)", ctx.Err(), lastErr)
		case <-time.After(delay):
		}
	}
}

func isTempUserPropagationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "InvalidClientTokenId") || strings.Contains(msg, "AccessDenied")
}

func isIAMNoSuchEntity(err error) bool {
	var nse *iamtypes.NoSuchEntityException
	return errors.As(err, &nse)
}

func ensureTempUserExists(ctx context.Context, client iamAPI) error {
	_, err := client.GetUser(ctx, &iam.GetUserInput{UserName: sdkaws.String(tempUserName)})
	if err == nil {
		return nil
	}
	if !isIAMNoSuchEntity(err) {
		return fmt.Errorf("checking temp user: %w", err)
	}
	_, err = client.CreateUser(ctx, &iam.CreateUserInput{UserName: sdkaws.String(tempUserName)})
	if err == nil {
		return nil
	}
	var exists *iamtypes.EntityAlreadyExistsException
	if errors.As(err, &exists) {
		return nil
	}
	return fmt.Errorf("creating temp user: %w", err)
}

func deleteUserAccessKeys(ctx context.Context, client iamAPI, userName, deleteAction string) error {
	keys, err := client.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: sdkaws.String(userName)})
	if err != nil {
		if isIAMNoSuchEntity(err) {
			return nil
		}
		return fmt.Errorf("listing keys: %w", err)
	}
	for _, k := range keys.AccessKeyMetadata {
		if _, err := client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
			UserName:    sdkaws.String(userName),
			AccessKeyId: k.AccessKeyId,
		}); err != nil {
			if isIAMNoSuchEntity(err) {
				continue
			}
			return fmt.Errorf("%s: %w", deleteAction, err)
		}
	}
	return nil
}

// createTempUser creates (or reuses) the temp IAM user and returns fresh
// credentials. Any existing access keys are deleted before creating a new one
// to handle leftover keys from a previous partial run.
func createTempUser(ctx context.Context, client iamAPI, roleARN string) (tempUserData, error) {
	if err := ensureTempUserExists(ctx, client); err != nil {
		return tempUserData{}, err
	}

	if err := deleteUserAccessKeys(ctx, client, tempUserName, "deleting orphaned key"); err != nil {
		return tempUserData{}, err
	}

	policyDoc := fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Resource":%q}]}`,
		roleARN,
	)
	if _, err := client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       sdkaws.String(tempUserName),
		PolicyName:     sdkaws.String(tempPolicyName),
		PolicyDocument: sdkaws.String(policyDoc),
	}); err != nil {
		return tempUserData{}, fmt.Errorf("putting temp user policy: %w", err)
	}

	key, err := client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{
		UserName: sdkaws.String(tempUserName),
	})
	if err != nil {
		return tempUserData{}, fmt.Errorf("creating temp user access key: %w", err)
	}

	return tempUserData{
		userName:        tempUserName,
		accessKeyID:     sdkaws.ToString(key.AccessKey.AccessKeyId),
		secretAccessKey: sdkaws.ToString(key.AccessKey.SecretAccessKey),
	}, nil
}

// deleteTempUser removes all access keys, the inline policy, and the user.
// Safe to call when any component is already absent.
func deleteTempUser(ctx context.Context, client iamAPI, u tempUserData) error {
	if err := deleteUserAccessKeys(ctx, client, u.userName, "deleting access key"); err != nil {
		return err
	}

	if _, err := client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{
		UserName:   sdkaws.String(u.userName),
		PolicyName: sdkaws.String(tempPolicyName),
	}); err != nil {
		if !isIAMNoSuchEntity(err) {
			return fmt.Errorf("deleting temp user policy: %w", err)
		}
	}

	if _, err := client.DeleteUser(ctx, &iam.DeleteUserInput{
		UserName: sdkaws.String(u.userName),
	}); err != nil {
		if !isIAMNoSuchEntity(err) {
			return fmt.Errorf("deleting temp user: %w", err)
		}
	}

	return nil
}
