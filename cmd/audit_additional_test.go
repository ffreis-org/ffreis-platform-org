package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	budgettypes "github.com/aws/aws-sdk-go-v2/service/budgets/types"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	taggingtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	"github.com/ffreis/platform-org/internal/activation"
)

type stubInventorySource struct {
	id        string
	loadFn    func(context.Context) (inventorySourceResult, error)
	cleanupFn func(context.Context) ([]string, error)
}

func (s stubInventorySource) sourceID() string { return s.id }

func (s stubInventorySource) load(ctx context.Context) (inventorySourceResult, error) {
	if s.loadFn == nil {
		return inventorySourceResult{}, nil
	}
	return s.loadFn(ctx)
}

func (s stubInventorySource) cleanupNuke(ctx context.Context) ([]string, error) {
	if s.cleanupFn == nil {
		return nil, nil
	}
	return s.cleanupFn(ctx)
}

func captureAuditOutput(t *testing.T) *bytes.Buffer {
	t.Helper()
	old := auditStdout
	var buf bytes.Buffer
	auditStdout = &buf
	t.Cleanup(func() {
		auditStdout = old
	})
	return &buf
}

func TestScanResourcesHandlesPagination(t *testing.T) {
	old := getResourcesPage
	defer func() { getResourcesPage = old }()

	calls := 0
	getResourcesPage = func(_ context.Context, input *resourcegroupstaggingapi.GetResourcesInput) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
		calls++
		switch calls {
		case 1:
			if sdkaws.ToString(input.PaginationToken) != "" {
				t.Fatalf("first page token: want empty got %q", sdkaws.ToString(input.PaginationToken))
			}
			return &resourcegroupstaggingapi.GetResourcesOutput{
				PaginationToken: sdkaws.String("next-page"),
				ResourceTagMappingList: []taggingtypes.ResourceTagMapping{
					testResourceTagMapping(
						"arn:aws:s3:::ffreis-tf-state-runtime",
						testTag("Stack", "platform-org"),
						testTag("Project", "platform"),
						testTag("Environment", testEnv),
						testTag("ManagedBy", "terraform"),
					),
				},
			}, nil
		case 2:
			if sdkaws.ToString(input.PaginationToken) != "next-page" {
				t.Fatalf("second page token: want next-page got %q", sdkaws.ToString(input.PaginationToken))
			}
			return &resourcegroupstaggingapi.GetResourcesOutput{
				ResourceTagMappingList: []taggingtypes.ResourceTagMapping{
					testResourceTagMapping(
						"arn:aws:s3:::manual-bucket",
						testTag("Name", "manual"),
					),
				},
			}, nil
		default:
			t.Fatalf("unexpected call %d", calls)
			return nil, nil
		}
	}

	got, err := scanResources(context.Background())
	if err != nil {
		t.Fatalf("scanResources: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("resources: want 2 got %d", len(got))
	}
	if got[0].status != "OK" || got[1].status != "UNOWNED" {
		t.Fatalf("unexpected statuses: %#v", got)
	}
}

