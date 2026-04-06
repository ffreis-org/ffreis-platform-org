package activation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/smithy-go"
)

// CostAllocationTags is the canonical list of tags to activate in AWS Cost Explorer.
// Both the CLI and the Lambda import this — the single source of truth.
var CostAllocationTags = []string{
	"Stack", "Project", "Layer", "Owner", "Environment",
}

// ErrNotReady is returned when AWS CE hasn't discovered the tag keys yet.
// This typically takes ~24 hours on a fresh account.
type ErrNotReady struct {
	Missing []string
}

func (e *ErrNotReady) Error() string {
	return fmt.Sprintf("cost allocation tags not ready yet (~24h AWS propagation): %v", e.Missing)
}

// CostAllocationTagsUpdater is the minimal CE surface required here, kept as an interface
// so it can be mocked in tests.
type CostAllocationTagsUpdater interface {
	UpdateCostAllocationTagsStatus(ctx context.Context,
		input *costexplorer.UpdateCostAllocationTagsStatusInput,
		optFns ...func(*costexplorer.Options),
	) (*costexplorer.UpdateCostAllocationTagsStatusOutput, error)
}

// Activate activates all CostAllocationTags in AWS Cost Explorer.
//
// It is idempotent: calling it when tags are already Active is a no-op that
// returns nil. If AWS hasn't yet discovered the tag keys (fresh account, ~24h
// propagation window), it returns *ErrNotReady. Any other error is returned
// as-is.
func Activate(ctx context.Context, client CostAllocationTagsUpdater) error {
	entries := make([]cetypes.CostAllocationTagStatusEntry, len(CostAllocationTags))
	for i, key := range CostAllocationTags {
		entries[i] = cetypes.CostAllocationTagStatusEntry{
			TagKey: sdkaws.String(key),
			Status: cetypes.CostAllocationTagStatusActive,
		}
	}

	out, err := client.UpdateCostAllocationTagsStatus(ctx,
		&costexplorer.UpdateCostAllocationTagsStatusInput{
			CostAllocationTagsStatus: entries,
		})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && strings.Contains(strings.ToLower(apiErr.ErrorCode()), "notfound") {
			return &ErrNotReady{Missing: CostAllocationTags}
		}
		return fmt.Errorf("UpdateCostAllocationTagsStatus: %w", err)
	}

	// The API may return partial failures inside the response body rather than
	// as a top-level error. Separate "not found yet" from genuine failures.
	var notReady, failed []string
	for _, e := range out.Errors {
		key := sdkaws.ToString(e.TagKey)
		if key == "" {
			continue
		}
		if strings.Contains(strings.ToLower(sdkaws.ToString(e.Code)), "notfound") ||
			strings.Contains(strings.ToLower(sdkaws.ToString(e.Message)), "not found") {
			notReady = append(notReady, key)
		} else {
			failed = append(failed, key+": "+sdkaws.ToString(e.Message))
		}
	}
	if len(notReady) > 0 {
		return &ErrNotReady{Missing: notReady}
	}
	if len(failed) > 0 {
		return fmt.Errorf("partial activation failure: %v", failed)
	}
	return nil
}
