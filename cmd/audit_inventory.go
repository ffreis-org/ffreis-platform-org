package cmd

import (
	"context"
	"fmt"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	"github.com/ffreis/platform-org/internal/activation"
)

var inventorySourcesFn = inventorySources

type inventorySource interface {
	sourceID() string
	load(context.Context) (inventorySourceResult, error)
	cleanupNuke(context.Context) ([]string, error)
}

type inventorySourceResult struct {
	expected []expectedAuditResource
	extra    []auditResource
}

type terraformInventorySource struct{}

type activationInventorySource struct{}

func inventorySources() []inventorySource {
	return []inventorySource{
		terraformInventorySource{},
		activationInventorySource{},
	}
}

func (terraformInventorySource) sourceID() string { return "terraform" }

func (terraformInventorySource) load(ctx context.Context) (inventorySourceResult, error) {
	data, err := terraformPlanJSONFn(ctx)
	if err != nil {
		return inventorySourceResult{}, fmt.Errorf("terraform plan inventory: %w", err)
	}
	expected, err := parseExpectedPlatformOrgResources(data)
	if err != nil {
		return inventorySourceResult{}, err
	}
	for i := range expected {
		expected[i].source = "terraform"
	}
	return inventorySourceResult{expected: expected}, nil
}

func (terraformInventorySource) cleanupNuke(context.Context) ([]string, error) {
	return nil, nil
}

func (activationInventorySource) sourceID() string { return "runtime" }

func (activationInventorySource) load(ctx context.Context) (inventorySourceResult, error) {
	active, err := loadAllCostTagStatuses(ctx)
	if err != nil {
		return inventorySourceResult{}, fmt.Errorf("list cost allocation tags: %w", err)
	}
	groupSchedules, err := listPlatformOrgSchedules(ctx, d.org)
	if err != nil {
		return inventorySourceResult{}, fmt.Errorf("list platform-org schedules: %w", err)
	}
	schedule, err := activationSchedule(ctx, d.org)
	if err != nil {
		return inventorySourceResult{}, fmt.Errorf("inspect activation schedule: %w", err)
	}
	scheduleHealthy := schedule != nil &&
		schedule.State == schedulertypes.ScheduleStateEnabled &&
		schedule.ActionAfterCompletion == schedulertypes.ActionAfterCompletionDelete

	expected := make([]expectedAuditResource, 0, len(activation.CostAllocationTags)+1)
	inactiveTags := make([]string, 0, len(activation.CostAllocationTags))
	for _, key := range activation.CostAllocationTags {
		resource, inactive := activationCostTagResource(key, active[key], schedule, scheduleHealthy)
		if inactive {
			inactiveTags = append(inactiveTags, key)
		}
		expected = append(expected, resource)
	}

	var extra []auditResource
	if len(inactiveTags) > 0 {
		expected = append(expected, activationScheduleExpectedResource(schedule))
	} else if schedule != nil {
		extra = append(extra, unexpectedActivationScheduleResource(*schedule))
	}

	for _, discoveredSchedule := range groupSchedules {
		if discoveredSchedule.Name == activationScheduleName(d.org) {
			continue
		}
		extra = append(extra, unexpectedGroupScheduleResource(discoveredSchedule))
	}

	return inventorySourceResult{
		expected: expected,
		extra:    extra,
	}, nil
}

func activationCostTagResource(key string, status cetypes.CostAllocationTagStatus, schedule *activationScheduleDetails, scheduleHealthy bool) (expectedAuditResource, bool) {
	resource := expectedAuditResource{
		source:       "runtime",
		address:      fmt.Sprintf("runtime.cost_allocation_tag[%q]", key),
		resourceType: "costexplorer/cost-allocation-tag",
		name:         key,
		stack:        platformOrgStackTag,
	}

	switch status {
	case cetypes.CostAllocationTagStatusActive:
		resource.status = "OK"
		return resource, false
	case cetypes.CostAllocationTagStatusInactive:
		resource.status = "MISSING"
		resource.issues = []string{"cost allocation tag is inactive"}
	default:
		resource.status = "MISSING"
		resource.issues = []string{"cost allocation tag is not discovered yet"}
	}

	applyActivationScheduleStatus(&resource, schedule, scheduleHealthy)
	return resource, true
}

func applyActivationScheduleStatus(resource *expectedAuditResource, schedule *activationScheduleDetails, scheduleHealthy bool) {
	if scheduleHealthy {
		resource.status = "SCHEDULED"
		resource.issues = []string{fmt.Sprintf("pending auto-activation schedule: %s", scheduleSummary(*schedule))}
		return
	}
	if schedule != nil {
		resource.status = "WARN"
		resource.issues = []string{fmt.Sprintf("auto-activation schedule needs attention: %s", scheduleSummary(*schedule))}
	}
}

func activationScheduleExpectedResource(schedule *activationScheduleDetails) expectedAuditResource {
	resource := expectedAuditResource{
		source:       "runtime",
		address:      fmt.Sprintf("runtime.scheduler_schedule[%q]", activationScheduleName(d.org)),
		resourceType: resourceTypeSchedulerSchedule,
		name:         activationScheduleName(d.org),
		stack:        platformOrgStackTag,
	}
	if schedule == nil {
		resource.status = "MISSING"
		resource.issues = []string{"auto-activation schedule not found while cost allocation tags are still inactive"}
		return resource
	}
	resource.arn = schedule.ARN
	resource.status = "SCHEDULED"
	resource.issues = []string{scheduleSummary(*schedule)}
	if schedule.State != schedulertypes.ScheduleStateEnabled {
		resource.status = "WARN"
		resource.issues = append(resource.issues, "schedule is not enabled")
	}
	if schedule.ActionAfterCompletion != schedulertypes.ActionAfterCompletionDelete {
		resource.status = "WARN"
		resource.issues = append(resource.issues, "schedule does not delete itself after completion")
	}
	return resource
}

func unexpectedActivationScheduleResource(schedule activationScheduleDetails) auditResource {
	return auditResource{
		status:       "WARN",
		source:       "runtime",
		resourceType: resourceTypeSchedulerSchedule,
		name:         schedule.Name,
		arn:          schedule.ARN,
		stack:        platformOrgStackTag,
		issues: []string{
			"activation schedule still exists even though all cost allocation tags are already active",
			scheduleSummary(schedule),
		},
	}
}

func unexpectedGroupScheduleResource(schedule activationScheduleDetails) auditResource {
	return auditResource{
		status:       "WARN",
		source:       "runtime",
		resourceType: resourceTypeSchedulerSchedule,
		name:         schedule.Name,
		arn:          schedule.ARN,
		stack:        platformOrgStackTag,
		issues: []string{
			"unexpected pending schedule in platform-org scheduler group",
			scheduleSummary(schedule),
		},
	}
}

func (activationInventorySource) cleanupNuke(ctx context.Context) ([]string, error) {
	return deletePendingSchedules(ctx, d.org)
}

func loadAllCostTagStatuses(ctx context.Context) (map[string]cetypes.CostAllocationTagStatus, error) {
	statuses := make(map[string]cetypes.CostAllocationTagStatus)
	var nextToken *string
	for {
		out, err := listCostAllocationTagsFn(ctx, &costexplorer.ListCostAllocationTagsInput{
			NextToken: nextToken,
		})
		if err != nil {
			return nil, err
		}
		for _, tag := range out.CostAllocationTags {
			statuses[sdkaws.ToString(tag.TagKey)] = tag.Status
		}
		if sdkaws.ToString(out.NextToken) == "" {
			return statuses, nil
		}
		nextToken = out.NextToken
	}
}