func TestScanResourcesReturnsWrappedError(t *testing.T) {
	old := getResourcesPage
	defer func() { getResourcesPage = old }()
	getResourcesPage = func(context.Context, *resourcegroupstaggingapi.GetResourcesInput) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
		return nil, errors.New("tagging failed")
	}

	_, err := scanResources(context.Background())
	if err == nil || !strings.Contains(err.Error(), "GetResources: tagging failed") {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestPrintAuditSectionRendersDefaultsAndTruncates(t *testing.T) {
	buf := captureAuditOutput(t)
	printAuditSection(newWriterOutput(buf, buf, nil), "Expected Platform Org Resources", []auditResource{{
		status:       "WARN",
		resourceType: "very-long-resource-type-that-should-truncate",
		name:         "very-long-resource-name-that-should-also-truncate",
		issues:       []string{"missing tag: Project", "missing tag: Stack"},
	}})

	out := buf.String()
	if !strings.Contains(out, "Expected Platform Org Resources") || !strings.Contains(out, "STATUS") || !strings.Contains(out, "ISSUES") {
		t.Fatalf("missing table header: %q", out)
	}
	if !strings.Contains(out, "(no tag)") {
		t.Fatalf("missing default stack label: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("expected truncated output with ellipsis: %q", out)
	}
	if !strings.Contains(out, "missing tag: Project; missing tag: Stack") {
		t.Fatalf("missing joined issues: %q", out)
	}
}

func TestPrintBudgetsReportsError(t *testing.T) {
	buf := captureAuditOutput(t)
	old := describeBudgetsFn
	defer func() { describeBudgetsFn = old }()
	describeBudgetsFn = func(context.Context, *budgets.DescribeBudgetsInput) (*budgets.DescribeBudgetsOutput, error) {
		return nil, errors.New("boom")
	}

	printBudgets(context.Background())
	if !strings.Contains(buf.String(), "could not list: boom") {
		t.Fatalf(errUnexpectedOutput, buf.String())
	}
}

func TestPrintBudgetsReportsEmpty(t *testing.T) {
	buf := captureAuditOutput(t)
	old := describeBudgetsFn
	defer func() { describeBudgetsFn = old }()
	describeBudgetsFn = func(context.Context, *budgets.DescribeBudgetsInput) (*budgets.DescribeBudgetsOutput, error) {
		return &budgets.DescribeBudgetsOutput{}, nil
	}

	printBudgets(context.Background())
	if !strings.Contains(buf.String(), "none found") {
		t.Fatalf(errUnexpectedOutput, buf.String())
	}
}

func TestPrintBudgetsReportsSuccess(t *testing.T) {
	buf := captureAuditOutput(t)
	old := describeBudgetsFn
	defer func() { describeBudgetsFn = old }()
	d.accountID = testAccountID
	describeBudgetsFn = func(_ context.Context, input *budgets.DescribeBudgetsInput) (*budgets.DescribeBudgetsOutput, error) {
		if sdkaws.ToString(input.AccountId) != d.accountID {
			t.Fatalf("account id: want %s got %s", d.accountID, sdkaws.ToString(input.AccountId))
		}
		return &budgets.DescribeBudgetsOutput{
			Budgets: []budgettypes.Budget{{
				BudgetName:  sdkaws.String("platform-budget"),
				BudgetLimit: &budgettypes.Spend{Amount: sdkaws.String("100.00")},
			}},
		}, nil
	}

	printBudgets(context.Background())
	if !strings.Contains(buf.String(), "platform-budget") || !strings.Contains(buf.String(), "$100.00/month") {
		t.Fatalf(errUnexpectedOutput, buf.String())
	}
}

func TestLoadActiveCostTagsHandlesPagination(t *testing.T) {
	old := listCostAllocationTagsFn
	defer func() { listCostAllocationTagsFn = old }()
	calls := 0
	listCostAllocationTagsFn = func(_ context.Context, input *costexplorer.ListCostAllocationTagsInput) (*costexplorer.ListCostAllocationTagsOutput, error) {
		calls++
		switch calls {
		case 1:
			if input.Status != cetypes.CostAllocationTagStatusActive {
				t.Fatalf("unexpected status: %s", input.Status)
			}
			return &costexplorer.ListCostAllocationTagsOutput{
				CostAllocationTags: []cetypes.CostAllocationTag{{TagKey: sdkaws.String("Stack")}},
				NextToken:          sdkaws.String("next-token"),
			}, nil
		case 2:
			if sdkaws.ToString(input.NextToken) != "next-token" {
				t.Fatalf("next token: want next-token got %q", sdkaws.ToString(input.NextToken))
			}
			return &costexplorer.ListCostAllocationTagsOutput{
				CostAllocationTags: []cetypes.CostAllocationTag{{TagKey: sdkaws.String("Project")}},
			}, nil
		default:
			t.Fatalf("unexpected call %d", calls)
			return nil, nil
		}
	}

	active, err := loadActiveCostTags(context.Background())
	if err != nil {
		t.Fatalf("loadActiveCostTags: %v", err)
	}
	if !active["Stack"] || !active["Project"] {
		t.Fatalf("unexpected active tags: %#v", active)
	}
}

func TestLoadActiveCostTagsReturnsError(t *testing.T) {
	old := listCostAllocationTagsFn
	defer func() { listCostAllocationTagsFn = old }()
	listCostAllocationTagsFn = func(context.Context, *costexplorer.ListCostAllocationTagsInput) (*costexplorer.ListCostAllocationTagsOutput, error) {
		return nil, errors.New("cost explorer failed")
	}

	_, err := loadActiveCostTags(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cost explorer failed") {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestPrintCostTagStatuses(t *testing.T) {
	buf := captureAuditOutput(t)
	printCostTagStatuses(map[string]bool{
		"Stack":       true,
		"Environment": true,
	})

	out := buf.String()
	if !strings.Contains(out, "Stack") || !strings.Contains(out, "active") {
		t.Fatalf("missing active tag output: %q", out)
	}
	if !strings.Contains(out, "Project") || !strings.Contains(out, "not activated") {
		t.Fatalf("missing inactive tag output: %q", out)
	}
}

func TestPrintBudgetSectionHandlesCostTagFailure(t *testing.T) {
	buf := captureAuditOutput(t)
	oldBudgets := describeBudgetsFn
	oldTags := listCostAllocationTagsFn
	defer func() {
		describeBudgetsFn = oldBudgets
		listCostAllocationTagsFn = oldTags
	}()

	describeBudgetsFn = func(context.Context, *budgets.DescribeBudgetsInput) (*budgets.DescribeBudgetsOutput, error) {
		return &budgets.DescribeBudgetsOutput{}, nil
	}
	listCostAllocationTagsFn = func(context.Context, *costexplorer.ListCostAllocationTagsInput) (*costexplorer.ListCostAllocationTagsOutput, error) {
		return nil, errors.New("tag listing failed")
	}

	printBudgetSection(context.Background())
	out := buf.String()
	if !strings.Contains(out, "Budget & Cost Coverage") || !strings.Contains(out, "tag listing failed") {
		t.Fatalf(errUnexpectedOutput, out)
	}
}

func TestBuildAuditSectionsSeparatesExpectedOtherManagedAndUnowned(t *testing.T) {
	oldSources := inventorySourcesFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	defer func() { inventorySourcesFn = oldSources }()
	defer func() { platformOrgCleanupTargetsForNukeFn = oldTargets }()

	inventorySourcesFn = func() []inventorySource {
		return []inventorySource{
			stubInventorySource{
				id: "terraform",
				loadFn: func(context.Context) (inventorySourceResult, error) {
					return inventorySourceResult{
						expected: []expectedAuditResource{
							{
								address:      "aws_s3_bucket.runtime",
								resourceType: "aws_s3_bucket",
								name:         "runtime-bucket",
								stack:        "platform-org",
								status:       "OK",
								taggable:     true,
							},
							{
								address:      "aws_iam_role.missing",
								resourceType: "aws_iam_role",
								name:         "missing-role",
								stack:        "platform-org",
								status:       "MISSING",
							},
						},
					}, nil
				},
			},
		}
	}
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		return []auditResource{
			{
				status:       "OK",
				resourceType: "iam/role",
				name:         "missing-role",
				stack:        "platform-org",
			},
		}, nil
	}

	sections, err := buildAuditSections(context.Background(), []auditResource{
		{
			status:       "WARN",
			resourceType: "s3",
			name:         "runtime-bucket",
			stack:        "platform-org",
			issues:       []string{"missing tag: Project"},
		},
		{
			status:       "OK",
			resourceType: "sns",
			name:         "platform-events",
			stack:        "(bootstrap)",
		},
		{
			status:       "OK",
			resourceType: "scheduler/schedule",
			name:         "platform-org-ephemeral",
			stack:        "platform-org",
		},
		{
			status:       "UNOWNED",
			resourceType: "s3",
			name:         "manual-bucket",
		},
	})
	if err != nil {
		t.Fatalf("buildAuditSections: %v", err)
	}

	if len(sections.expected) != 2 || len(sections.extra) != 1 || len(sections.otherManaged) != 1 || len(sections.unowned) != 1 {
		t.Fatalf("unexpected section sizes: %#v", sections)
	}
	if sections.expected[0].status != "OK" || sections.expected[1].status != "WARN" {
		t.Fatalf("unexpected expected statuses/order: %#v", sections.expected)
	}
	if sections.expected[0].name != "missing-role" {
		t.Fatalf("expected explicit live inventory to turn missing-role into an expected OK row: %#v", sections.expected[0])
	}
	if sections.extra[0].name != "platform-org-ephemeral" || sections.otherManaged[0].name != "platform-events" || sections.unowned[0].name != "manual-bucket" {
		t.Fatalf("unexpected non-expected sections: extra=%#v other=%#v unowned=%#v", sections.extra, sections.otherManaged, sections.unowned)
	}
	if sections.summary.expectedOK != 1 || sections.summary.expectedScheduled != 0 || sections.summary.expectedWarn != 1 || sections.summary.expectedMissing != 0 {
		t.Fatalf("unexpected expected summary: %#v", sections.summary)
	}
	if sections.summary.extraPlatformOrg != 1 || sections.summary.otherManaged != 1 || sections.summary.otherManagedWarn != 0 || sections.summary.unowned != 1 {
		t.Fatalf("unexpected other/unowned summary: %#v", sections.summary)
	}
}

func TestAuditCommandRunEPrintsSectionSummary(t *testing.T) {
	buf := captureAuditOutput(t)
	oldScan := scanResourcesFn
	oldSources := inventorySourcesFn
	oldBudget := printBudgetSectionFn
	oldDoctor := platformOrgDoctorRunFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	defer func() {
		scanResourcesFn = oldScan
		inventorySourcesFn = oldSources
		printBudgetSectionFn = oldBudget
		platformOrgDoctorRunFn = oldDoctor
		platformOrgCleanupTargetsForNukeFn = oldTargets
	}()

	d.accountID = testAccountID
	d.region = testRegion
	budgetCalled := false
	scanResourcesFn = func(context.Context) ([]auditResource, error) {
		return []auditResource{
			{status: "OK", resourceType: "sns", name: "platform-events", stack: "(bootstrap)"},
			{status: "OK", resourceType: "scheduler/schedule", name: "ephemeral-schedule", stack: "platform-org"},
			{status: "UNOWNED", resourceType: "s3", name: "manual-bucket"},
		}, nil
	}
	inventorySourcesFn = func() []inventorySource {
		return []inventorySource{
			stubInventorySource{
				id: "terraform",
				loadFn: func(context.Context) (inventorySourceResult, error) {
					return inventorySourceResult{
						expected: []expectedAuditResource{
							{
								address:      "aws_organizations_organization.this",
								resourceType: "aws_organizations_organization",
								name:         "organization",
								stack:        "platform-org",
								status:       "OK",
							},
							{
								address:      "aws_iam_role.missing",
								resourceType: "aws_iam_role",
								name:         "missing-role",
								stack:        "platform-org",
								status:       "MISSING",
							},
						},
					}, nil
				},
			},
		}
	}
	printBudgetSectionFn = func(context.Context) {
		budgetCalled = true
	}
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{
			Sections: []platformOrgDoctorSection{{
				Title: "Backend Contract",
				Checks: []platformOrgDoctorCheck{{
					Key:    "backend.identity",
					Title:  "backend points at the current org and env",
					Status: "ok",
					Detail: "bucket/table/key match",
				}},
			}},
			Summary: platformOrgDoctorSummary{OK: 1, Total: 1},
		}, nil
	}

	if err := auditCmd.RunE(auditCmd, nil); err != nil {
		t.Fatalf("auditCmd.RunE: %v", err)
	}
	if !budgetCalled {
		t.Fatal("expected budget printer to run")
	}
	for _, want := range []string{
		"Expected Platform Org Resources",
		"Extra Platform Org Resources",
		"Other Managed Resources",
		"Unowned Resources",
		"SOURCE",
		"ADDRESS",
		"Summary: expected_ok=1  expected_scheduled=0  expected_warn=0  expected_missing=1  extra_platform_org=1  other_managed=1  other_managed_warn=0  unowned=1",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf(errUnexpectedOutput, buf.String())
		}
	}
}

