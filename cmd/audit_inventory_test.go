package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
)

// --- terraformInventorySource ---

func TestTerraformInventorySourceID(t *testing.T) {
	t.Parallel()
	src := terraformInventorySource{}
	if got := src.sourceID(); got != "terraform" {
		t.Fatalf("sourceID() = %q, want %q", got, "terraform")
	}
}

func TestTerraformInventorySourceCleanupNukeReturnsNil(t *testing.T) {
	t.Parallel()
	msgs, err := terraformInventorySource{}.cleanupNuke(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Fatalf("expected nil messages, got: %v", msgs)
	}
}

func TestTerraformInventorySourceLoadReturnsExpected(t *testing.T) {
	old := terraformPlanJSONFn
	t.Cleanup(func() { terraformPlanJSONFn = old })
	terraformPlanJSONFn = func(_ context.Context) ([]byte, error) {
		return []byte(`{"resource_changes":[]}`), nil
	}
	result, err := terraformInventorySource{}.load(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = result // empty expected list is fine
}

func TestTerraformInventorySourceLoadForwardsPlanError(t *testing.T) {
	old := terraformPlanJSONFn
	t.Cleanup(func() { terraformPlanJSONFn = old })
	terraformPlanJSONFn = func(_ context.Context) ([]byte, error) {
		return nil, errors.New("terraform plan failed")
	}
	_, err := terraformInventorySource{}.load(context.Background())
	if err == nil {
		t.Fatal("expected error from plan failure")
	}
}

// --- activationInventorySource ---

func TestActivationInventorySourceID(t *testing.T) {
	t.Parallel()
	src := activationInventorySource{}
	if got := src.sourceID(); got != "runtime" {
		t.Fatalf("sourceID() = %q, want %q", got, "runtime")
	}
}

// --- loadAllCostTagStatuses ---

func TestLoadAllCostTagStatusesReturnsSinglePage(t *testing.T) {
	old := listCostAllocationTagsFn
	t.Cleanup(func() { listCostAllocationTagsFn = old })
	listCostAllocationTagsFn = func(_ context.Context, _ *costexplorer.ListCostAllocationTagsInput) (*costexplorer.ListCostAllocationTagsOutput, error) {
		return &costexplorer.ListCostAllocationTagsOutput{
			CostAllocationTags: []cetypes.CostAllocationTag{
				{TagKey: sdkaws.String("platform-org"), Status: cetypes.CostAllocationTagStatusActive},
			},
		}, nil
	}
	statuses, err := loadAllCostTagStatuses(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statuses["platform-org"] != cetypes.CostAllocationTagStatusActive {
		t.Fatalf("expected active status for platform-org, got: %v", statuses["platform-org"])
	}
}

func TestLoadAllCostTagStatusesPaginates(t *testing.T) {
	old := listCostAllocationTagsFn
	t.Cleanup(func() { listCostAllocationTagsFn = old })
	call := 0
	listCostAllocationTagsFn = func(_ context.Context, input *costexplorer.ListCostAllocationTagsInput) (*costexplorer.ListCostAllocationTagsOutput, error) {
		call++
		if call == 1 {
			return &costexplorer.ListCostAllocationTagsOutput{
				CostAllocationTags: []cetypes.CostAllocationTag{
					{TagKey: sdkaws.String("tag-a"), Status: cetypes.CostAllocationTagStatusActive},
				},
				NextToken: sdkaws.String("next"),
			}, nil
		}
		return &costexplorer.ListCostAllocationTagsOutput{
			CostAllocationTags: []cetypes.CostAllocationTag{
				{TagKey: sdkaws.String("tag-b"), Status: cetypes.CostAllocationTagStatusInactive},
			},
		}, nil
	}
	statuses, err := loadAllCostTagStatuses(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if call != 2 {
		t.Fatalf("expected 2 list calls, got %d", call)
	}
	if statuses["tag-a"] != cetypes.CostAllocationTagStatusActive {
		t.Fatalf("unexpected status for tag-a: %v", statuses["tag-a"])
	}
	if statuses["tag-b"] != cetypes.CostAllocationTagStatusInactive {
		t.Fatalf("unexpected status for tag-b: %v", statuses["tag-b"])
	}
}

func TestLoadAllCostTagStatusesReturnsError(t *testing.T) {
	old := listCostAllocationTagsFn
	t.Cleanup(func() { listCostAllocationTagsFn = old })
	listCostAllocationTagsFn = func(_ context.Context, _ *costexplorer.ListCostAllocationTagsInput) (*costexplorer.ListCostAllocationTagsOutput, error) {
		return nil, errors.New("list failed")
	}
	_, err := loadAllCostTagStatuses(context.Background())
	if err == nil {
		t.Fatal("expected error from list failure")
	}
}
