package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

type fakeIAMAPI struct {
	getUserErr          error
	createUserErr       error
	putUserPolicyErr    error
	createAccessKeyErr  error
	listAccessKeysErr   error
	deleteUserPolicyErr error
	deleteUserErr       error
	deleteAccessKeyErrs map[string]error
	accessKeys          []iamtypes.AccessKeyMetadata
	putUserPolicyInput  *iam.PutUserPolicyInput
	createdUser         string
	deletedUsers        []string
	deletedPolicies     []string
	deletedAccessKeys   []string
}

func (f *fakeIAMAPI) GetUser(context.Context, *iam.GetUserInput, ...func(*iam.Options)) (*iam.GetUserOutput, error) {
	if f.getUserErr != nil {
		return nil, f.getUserErr
	}
	return &iam.GetUserOutput{}, nil
}

func (f *fakeIAMAPI) CreateUser(_ context.Context, input *iam.CreateUserInput, _ ...func(*iam.Options)) (*iam.CreateUserOutput, error) {
	f.createdUser = sdkaws.ToString(input.UserName)
	if f.createUserErr != nil {
		return nil, f.createUserErr
	}
	return &iam.CreateUserOutput{}, nil
}

func (f *fakeIAMAPI) PutUserPolicy(_ context.Context, input *iam.PutUserPolicyInput, _ ...func(*iam.Options)) (*iam.PutUserPolicyOutput, error) {
	f.putUserPolicyInput = input
	if f.putUserPolicyErr != nil {
		return nil, f.putUserPolicyErr
	}
	return &iam.PutUserPolicyOutput{}, nil
}

func (f *fakeIAMAPI) CreateAccessKey(_ context.Context, _ *iam.CreateAccessKeyInput, _ ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error) {
	if f.createAccessKeyErr != nil {
		return nil, f.createAccessKeyErr
	}
	return &iam.CreateAccessKeyOutput{AccessKey: &iamtypes.AccessKey{
		AccessKeyId:     sdkaws.String("AKIAFAKE"),
		SecretAccessKey: sdkaws.String("secret"),
	}}, nil
}

func (f *fakeIAMAPI) ListAccessKeys(context.Context, *iam.ListAccessKeysInput, ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error) {
	if f.listAccessKeysErr != nil {
		return nil, f.listAccessKeysErr
	}
	return &iam.ListAccessKeysOutput{AccessKeyMetadata: f.accessKeys}, nil
}

func (f *fakeIAMAPI) DeleteAccessKey(_ context.Context, input *iam.DeleteAccessKeyInput, _ ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error) {
	keyID := sdkaws.ToString(input.AccessKeyId)
	f.deletedAccessKeys = append(f.deletedAccessKeys, keyID)
	if err := f.deleteAccessKeyErrs[keyID]; err != nil {
		return nil, err
	}
	return &iam.DeleteAccessKeyOutput{}, nil
}

func (f *fakeIAMAPI) DeleteUserPolicy(_ context.Context, input *iam.DeleteUserPolicyInput, _ ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error) {
	f.deletedPolicies = append(f.deletedPolicies, sdkaws.ToString(input.PolicyName))
	if f.deleteUserPolicyErr != nil {
		return nil, f.deleteUserPolicyErr
	}
	return &iam.DeleteUserPolicyOutput{}, nil
}

func (f *fakeIAMAPI) DeleteUser(_ context.Context, input *iam.DeleteUserInput, _ ...func(*iam.Options)) (*iam.DeleteUserOutput, error) {
	f.deletedUsers = append(f.deletedUsers, sdkaws.ToString(input.UserName))
	if f.deleteUserErr != nil {
		return nil, f.deleteUserErr
	}
	return &iam.DeleteUserOutput{}, nil
}

func TestEnsureTempUserExistsCreatesMissingUser(t *testing.T) {
	t.Parallel()

	client := &fakeIAMAPI{getUserErr: &iamtypes.NoSuchEntityException{}}
	if err := ensureTempUserExists(context.Background(), client); err != nil {
		t.Fatalf("ensureTempUserExists: %v", err)
	}
	if client.createdUser != tempUserName {
		t.Fatalf("created user = %q, want %q", client.createdUser, tempUserName)
	}
}