func TestAuditCommandRunEExpectedResourceError(t *testing.T) {
	oldScan := scanResourcesFn
	oldSources := inventorySourcesFn
	oldDoctor := platformOrgDoctorRunFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	defer func() {
		scanResourcesFn = oldScan
		inventorySourcesFn = oldSources
		platformOrgDoctorRunFn = oldDoctor
		platformOrgCleanupTargetsForNukeFn = oldTargets
	}()

	scanResourcesFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	inventorySourcesFn = func() []inventorySource {
		return []inventorySource{
			stubInventorySource{
				id: "runtime",
				loadFn: func(context.Context) (inventorySourceResult, error) {
					return inventorySourceResult{}, errors.New("expected inventory failed")
				},
			},
		}
	}
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{}, nil
	}

	err := auditCmd.RunE(auditCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "building audit sections: runtime inventory: expected inventory failed") {
		t.Fatalf(errUnexpectedError, err)
	}
}

func TestActivationInventorySourceShowsPendingScheduleAndInactiveTags(t *testing.T) {
	oldListTags := listCostAllocationTagsFn
	oldGetSchedule := getScheduleFn
	oldListSchedules := listSchedulesFn
	defer func() {
		listCostAllocationTagsFn = oldListTags
		getScheduleFn = oldGetSchedule
		listSchedulesFn = oldListSchedules
	}()

	d.org = "ffreis"
	listCostAllocationTagsFn = func(context.Context, *costexplorer.ListCostAllocationTagsInput) (*costexplorer.ListCostAllocationTagsOutput, error) {
		return &costexplorer.ListCostAllocationTagsOutput{
			CostAllocationTags: []cetypes.CostAllocationTag{
				{TagKey: sdkaws.String("Stack")},
			},
		}, nil
	}
	schedName := activationScheduleName(d.org)
	groupName := activationScheduleGroupName(d.org)
	schedARN := "arn:aws:scheduler:::schedule/" + groupName + "/" + schedName
	getScheduleFn = func(context.Context, *scheduler.GetScheduleInput) (*scheduler.GetScheduleOutput, error) {
		return &scheduler.GetScheduleOutput{
			Name:                  sdkaws.String(schedName),
			GroupName:             sdkaws.String(groupName),
			Arn:                   sdkaws.String(schedARN),
			ScheduleExpression:    sdkaws.String("at(2026-04-05T20:00:00)"),
			State:                 schedulertypes.ScheduleStateEnabled,
			ActionAfterCompletion: schedulertypes.ActionAfterCompletionDelete,
		}, nil
	}
	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return &scheduler.ListSchedulesOutput{
			Schedules: []schedulertypes.ScheduleSummary{
				{Name: sdkaws.String(schedName), GroupName: sdkaws.String(groupName), Arn: sdkaws.String(schedARN), State: schedulertypes.ScheduleStateEnabled},
			},
		}, nil
	}

	result, err := activationInventorySource{}.load(context.Background())
	if err != nil {
		t.Fatalf("activationInventorySource.load: %v", err)
	}
	if len(result.extra) != 0 {
		t.Fatalf("unexpected extra resources: %#v", result.extra)
	}
	if len(result.expected) != len(activation.CostAllocationTags)+1 {
		t.Fatalf("expected runtime resources count: got %d", len(result.expected))
	}
	if result.expected[len(result.expected)-1].resourceType != "scheduler/schedule" || result.expected[len(result.expected)-1].status != "SCHEDULED" {
		t.Fatalf("expected final schedule row, got %#v", result.expected[len(result.expected)-1])
	}
	var scheduled bool
	for _, row := range result.expected {
		if row.resourceType == "costexplorer/cost-allocation-tag" && row.name == "Project" {
			scheduled = row.status == "SCHEDULED"
		}
	}
	if !scheduled {
		t.Fatalf("expected inactive cost tag to become SCHEDULED while schedule exists: %#v", result.expected)
	}
}

