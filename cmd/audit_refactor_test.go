package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
)

func TestLoadExpectedAuditResourcesAnnotatesSources(t *testing.T) {
	t.Parallel()

	sources := []inventorySource{
		stubInventorySource{id: "first", loadFn: func(context.Context) (inventorySourceResult, error) {
			return inventorySourceResult{
				expected: []expectedAuditResource{{name: "a"}},
				extra:    []auditResource{{name: "x"}},
			}, nil
		}},
		stubInventorySource{id: "second", loadFn: func(context.Context) (inventorySourceResult, error) {
			return inventorySourceResult{expected: []expectedAuditResource{{name: "b"}}}, nil
		}},
	}

	expected, extra, err := loadExpectedAuditResources(context.Background(), sources)
	if err != nil {
		t.Fatalf("loadExpectedAuditResources: %v", err)
	}
	if len(expected) != 2 || expected[0].source != "first" || expected[0].sourceOrder != 0 || expected[0].order != 0 || expected[1].source != "second" {
		t.Fatalf("unexpected expected resources: %#v", expected)
	}
	if len(extra) != 1 || extra[0].source != "first" {
		t.Fatalf("unexpected extra resources: %#v", extra)
	}
}

func TestLoadExpectedAuditResourcesWrapsSourceError(t *testing.T) {
	t.Parallel()

	_, _, err := loadExpectedAuditResources(context.Background(), []inventorySource{
		stubInventorySource{id: "broken", loadFn: func(context.Context) (inventorySourceResult, error) {
			return inventorySourceResult{}, errors.New("nope")
		}},
	})
	if err == nil || err.Error() != "broken inventory: nope" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveAndPartitionAuditResources(t *testing.T) {
	t.Parallel()

	expectedDefs := []expectedAuditResource{{
		source:       "runtime",
		resourceType: "s3",
		name:         "bucket",
		stack:        platformOrgStackTag,
		status:       "MISSING",
		taggable:     true,
	}}
	discovered := []auditResource{
		{status: "WARN", resourceType: "s3", name: "bucket", stack: platformOrgStackTag, issues: []string{"missing tag"}},
		{status: "OK", resourceType: "sns", name: "bootstrap-topic", stack: "bootstrap"},
		{status: "UNOWNED", resourceType: "s3", name: "manual"},
	}
	discoveredByARN, discoveredByName := buildDiscoveredIndexes(discovered, nil, false)
	resolved, matched := resolveExpectedAuditResources(expectedDefs, discoveredByARN, discoveredByName)
	extra, otherManaged, unowned := partitionDiscoveredAuditResources(discovered, matched, nil)
	summary := summarizeAuditResources(resolved, extra, otherManaged, unowned)

	if len(resolved) != 1 || resolved[0].status != "WARN" || len(resolved[0].issues) != 1 {
		t.Fatalf("unexpected resolved resources: %#v", resolved)
	}
	if len(extra) != 0 || len(otherManaged) != 1 || len(unowned) != 1 {
		t.Fatalf("unexpected partitions: extra=%#v other=%#v unowned=%#v", extra, otherManaged, unowned)
	}
	if summary.expectedWarn != 1 || summary.otherManaged != 1 || summary.unowned != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestActivationHelpers(t *testing.T) {
	t.Parallel()

	schedule := &activationScheduleDetails{
		Name:                  "schedule",
		ARN:                   "arn:aws:scheduler:::schedule",
		State:                 schedulertypes.ScheduleStateEnabled,
		ActionAfterCompletion: schedulertypes.ActionAfterCompletionDelete,
	}
	resource, inactive := activationCostTagResource("Team", types.CostAllocationTagStatusInactive, schedule, true)
	if !inactive || resource.status != "SCHEDULED" {
		t.Fatalf("unexpected cost tag resource: %#v inactive=%v", resource, inactive)
	}
	scheduleResource := activationScheduleExpectedResource(schedule)
	if scheduleResource.resourceType != resourceTypeSchedulerSchedule || scheduleResource.status != "SCHEDULED" {
		t.Fatalf("unexpected schedule resource: %#v", scheduleResource)
	}
	extra := unexpectedActivationScheduleResource(*schedule)
	if extra.resourceType != resourceTypeSchedulerSchedule || extra.stack != platformOrgStackTag {
		t.Fatalf("unexpected extra activation schedule: %#v", extra)
	}
	groupExtra := unexpectedGroupScheduleResource(*schedule)
	if groupExtra.name != schedule.Name || len(groupExtra.issues) != 2 {
		t.Fatalf("unexpected group schedule: %#v", groupExtra)
	}
}

func TestActivationHelpersWarnAndMissingBranches(t *testing.T) {
	t.Parallel()

	resource, inactive := activationCostTagResource("Team", types.CostAllocationTagStatusInactive, nil, false)
	if !inactive || resource.status != "MISSING" {
		t.Fatalf("unexpected missing resource: %#v inactive=%v", resource, inactive)
	}
	schedule := &activationScheduleDetails{
		Name:                  "schedule",
		ARN:                   "arn:aws:scheduler:::schedule",
		State:                 schedulertypes.ScheduleStateDisabled,
		ActionAfterCompletion: schedulertypes.ActionAfterCompletionNone,
	}
	applyActivationScheduleStatus(&resource, schedule, false)
	if resource.status != "WARN" {
		t.Fatalf("unexpected warn resource: %#v", resource)
	}
	missingSchedule := activationScheduleExpectedResource(nil)
	if missingSchedule.status != "MISSING" {
		t.Fatalf("unexpected missing schedule resource: %#v", missingSchedule)
	}
	warnSchedule := activationScheduleExpectedResource(schedule)
	if warnSchedule.status != "WARN" || len(warnSchedule.issues) < 2 {
		t.Fatalf("unexpected warn schedule resource: %#v", warnSchedule)
	}
}

func TestAppendUnmatchedLiveManagedAndSummaryBranches(t *testing.T) {
	t.Parallel()

	extra := appendUnmatchedLiveManaged(nil, []auditResource{{name: "skip", stack: "bootstrap"}, {name: "keep", stack: platformOrgStackTag}}, map[string]bool{}, true)
	if len(extra) != 1 || extra[0].name != "keep" {
		t.Fatalf("unexpected extra resources: %#v", extra)
	}
	summary := summarizeAuditResources(
		[]auditResource{{status: "OK"}, {status: "SCHEDULED"}, {status: "MISSING"}, {status: "WARN"}},
		[]auditResource{{name: "extra"}},
		[]auditResource{{status: "WARN"}, {status: "OK"}},
		[]auditResource{{name: "u"}},
	)
	if summary.expectedOK != 1 || summary.expectedScheduled != 1 || summary.expectedMissing != 1 || summary.expectedWarn != 1 || summary.extraPlatformOrg != 1 || summary.otherManaged != 2 || summary.otherManagedWarn != 1 || summary.unowned != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestActivationCostTagResourceAdditionalBranches(t *testing.T) {
	t.Parallel()

	active, inactive := activationCostTagResource("Team", types.CostAllocationTagStatusActive, nil, false)
	if inactive || active.status != "OK" {
		t.Fatalf("unexpected active resource: %#v inactive=%v", active, inactive)
	}
	unknown, missing := activationCostTagResource("Team", "", nil, false)
	if !missing || unknown.status != "MISSING" || len(unknown.issues) != 1 {
		t.Fatalf("unexpected unknown-status resource: %#v missing=%v", unknown, missing)
	}
	unchanged := appendUnmatchedLiveManaged([]auditResource{{name: "existing"}}, []auditResource{{name: "ignored", stack: platformOrgStackTag}}, map[string]bool{}, false)
	if len(unchanged) != 1 || unchanged[0].name != "existing" {
		t.Fatalf("unexpected unchanged extras: %#v", unchanged)
	}
}