func TestEnsureTempUserExistsWrapsUnexpectedError(t *testing.T) {
	t.Parallel()

	err := ensureTempUserExists(context.Background(), &fakeIAMAPI{getUserErr: errors.New("boom")})
	if err == nil || !strings.Contains(err.Error(), "checking temp user: boom") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureTempUserExistsIgnoresEntityAlreadyExists(t *testing.T) {
	t.Parallel()

	client := &fakeIAMAPI{
		getUserErr:    &iamtypes.NoSuchEntityException{},
		createUserErr: &iamtypes.EntityAlreadyExistsException{},
	}
	if err := ensureTempUserExists(context.Background(), client); err != nil {
		t.Fatalf("ensureTempUserExists: %v", err)
	}
}

func TestEnsureTempUserExistsWrapsCreateUserError(t *testing.T) {
	t.Parallel()

	err := ensureTempUserExists(context.Background(), &fakeIAMAPI{
		getUserErr:    &iamtypes.NoSuchEntityException{},
		createUserErr: errors.New("create denied"),
	})
	if err == nil || !strings.Contains(err.Error(), "creating temp user: create denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteUserAccessKeysIgnoresMissingEntities(t *testing.T) {
	t.Parallel()

	client := &fakeIAMAPI{
		accessKeys:          []iamtypes.AccessKeyMetadata{{AccessKeyId: sdkaws.String("A")}, {AccessKeyId: sdkaws.String("B")}},
		deleteAccessKeyErrs: map[string]error{"B": &iamtypes.NoSuchEntityException{}},
	}
	if err := deleteUserAccessKeys(context.Background(), client, tempUserName, "deleting access key"); err != nil {
		t.Fatalf("deleteUserAccessKeys: %v", err)
	}
	if len(client.deletedAccessKeys) != 2 {
		t.Fatalf("deleted access keys = %v, want both keys removed", client.deletedAccessKeys)
	}
}

func TestDeleteUserAccessKeysHandlesListNoSuchEntity(t *testing.T) {
	t.Parallel()

	if err := deleteUserAccessKeys(context.Background(), &fakeIAMAPI{listAccessKeysErr: &iamtypes.NoSuchEntityException{}}, tempUserName, "deleting access key"); err != nil {
		t.Fatalf("deleteUserAccessKeys: %v", err)
	}
}

func TestDeleteUserAccessKeysWrapsDeleteError(t *testing.T) {
	t.Parallel()

	err := deleteUserAccessKeys(context.Background(), &fakeIAMAPI{
		accessKeys:          []iamtypes.AccessKeyMetadata{{AccessKeyId: sdkaws.String("A")}},
		deleteAccessKeyErrs: map[string]error{"A": errors.New("denied")},
	}, tempUserName, "deleting access key")
	if err == nil || !strings.Contains(err.Error(), "deleting access key: denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateTempUserBuildsPolicyAndReturnsCredentials(t *testing.T) {
	t.Parallel()

	client := &fakeIAMAPI{
		getUserErr:          &iamtypes.NoSuchEntityException{},
		deleteAccessKeyErrs: map[string]error{},
	}
	data, err := createTempUser(context.Background(), client, "arn:aws:iam::123456789012:role/platform-admin")
	if err != nil {
		t.Fatalf("createTempUser: %v", err)
	}
	if data.userName != tempUserName || data.accessKeyID != "AKIAFAKE" || data.secretAccessKey != "secret" {
		t.Fatalf("unexpected temp user data: %#v", data)
	}
	if client.putUserPolicyInput == nil {
		t.Fatal("expected inline policy to be attached")
	}
	policyDoc := sdkaws.ToString(client.putUserPolicyInput.PolicyDocument)
	if !strings.Contains(policyDoc, "sts:AssumeRole") || !strings.Contains(policyDoc, "platform-admin") {
		t.Fatalf("policy doc = %q, want AssumeRole permission for target role", policyDoc)
	}
}

func TestCreateTempUserWrapsPutPolicyError(t *testing.T) {
	t.Parallel()

	_, err := createTempUser(context.Background(), &fakeIAMAPI{
		getUserErr:          &iamtypes.NoSuchEntityException{},
		deleteAccessKeyErrs: map[string]error{},
		putUserPolicyErr:    errors.New("policy denied"),
	}, "arn:aws:iam::123456789012:role/platform-admin")
	if err == nil || !strings.Contains(err.Error(), "putting temp user policy: policy denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateTempUserWrapsCreateAccessKeyError(t *testing.T) {
	t.Parallel()

	_, err := createTempUser(context.Background(), &fakeIAMAPI{
		getUserErr:          &iamtypes.NoSuchEntityException{},
		deleteAccessKeyErrs: map[string]error{},
		createAccessKeyErr:  errors.New("access key denied"),
	}, "arn:aws:iam::123456789012:role/platform-admin")
	if err == nil || !strings.Contains(err.Error(), "creating temp user access key: access key denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateTempUserReturnsEnsureUserError(t *testing.T) {
	t.Parallel()

	_, err := createTempUser(context.Background(), &fakeIAMAPI{getUserErr: errors.New("boom")}, "arn:aws:iam::123456789012:role/platform-admin")
	if err == nil || !strings.Contains(err.Error(), "checking temp user: boom") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateTempUserReturnsDeleteKeyError(t *testing.T) {
	t.Parallel()

	_, err := createTempUser(context.Background(), &fakeIAMAPI{
		accessKeys:          []iamtypes.AccessKeyMetadata{{AccessKeyId: sdkaws.String("A")}},
		deleteAccessKeyErrs: map[string]error{"A": errors.New("key denied")},
	}, "arn:aws:iam::123456789012:role/platform-admin")
	if err == nil || !strings.Contains(err.Error(), "deleting orphaned key: key denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteTempUserIgnoresMissingPolicyAndUser(t *testing.T) {
	t.Parallel()

	client := &fakeIAMAPI{
		deleteAccessKeyErrs: map[string]error{},
		accessKeys:          []iamtypes.AccessKeyMetadata{{AccessKeyId: sdkaws.String("A")}},
		deleteUserPolicyErr: &iamtypes.NoSuchEntityException{},
		deleteUserErr:       &iamtypes.NoSuchEntityException{},
	}
	err := deleteTempUser(context.Background(), client, tempUserData{userName: tempUserName})
	if err != nil {
		t.Fatalf("deleteTempUser: %v", err)
	}
	if len(client.deletedAccessKeys) != 1 || len(client.deletedPolicies) != 1 || len(client.deletedUsers) != 1 {
		t.Fatalf("unexpected delete calls: keys=%v policies=%v users=%v", client.deletedAccessKeys, client.deletedPolicies, client.deletedUsers)
	}
}

func TestDeleteTempUserWrapsPolicyDeleteError(t *testing.T) {
	t.Parallel()

	err := deleteTempUser(context.Background(), &fakeIAMAPI{
		deleteAccessKeyErrs: map[string]error{},
		deleteUserPolicyErr: errors.New("policy denied"),
	}, tempUserData{userName: tempUserName})
	if err == nil || !strings.Contains(err.Error(), "deleting temp user policy: policy denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteTempUserWrapsDeleteUserError(t *testing.T) {
	t.Parallel()

	err := deleteTempUser(context.Background(), &fakeIAMAPI{
		deleteAccessKeyErrs: map[string]error{},
		deleteUserErr:       errors.New("delete denied"),
	}, tempUserData{userName: tempUserName})
	if err == nil || !strings.Contains(err.Error(), "deleting temp user: delete denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteTempUserWrapsDeleteAccessKeyError(t *testing.T) {
	t.Parallel()

	err := deleteTempUser(context.Background(), &fakeIAMAPI{
		accessKeys:          []iamtypes.AccessKeyMetadata{{AccessKeyId: sdkaws.String("A")}},
		deleteAccessKeyErrs: map[string]error{"A": errors.New("key denied")},
	}, tempUserData{userName: tempUserName})
	if err == nil || !strings.Contains(err.Error(), "deleting access key: key denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsTempUserPropagationErr(t *testing.T) {
	t.Parallel()

	if isTempUserPropagationErr(nil) {
		t.Fatal("nil error must not be treated as propagation error")
	}
	if !isTempUserPropagationErr(errors.New("InvalidClientTokenId: not ready")) {
		t.Fatal("expected InvalidClientTokenId to be treated as propagation error")
	}
	if !isTempUserPropagationErr(errors.New("AccessDenied: not ready")) {
		t.Fatal("expected AccessDenied to be treated as propagation error")
	}
	if isTempUserPropagationErr(errors.New("ValidationException")) {
		t.Fatal("unexpected propagation classification")
	}
}