func TestParseExpectedPlatformOrgResourcesUsesTerraformPlanInventory(t *testing.T) {
	defs, err := parseExpectedPlatformOrgResources([]byte(`{
  "planned_values": {
    "root_module": {
      "resources": [
        {
          "address": "aws_s3_bucket.runtime",
          "mode": "managed",
          "type": "aws_s3_bucket",
          "name": "runtime",
          "values": {
            "bucket": "ffreis-tf-state-runtime",
            "arn": "arn:aws:s3:::ffreis-tf-state-runtime",
            "tags": {"ManagedBy": "terraform", "Stack": "platform-org"}
          }
        },
        {
          "address": "aws_iam_role_policy.helper",
          "mode": "managed",
          "type": "aws_iam_role_policy",
          "name": "helper",
          "values": {
            "name": "helper"
          }
        }
      ],
      "child_modules": [
        {
          "address": "module.extra",
          "resources": [
            {
              "address": "module.extra.aws_sqs_queue.jobs",
              "mode": "managed",
              "type": "aws_sqs_queue",
              "name": "jobs",
              "values": {
                "name": "ffreis-jobs"
              }
            }
          ]
        }
      ]
    }
  },
  "resource_changes": [
    {
      "address": "aws_s3_bucket.runtime",
      "mode": "managed",
      "type": "aws_s3_bucket",
      "change": {
        "actions": ["no-op"]
      }
    },
    {
      "address": "module.extra.aws_sqs_queue.jobs",
      "mode": "managed",
      "type": "aws_sqs_queue",
      "change": {
        "actions": ["create"]
      }
    }
  ]
}`))
	if err != nil {
		t.Fatalf("parseExpectedPlatformOrgResources: %v", err)
	}

	if len(defs) != 2 {
		t.Fatalf("expected 2 plan resources after helper exclusion, got %d", len(defs))
	}
	if defs[0].address != "aws_s3_bucket.runtime" || defs[0].status != "OK" || defs[0].name != "ffreis-tf-state-runtime" {
		t.Fatalf("unexpected first expected resource: %#v", defs[0])
	}
	if defs[1].address != "module.extra.aws_sqs_queue.jobs" || defs[1].status != "MISSING" || defs[1].name != "ffreis-jobs" {
		t.Fatalf("unexpected second expected resource: %#v", defs[1])
	}
}

func TestAuditCommandRunEReturnsWrappedError(t *testing.T) {
	oldScan := scanResourcesFn
	oldDoctor := platformOrgDoctorRunFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	defer func() { scanResourcesFn = oldScan }()
	defer func() { platformOrgDoctorRunFn = oldDoctor }()
	defer func() { platformOrgCleanupTargetsForNukeFn = oldTargets }()
	scanResourcesFn = func(context.Context) ([]auditResource, error) {
		return nil, errors.New("scan failed")
	}
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{}, nil
	}

	err := auditCmd.RunE(auditCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "scanning resources: scan failed") {
		t.Fatalf(errUnexpectedError, err)
	}
}
