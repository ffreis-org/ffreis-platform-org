package cmd

import (
	"reflect"
	"testing"

	"github.com/ffreis/platform-cli/pkg/audit"
)

func fullTags() map[string]string {
	return map[string]string{
		"CostCenter":  "platform",
		"Project":     "ffreis-platform",
		"Environment": "dev",
		"ManagedBy":   "terraform",
		"Stack":       "platform-org",
	}
}

func TestComputeTagCoverage_CoveredAndUncovered(t *testing.T) {
	rs := []audit.Resource{
		{ResourceType: "dynamodb:table", Name: "covered", ARN: "arn:aws:dynamodb:us-east-1:1:table/covered", Tags: fullTags()},
		{ResourceType: "s3", Name: "no-costcenter", ARN: "arn:aws:s3:::no-costcenter", Tags: map[string]string{
			"Project": "p", "Environment": "dev", "ManagedBy": "terraform", "Stack": "s",
		}},
	}

	view := computeTagCoverage(rs, costAllocationTagKeys)

	if view.Summary.Total != 2 || view.Summary.Covered != 1 || view.Summary.Uncovered != 1 || view.Summary.Skipped != 0 {
		t.Fatalf("unexpected summary: %+v", view.Summary)
	}
	if len(view.Gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(view.Gaps))
	}
	if got := view.Gaps[0].Missing; !reflect.DeepEqual(got, []string{"CostCenter"}) {
		t.Fatalf("expected missing [CostCenter], got %v", got)
	}
	if view.Gaps[0].Name != "no-costcenter" {
		t.Fatalf("unexpected gap resource: %s", view.Gaps[0].Name)
	}
}

func TestComputeTagCoverage_MissingMultipleSorted(t *testing.T) {
	rs := []audit.Resource{
		{ResourceType: "lambda", Name: "bare", ARN: "arn:aws:lambda:us-east-1:1:function:bare", Tags: map[string]string{
			"Project": "p", "Environment": "dev", "ManagedBy": "terraform", // CostCenter + Stack absent
		}},
		{ResourceType: "sns", Name: "empty-stack", ARN: "arn:aws:sns:us-east-1:1:empty-stack", Tags: map[string]string{
			"CostCenter": "platform", "Project": "p", "Environment": "dev", "ManagedBy": "terraform", "Stack": "", // empty == missing
		}},
	}

	view := computeTagCoverage(rs, costAllocationTagKeys)

	if view.Summary.Uncovered != 2 {
		t.Fatalf("expected 2 uncovered, got %d", view.Summary.Uncovered)
	}
	// Gaps are sorted by ResourceType then Name: lambda before sns.
	if view.Gaps[0].ResourceType != "lambda" || view.Gaps[1].ResourceType != "sns" {
		t.Fatalf("gaps not sorted by type: %+v", view.Gaps)
	}
	if got := view.Gaps[0].Missing; !reflect.DeepEqual(got, []string{"CostCenter", "Stack"}) {
		t.Fatalf("expected sorted missing [CostCenter Stack], got %v", got)
	}
	if got := view.Gaps[1].Missing; !reflect.DeepEqual(got, []string{"Stack"}) {
		t.Fatalf("expected empty Stack flagged, got %v", got)
	}
}

func TestComputeTagCoverage_SkipsUntaggable(t *testing.T) {
	rs := []audit.Resource{
		{ResourceType: "payments", Name: "instr", ARN: "arn:aws:payments::1:payment-instrument:abc", Tags: nil},
		{ResourceType: "s3", Name: "ok", ARN: "arn:aws:s3:::ok", Tags: fullTags()},
	}

	view := computeTagCoverage(rs, costAllocationTagKeys)

	if view.Summary.Skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", view.Summary.Skipped)
	}
	if view.Summary.Total != 1 || view.Summary.Covered != 1 || view.Summary.Uncovered != 0 {
		t.Fatalf("untaggable resource leaked into tally: %+v", view.Summary)
	}
	if len(view.Gaps) != 0 {
		t.Fatalf("expected no gaps, got %+v", view.Gaps)
	}
}

func TestComputeTagCoverage_Empty(t *testing.T) {
	view := computeTagCoverage(nil, costAllocationTagKeys)
	if view.Summary.Total != 0 || len(view.Gaps) != 0 {
		t.Fatalf("expected empty view, got %+v", view)
	}
	if !reflect.DeepEqual(view.Required, costAllocationTagKeys) {
		t.Fatalf("Required not echoed: %v", view.Required)
	}
}

func TestIsUntaggable(t *testing.T) {
	cases := map[string]struct {
		arn  string
		want bool
	}{
		"payment instrument": {"arn:aws:payments::1:payment-instrument:abc", true},
		"dynamodb table":     {"arn:aws:dynamodb:us-east-1:1:table/foo", false},
		"empty arn":          {"", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isUntaggable(audit.Resource{ARN: tc.arn}); got != tc.want {
				t.Fatalf("isUntaggable(%q) = %v, want %v", tc.arn, got, tc.want)
			}
		})
	}
}

func TestCostAllocationTagKeysIncludeCostCenter(t *testing.T) {
	var found bool
	for _, k := range costAllocationTagKeys {
		if k == "CostCenter" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CostCenter must be a required cost-allocation tag, got %v", costAllocationTagKeys)
	}
}

func TestResourcesHasUntaggedFlag(t *testing.T) {
	if resourcesCmd.Flags().Lookup("untagged") == nil {
		t.Fatal("resources command missing --untagged flag")
	}
}
