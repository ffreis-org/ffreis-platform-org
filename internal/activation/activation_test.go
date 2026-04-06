package activation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/smithy-go"

	"github.com/ffreis/platform-org/internal/activation"
)

const errExpectedNonNil = "expected error, got nil"

// mockCE implements activation.CostAllocationTagsUpdater for testing.
type mockCE struct {
	out *costexplorer.UpdateCostAllocationTagsStatusOutput
	err error
}

func (m *mockCE) UpdateCostAllocationTagsStatus(_ context.Context,
	_ *costexplorer.UpdateCostAllocationTagsStatusInput,
	_ ...func(*costexplorer.Options),
) (*costexplorer.UpdateCostAllocationTagsStatusOutput, error) {
	return m.out, m.err
}

func TestActivate_HappyPath(t *testing.T) {
	mock := &mockCE{
		out: &costexplorer.UpdateCostAllocationTagsStatusOutput{},
	}
	if err := activation.Activate(context.Background(), mock); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestActivate_APIErrorNotFound(t *testing.T) {
	mock := &mockCE{
		err: &smithy.GenericAPIError{Code: "TagKeyNotFoundException", Message: "Tag keys not found: Stack, Project"},
	}
	err := activation.Activate(context.Background(), mock)
	if err == nil {
		t.Fatal(errExpectedNonNil)
	}
	var notReady *activation.ErrNotReady
	if !errors.As(err, &notReady) {
		t.Fatalf("expected *ErrNotReady, got %T: %v", err, err)
	}
	if len(notReady.Missing) == 0 {
		t.Fatal("ErrNotReady.Missing should be non-empty")
	}
}

func TestActivate_APIErrorGeneric(t *testing.T) {
	mock := &mockCE{
		err: &smithy.GenericAPIError{Code: "InternalServiceError", Message: "something went wrong"},
	}
	err := activation.Activate(context.Background(), mock)
	if err == nil {
		t.Fatal(errExpectedNonNil)
	}
	var notReady *activation.ErrNotReady
	if errors.As(err, &notReady) {
		t.Fatalf("expected generic error, got *ErrNotReady")
	}
}

func TestActivate_PartialNotFoundInResponse(t *testing.T) {
	mock := &mockCE{
		out: &costexplorer.UpdateCostAllocationTagsStatusOutput{
			Errors: []cetypes.UpdateCostAllocationTagsStatusError{
				{
					TagKey:  aws.String("Stack"),
					Code:    aws.String("NotFound"),
					Message: aws.String("tag key not found"),
				},
			},
		},
	}
	err := activation.Activate(context.Background(), mock)
	if err == nil {
		t.Fatal(errExpectedNonNil)
	}
	var notReady *activation.ErrNotReady
	if !errors.As(err, &notReady) {
		t.Fatalf("expected *ErrNotReady, got %T: %v", err, err)
	}
	if len(notReady.Missing) != 1 || notReady.Missing[0] != "Stack" {
		t.Fatalf("unexpected Missing: %v", notReady.Missing)
	}
}

func TestActivate_PartialOtherFailureInResponse(t *testing.T) {
	mock := &mockCE{
		out: &costexplorer.UpdateCostAllocationTagsStatusOutput{
			Errors: []cetypes.UpdateCostAllocationTagsStatusError{
				{
					TagKey:  aws.String("Owner"),
					Code:    aws.String("InvalidParameterValue"),
					Message: aws.String("invalid value"),
				},
			},
		},
	}
	err := activation.Activate(context.Background(), mock)
	if err == nil {
		t.Fatal(errExpectedNonNil)
	}
	var notReady *activation.ErrNotReady
	if errors.As(err, &notReady) {
		t.Fatalf("expected generic error, got *ErrNotReady")
	}
}

func TestActivate_Idempotent_AlreadyActive(t *testing.T) {
	// When tags are already active, AWS returns success with no errors.
	mock := &mockCE{
		out: &costexplorer.UpdateCostAllocationTagsStatusOutput{
			Errors: []cetypes.UpdateCostAllocationTagsStatusError{},
		},
	}
	if err := activation.Activate(context.Background(), mock); err != nil {
		t.Fatalf("expected nil for already-active tags, got %v", err)
	}
}

func TestCostAllocationTags_NotEmpty(t *testing.T) {
	if len(activation.CostAllocationTags) == 0 {
		t.Fatal("CostAllocationTags must not be empty")
	}
	for _, tag := range activation.CostAllocationTags {
		if tag == "" {
			t.Fatal("CostAllocationTags must not contain empty strings")
		}
	}
}
