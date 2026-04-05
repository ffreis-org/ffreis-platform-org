package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	organizationstypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroups"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	taggingtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/spf13/cobra"
)

// requiredTags are the tags every Terraform-managed resource must carry.
// Ownership is determined by cross-layer ownership tags rather than by
// resource naming: bootstrap resources identify themselves with Stack=bootstrap
// / ManagedBy=platform-bootstrap, while Terraform stacks use Stack=<stack>
// / ManagedBy=terraform. New stacks are therefore recognised automatically
// once they emit the shared tag contract.
var requiredTags = []string{"Project", "Environment", "ManagedBy", "Stack"}

var (
	auditStdout io.Writer = os.Stdout

	scanResourcesFn     = scanResources
	resourceExistsFn    = resourceExists
	terraformPlanJSONFn = terraformPlanJSON

	getResourcesPage = func(ctx context.Context, input *resourcegroupstaggingapi.GetResourcesInput) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
		return d.tagging.GetResources(ctx, input)
	}
	getScheduleFn = func(ctx context.Context, input *scheduler.GetScheduleInput) (*scheduler.GetScheduleOutput, error) {
		return scheduler.NewFromConfig(d.awsCfg).GetSchedule(ctx, input)
	}
	listSchedulesFn = func(ctx context.Context, input *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return scheduler.NewFromConfig(d.awsCfg).ListSchedules(ctx, input)
	}
	deleteScheduleFn = func(ctx context.Context, input *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return scheduler.NewFromConfig(d.awsCfg).DeleteSchedule(ctx, input)
	}
	describeBudgetsFn = func(ctx context.Context, input *budgets.DescribeBudgetsInput) (*budgets.DescribeBudgetsOutput, error) {
		return d.budgets.DescribeBudgets(ctx, input)
	}
	listCostAllocationTagsFn = func(ctx context.Context, input *costexplorer.ListCostAllocationTagsInput) (*costexplorer.ListCostAllocationTagsOutput, error) {
		return d.ce.ListCostAllocationTags(ctx, input)
	}
)

type platformOrgEnvConfig struct {
	Org      string                                 `json:"org"`
	Accounts map[string]platformOrgEnvAccountConfig `json:"accounts"`
}

type platformOrgEnvAccountConfig struct {
	Email string `json:"email"`
}

type expectedAuditResource struct {
	source       string
	sourceOrder  int
	order        int
	address      string
	resourceType string
	name         string
	arn          string
	stack        string
	status       string
	taggable     bool
	issues       []string
}

type auditSections struct {
	expected     []auditResource
	extra        []auditResource
	otherManaged []auditResource
	unowned      []auditResource
	summary      auditSectionSummary
}

type auditSectionSummary struct {
	expectedOK        int
	expectedScheduled int
	expectedWarn      int
	expectedMissing   int
	extraPlatformOrg  int
	otherManaged      int
	otherManagedWarn  int
	unowned           int
}

// auditResource is one finding from the resource scan.
type auditResource struct {
	status       string
	source       string
	address      string
	resourceType string
	name         string
	arn          string
	stack        string
	issues       []string
}

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Audit expected platform resources, other managed stacks, and unowned resources",
	Long: `audit verifies the deterministic platform-org resource inventory first,
then reports other managed resources and finally unowned resources.

Sections:

  1. Expected Platform Org Resources
  2. Extra Platform Org Resources
  3. Other Managed Resources
  4. Unowned Resources
  5. Budget & Cost Coverage`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		out := newWriterOutput(auditStdout, auditStdout, d.ui)
		out.Header("Platform Org Audit", envAccountRegionSummary(d.env, d.accountID, d.region))
		out.Blank()

		discovered, err := scanResourcesFn(ctx)
		if err != nil {
			return fmt.Errorf("scanning resources: %w", err)
		}

		sections, err := buildAuditSections(ctx, discovered)
		if err != nil {
			return fmt.Errorf("building audit sections: %w", err)
		}
		doctorReport, err := platformOrgDoctorRunFn(ctx, platformOrgDoctorModes.audit)
		if err != nil {
			return fmt.Errorf("running integrity checks: %w", err)
		}

		printAuditSections(out, sections)
		out.Blank()
		out.Header("Integrity Checks", "")
		printPlatformOrgDoctorReport(out, doctorReport)
		out.Blank()
		printPlatformOrgDoctorSummary(out, doctorReport)
		out.Blank()
		printBudgetSectionFn(ctx)

		out.Blank()
		out.Summary("Summary",
			countPart("expected_ok", sections.summary.expectedOK),
			countPart("expected_scheduled", sections.summary.expectedScheduled),
			countPart("expected_warn", sections.summary.expectedWarn),
			countPart("expected_missing", sections.summary.expectedMissing),
			countPart("extra_platform_org", sections.summary.extraPlatformOrg),
			countPart("other_managed", sections.summary.otherManaged),
			countPart("other_managed_warn", sections.summary.otherManagedWarn),
			countPart("unowned", sections.summary.unowned),
		)
		out.Blank()
		if doctorReport.HasFailures() {
			return fmt.Errorf("integrity checks failed with %d blocking issue(s)", doctorReport.Summary.Fail)
		}
		return nil
	},
}

var printBudgetSectionFn = printBudgetSection

// scanResources fetches all tagged resources from the Tagging API and
// categorises each one as owned, unowned, or owned-with-issues.
func scanResources(ctx context.Context) ([]auditResource, error) {
	var results []auditResource

	var nextToken *string
	for {
		out, err := getResourcesPage(ctx, &resourcegroupstaggingapi.GetResourcesInput{
			ResourcesPerPage: sdkaws.Int32(100),
			PaginationToken:  nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("GetResources: %w", err)
		}

		for _, mapping := range out.ResourceTagMappingList {
			resource := classifyResource(mapping)
			exists, err := resourceExistsFn(ctx, resource)
			if err != nil {
				if d.log != nil {
					d.log.Warn("resource existence check failed", "resource_type", resource.resourceType, "name", resource.name, "error", err)
				}
				results = append(results, resource)
				continue
			}
			if !exists {
				continue
			}
			results = append(results, resource)
		}

		if sdkaws.ToString(out.PaginationToken) == "" {
			break
		}
		nextToken = out.PaginationToken
	}

	return results, nil
}

func buildAuditSections(ctx context.Context, discovered []auditResource) (auditSections, error) {
	sources := inventorySourcesFn()
	expectedDefs := make([]expectedAuditResource, 0, 32)
	extraFromSources := make([]auditResource, 0, 8)
	for sourceOrder, source := range sources {
		result, err := source.load(ctx)
		if err != nil {
			return auditSections{}, fmt.Errorf("%s inventory: %w", source.sourceID(), err)
		}
		for i := range result.expected {
			result.expected[i].source = source.sourceID()
			result.expected[i].sourceOrder = sourceOrder
			result.expected[i].order = i
		}
		for i := range result.extra {
			if result.extra[i].source == "" {
				result.extra[i].source = source.sourceID()
			}
		}
		expectedDefs = append(expectedDefs, result.expected...)
		extraFromSources = append(extraFromSources, result.extra...)
	}

	liveManaged, liveErr := platformOrgCleanupTargetsForNukeFn(ctx)
	if liveErr != nil && d.log != nil {
		d.log.Warn("explicit platform-org live inventory failed during audit; falling back to tagging matches only", "err", liveErr)
	}

	discoveredByARN := make(map[string]auditResource, len(discovered))
	discoveredByName := make(map[string][]auditResource, len(discovered))
	addMatchCandidate := func(resource auditResource) {
		if resource.arn != "" {
			discoveredByARN[resource.arn] = resource
		}
		if resource.name != "" {
			key := strings.ToLower(resource.name)
			discoveredByName[key] = append(discoveredByName[key], resource)
		}
	}
	for _, resource := range discovered {
		addMatchCandidate(resource)
	}
	if liveErr == nil {
		for _, resource := range liveManaged {
			addMatchCandidate(resource)
		}
	}

	matched := make(map[string]bool, len(discovered))
	expected := make([]auditResource, 0, len(expectedDefs))
	for _, def := range expectedDefs {
		resource := auditResource{
			source:       def.source,
			address:      def.address,
			resourceType: def.resourceType,
			name:         def.name,
			arn:          def.arn,
			stack:        def.stack,
			status:       def.status,
			issues:       append([]string(nil), def.issues...),
		}

		discoveredResource, ok := matchExpectedAuditResource(def, discoveredByARN, discoveredByName)
		if ok {
			matched[matchedDiscoveredResourceKey(discoveredResource)] = true
			if resource.name == "" {
				resource.name = discoveredResource.name
			}
			if resource.arn == "" {
				resource.arn = discoveredResource.arn
			}
			if resource.status == "MISSING" {
				resource.status = "OK"
				resource.issues = nil
			}
			if def.taggable && discoveredResource.status != "OK" {
				resource.status = "WARN"
				resource.issues = append(resource.issues, discoveredResource.issues...)
			}
		}
		expected = append(expected, resource)
	}

	extra := make([]auditResource, 0, len(discovered))
	extra = append(extra, extraFromSources...)
	otherManaged := make([]auditResource, 0, len(discovered))
	unowned := make([]auditResource, 0, len(discovered))
	for _, resource := range discovered {
		if matched[matchedDiscoveredResourceKey(resource)] {
			continue
		}
		if resource.status == "UNOWNED" {
			unowned = append(unowned, resource)
			continue
		}
		if resource.stack == "platform-org" {
			extra = append(extra, resource)
			continue
		}
		otherManaged = append(otherManaged, resource)
	}
	if liveErr == nil {
		for _, resource := range liveManaged {
			if matched[matchedDiscoveredResourceKey(resource)] {
				continue
			}
			if resource.stack != "platform-org" {
				continue
			}
			extra = append(extra, resource)
		}
	}
	extra = dedupeAuditResources(extra)

	sortExpectedResources(expected, expectedDefs)
	sortOtherManagedResources(extra)
	sortOtherManagedResources(otherManaged)
	sortUnownedResources(unowned)

	summary := auditSectionSummary{}
	for _, resource := range expected {
		switch resource.status {
		case "OK":
			summary.expectedOK++
		case "SCHEDULED":
			summary.expectedScheduled++
		case "WARN":
			summary.expectedWarn++
		case "MISSING":
			summary.expectedMissing++
		}
	}
	summary.extraPlatformOrg = len(extra)
	for _, resource := range otherManaged {
		summary.otherManaged++
		if resource.status == "WARN" {
			summary.otherManagedWarn++
		}
	}
	summary.unowned = len(unowned)

	return auditSections{
		expected:     expected,
		extra:        extra,
		otherManaged: otherManaged,
		unowned:      unowned,
		summary:      summary,
	}, nil
}

func dedupeAuditResources(resources []auditResource) []auditResource {
	seen := make(map[string]bool, len(resources))
	out := make([]auditResource, 0, len(resources))
	for _, resource := range resources {
		key := matchedDiscoveredResourceKey(resource)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, resource)
	}
	return out
}

func matchExpectedAuditResource(def expectedAuditResource, discoveredByARN map[string]auditResource, discoveredByName map[string][]auditResource) (auditResource, bool) {
	if def.arn != "" {
		if resource, ok := discoveredByARN[def.arn]; ok {
			return resource, true
		}
	}
	if def.name == "" {
		return auditResource{}, false
	}
	candidates := discoveredByName[strings.ToLower(def.name)]
	if len(candidates) == 1 {
		return candidates[0], true
	}
	var stackMatches []auditResource
	for _, candidate := range candidates {
		if candidate.stack == def.stack {
			stackMatches = append(stackMatches, candidate)
		}
	}
	if len(stackMatches) == 1 {
		return stackMatches[0], true
	}
	return auditResource{}, false
}

func matchedDiscoveredResourceKey(resource auditResource) string {
	if resource.arn != "" {
		return "arn:" + resource.arn
	}
	return strings.ToLower(resource.stack + "|" + resource.resourceType + "|" + resource.name)
}

func classifyResource(m taggingtypes.ResourceTagMapping) auditResource {
	arn := sdkaws.ToString(m.ResourceARN)
	rtype, name := parseARN(arn)

	tags := make(map[string]string, len(m.Tags))
	for _, t := range m.Tags {
		tags[sdkaws.ToString(t.Key)] = sdkaws.ToString(t.Value)
	}

	if tags["Stack"] == "bootstrap" || tags["Layer"] == "bootstrap" || tags["ManagedBy"] == "platform-bootstrap" {
		stack := tags["Stack"]
		if stack == "" {
			stack = "bootstrap"
		}
		return auditResource{
			status:       "OK",
			resourceType: rtype,
			name:         name,
			arn:          arn,
			stack:        stack,
		}
	}

	if tags["ManagedBy"] != "terraform" {
		return auditResource{
			status:       "UNOWNED",
			resourceType: rtype,
			name:         name,
			arn:          arn,
			stack:        tags["Stack"],
			issues:       []string{"ManagedBy tag absent or not 'terraform'"},
		}
	}

	var issues []string
	for _, req := range requiredTags {
		if tags[req] == "" {
			issues = append(issues, "missing tag: "+req)
		}
	}

	status := "OK"
	if len(issues) > 0 {
		status = "WARN"
	}

	return auditResource{
		status:       status,
		resourceType: rtype,
		name:         name,
		arn:          arn,
		stack:        tags["Stack"],
		issues:       issues,
	}
}

func parseARN(arn string) (resourceType, name string) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return "unknown", arn
	}
	service := parts[2]
	resource := parts[5]

	slashIdx := strings.Index(resource, "/")
	colonIdx := strings.Index(resource, ":")
	switch {
	case colonIdx >= 0 && (slashIdx == -1 || colonIdx < slashIdx):
		return service + "/" + resource[:colonIdx], resource[colonIdx+1:]
	case slashIdx >= 0:
		return service + "/" + resource[:slashIdx], resource[slashIdx+1:]
	}
	return service, resource
}

func printAuditSections(out *commandOutput, sections auditSections) {
	printExpectedAuditSection(out, "Expected Platform Org Resources", sections.expected)
	out.Blank()
	printAuditSection(out, "Extra Platform Org Resources", sections.extra)
	out.Blank()
	printAuditSection(out, "Other Managed Resources", sections.otherManaged)
	out.Blank()
	printAuditSection(out, "Unowned Resources", sections.unowned)
}

func printExpectedAuditSection(out *commandOutput, title string, resources []auditResource) {
	out.Header(title, "")
	rows := make([][]string, 0, len(resources))
	for _, resource := range resources {
		issues := "-"
		if len(resource.issues) > 0 {
			issues = strings.Join(resource.issues, "; ")
		}
		name := resource.name
		if name == "" {
			name = "-"
		}
		rows = append(rows, []string{
			auditStatusCell(resource.status),
			truncate(resource.source, 12),
			truncate(resource.address, 54),
			truncate(resource.resourceType, 32),
			truncate(name, 36),
			issues,
		})
	}
	_ = out.Table([]string{"STATUS", "SOURCE", "ADDRESS", "TYPE", "NAME", "ISSUES"}, rows)
}

func printAuditSection(out *commandOutput, title string, resources []auditResource) {
	out.Header(title, "")
	rows := make([][]string, 0, len(resources))
	for _, resource := range resources {
		issues := "-"
		if len(resource.issues) > 0 {
			issues = strings.Join(resource.issues, "; ")
		}
		stack := resource.stack
		if stack == "" {
			stack = "(no tag)"
		}
		rows = append(rows, []string{
			auditStatusCell(resource.status),
			truncate(resource.resourceType, 30),
			truncate(resource.name, 40),
			truncate(stack, 22),
			issues,
		})
	}
	_ = out.Table([]string{"STATUS", "TYPE", "NAME", "STACK", "ISSUES"}, rows)
}

func sortExpectedResources(resources []auditResource, defs []expectedAuditResource) {
	sourceOrder := make(map[string]int, len(defs))
	order := make(map[string]int, len(defs))
	for _, def := range defs {
		sourceOrder[def.address] = def.sourceOrder
		order[def.address] = def.order
	}

	sort.Slice(resources, func(i, j int) bool {
		leftRank := expectedStatusRank(resources[i].status)
		rightRank := expectedStatusRank(resources[j].status)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftSourceOrder := sourceOrder[resources[i].address]
		rightSourceOrder := sourceOrder[resources[j].address]
		if leftSourceOrder != rightSourceOrder {
			return leftSourceOrder < rightSourceOrder
		}
		return order[resources[i].address] < order[resources[j].address]
	})
}

func expectedStatusRank(status string) int {
	switch status {
	case "OK":
		return 0
	case "SCHEDULED":
		return 1
	case "WARN":
		return 2
	case "MISSING":
		return 3
	default:
		return 4
	}
}

func sortOtherManagedResources(resources []auditResource) {
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].stack != resources[j].stack {
			return resources[i].stack < resources[j].stack
		}
		if resources[i].resourceType != resources[j].resourceType {
			return resources[i].resourceType < resources[j].resourceType
		}
		return resources[i].name < resources[j].name
	})
}

func sortUnownedResources(resources []auditResource) {
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].resourceType != resources[j].resourceType {
			return resources[i].resourceType < resources[j].resourceType
		}
		return resources[i].name < resources[j].name
	})
}

type terraformPlanDocument struct {
	PlannedValues   terraformPlannedValues    `json:"planned_values"`
	ResourceChanges []terraformResourceChange `json:"resource_changes"`
}

type terraformPlannedValues struct {
	RootModule *terraformPlanModule `json:"root_module"`
}

type terraformPlanModule struct {
	Address      string                  `json:"address"`
	Resources    []terraformPlanResource `json:"resources"`
	ChildModules []terraformPlanModule   `json:"child_modules"`
}

type terraformPlanResource struct {
	Address string         `json:"address"`
	Mode    string         `json:"mode"`
	Type    string         `json:"type"`
	Name    string         `json:"name"`
	Values  map[string]any `json:"values"`
}

type terraformResourceChange struct {
	Address string              `json:"address"`
	Mode    string              `json:"mode"`
	Type    string              `json:"type"`
	Change  terraformPlanChange `json:"change"`
}

type terraformPlanChange struct {
	Actions []string `json:"actions"`
}

type activationScheduleDetails struct {
	Name                  string
	GroupName             string
	ARN                   string
	ScheduleExpression    string
	State                 schedulertypes.ScheduleState
	ActionAfterCompletion schedulertypes.ActionAfterCompletion
	TargetARN             string
	TargetRoleARN         string
}

func parseExpectedPlatformOrgResources(data []byte) ([]expectedAuditResource, error) {
	var plan terraformPlanDocument
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("parse terraform show json: %w", err)
	}

	changeByAddress := make(map[string]terraformResourceChange, len(plan.ResourceChanges))
	for _, change := range plan.ResourceChanges {
		if change.Mode != "managed" || excludeTerraformAuditResource(change.Type) {
			continue
		}
		changeByAddress[change.Address] = change
	}

	var planned []terraformPlanResource
	collectTerraformPlannedResources(plan.PlannedValues.RootModule, &planned)

	expected := make([]expectedAuditResource, 0, len(planned))
	for _, resource := range planned {
		status, issues := statusFromTerraformChange(changeByAddress[resource.Address])
		expected = append(expected, expectedAuditResource{
			address:      resource.Address,
			resourceType: resource.Type,
			name:         terraformResourceDisplayName(resource),
			arn:          terraformResourceARN(resource.Values),
			stack:        "platform-org",
			status:       status,
			taggable:     terraformResourceTaggable(resource.Type, resource.Values),
			issues:       issues,
		})
	}
	return expected, nil
}

func collectTerraformPlannedResources(module *terraformPlanModule, out *[]terraformPlanResource) {
	if module == nil {
		return
	}
	for _, resource := range module.Resources {
		if resource.Mode != "managed" || excludeTerraformAuditResource(resource.Type) {
			continue
		}
		*out = append(*out, resource)
	}
	for i := range module.ChildModules {
		collectTerraformPlannedResources(&module.ChildModules[i], out)
	}
}

func excludeTerraformAuditResource(terraformType string) bool {
	switch terraformType {
	case "aws_iam_role_policy",
		"aws_s3_bucket_acl",
		"aws_s3_bucket_lifecycle_configuration",
		"aws_s3_bucket_ownership_controls",
		"aws_s3_bucket_policy",
		"aws_s3_bucket_public_access_block",
		"aws_s3_bucket_server_side_encryption_configuration",
		"aws_s3_bucket_versioning":
		return true
	default:
		return false
	}
}

func statusFromTerraformChange(change terraformResourceChange) (string, []string) {
	actions := change.Change.Actions
	if len(actions) == 0 || equalStringSlices(actions, []string{"no-op"}) || equalStringSlices(actions, []string{"read"}) {
		return "OK", nil
	}
	if equalStringSlices(actions, []string{"create"}) {
		return "MISSING", nil
	}
	return "WARN", []string{"terraform planned action: " + strings.Join(actions, ",")}
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func terraformResourceARN(values map[string]any) string {
	return firstStringValue(values, "arn")
}

func terraformResourceDisplayName(resource terraformPlanResource) string {
	if name := firstStringValue(resource.Values,
		"name",
		"bucket",
		"bucket_prefix",
		"function_name",
		"group_name",
		"role",
		"table_name",
		"url",
		"id",
	); name != "" {
		return name
	}
	return resource.Address
}

func terraformResourceTaggable(terraformType string, values map[string]any) bool {
	if hasStringMap(values["tags"]) || hasStringMap(values["tags_all"]) {
		return true
	}
	switch terraformType {
	case "aws_organizations_organization",
		"aws_organizations_organizational_unit",
		"aws_organizations_policy_attachment":
		return false
	default:
		return strings.HasPrefix(terraformType, "aws_")
	}
}

func firstStringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		if value, ok := raw.(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func hasStringMap(value any) bool {
	if value == nil {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed != nil
	case map[string]string:
		return typed != nil
	default:
		return false
	}
}

func activationSchedule(ctx context.Context, org string) (*activationScheduleDetails, error) {
	out, err := getScheduleFn(ctx, &scheduler.GetScheduleInput{
		GroupName: sdkaws.String(activationScheduleGroupName(org)),
		Name:      sdkaws.String(activationScheduleName(org)),
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}
	targetARN := ""
	targetRoleARN := ""
	if out.Target != nil {
		targetARN = sdkaws.ToString(out.Target.Arn)
		targetRoleARN = sdkaws.ToString(out.Target.RoleArn)
	}
	return &activationScheduleDetails{
		Name:                  sdkaws.ToString(out.Name),
		GroupName:             sdkaws.ToString(out.GroupName),
		ARN:                   sdkaws.ToString(out.Arn),
		ScheduleExpression:    sdkaws.ToString(out.ScheduleExpression),
		State:                 out.State,
		ActionAfterCompletion: out.ActionAfterCompletion,
		TargetARN:             targetARN,
		TargetRoleARN:         targetRoleARN,
	}, nil
}

func listPlatformOrgSchedules(ctx context.Context, org string) ([]activationScheduleDetails, error) {
	groupName := activationScheduleGroupName(org)
	var schedules []activationScheduleDetails
	var nextToken *string
	for {
		out, err := listSchedulesFn(ctx, &scheduler.ListSchedulesInput{
			GroupName: sdkaws.String(groupName),
			NextToken: nextToken,
		})
		if err != nil {
			if isNotFoundError(err) {
				return nil, nil
			}
			return nil, err
		}
		for _, summary := range out.Schedules {
			schedules = append(schedules, activationScheduleDetails{
				Name:      sdkaws.ToString(summary.Name),
				GroupName: sdkaws.ToString(summary.GroupName),
				ARN:       sdkaws.ToString(summary.Arn),
				State:     summary.State,
			})
		}
		if sdkaws.ToString(out.NextToken) == "" {
			break
		}
		nextToken = out.NextToken
	}
	return schedules, nil
}

func activationScheduleName(org string) string {
	return org + "-activate-cost-tags"
}

func activationScheduleGroupName(org string) string {
	return org + "-platform-org"
}

func activateLambdaName(org string) string {
	return org + "-activate-cost-tags"
}

func activateLambdaLogGroupName(org string) string {
	return "/aws/lambda/" + org + "-activate-cost-tags"
}

func schedulerInvokeRoleName(org string) string {
	return org + "-scheduler-invoke-activate"
}

func platformAdminBudgetName(org string) string {
	return org + "-platform-admin-budget"
}

func bootstrapLayerGroupName(org string) string {
	return org + "-bootstrap-layer"
}

func scheduleSummary(schedule activationScheduleDetails) string {
	parts := []string{
		"state=" + string(schedule.State),
	}
	if schedule.ScheduleExpression != "" {
		parts = append(parts, "expr="+schedule.ScheduleExpression)
	}
	if schedule.ActionAfterCompletion != "" {
		parts = append(parts, "after="+string(schedule.ActionAfterCompletion))
	}
	return strings.Join(parts, "  ")
}

func loadPlatformOrgEnvConfig() (platformOrgEnvConfig, error) {
	root, err := repoRoot()
	if err != nil {
		return platformOrgEnvConfig{}, err
	}

	path := filepath.Join(root, envsDirName, d.env, "fetched.auto.tfvars.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return platformOrgEnvConfig{Org: d.org, Accounts: map[string]platformOrgEnvAccountConfig{}}, nil
		}
		return platformOrgEnvConfig{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg platformOrgEnvConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return platformOrgEnvConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Org == "" {
		cfg.Org = d.org
	}
	if cfg.Accounts == nil {
		cfg.Accounts = map[string]platformOrgEnvAccountConfig{}
	}
	return cfg, nil
}

func organizationExists(ctx context.Context) (bool, error) {
	client := organizations.NewFromConfig(d.awsCfg)
	out, err := client.ListRoots(ctx, &organizations.ListRootsInput{})
	if err == nil {
		return len(out.Roots) > 0, nil
	}
	if _, describeErr := client.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{}); describeErr == nil {
		return true, nil
	}
	var notInUse *organizationstypes.AWSOrganizationsNotInUseException
	if strings.Contains(strings.ToLower(err.Error()), "not in use") || strings.Contains(strings.ToLower(err.Error()), "awsorganizationsnotinuse") {
		return false, nil
	}
	if errors.As(err, &notInUse) {
		return false, nil
	}
	return false, err
}

func organizationalUnitExists(ctx context.Context, name string) (bool, error) {
	client := organizations.NewFromConfig(d.awsCfg)
	rootID, err := organizationRootID(ctx, client)
	if err != nil {
		return false, err
	}
	var nextToken *string
	for {
		out, err := client.ListOrganizationalUnitsForParent(ctx, &organizations.ListOrganizationalUnitsForParentInput{
			ParentId:  sdkaws.String(rootID),
			NextToken: nextToken,
		})
		if err != nil {
			return false, err
		}
		for _, ou := range out.OrganizationalUnits {
			if sdkaws.ToString(ou.Name) == name {
				return true, nil
			}
		}
		if sdkaws.ToString(out.NextToken) == "" {
			return false, nil
		}
		nextToken = out.NextToken
	}
}

func organizationPolicyExists(ctx context.Context, name string) (bool, error) {
	client := organizations.NewFromConfig(d.awsCfg)
	policy, err := findOrganizationPolicyByName(ctx, client, name)
	if err != nil {
		return false, err
	}
	return policy != nil, nil
}

func organizationPolicyAttachmentExists(ctx context.Context, policyName, targetName string) (bool, error) {
	client := organizations.NewFromConfig(d.awsCfg)
	policy, err := findOrganizationPolicyByName(ctx, client, policyName)
	if err != nil {
		return false, err
	}
	if policy == nil || policy.Id == nil {
		return false, nil
	}

	var nextToken *string
	for {
		out, err := client.ListTargetsForPolicy(ctx, &organizations.ListTargetsForPolicyInput{
			PolicyId:  policy.Id,
			NextToken: nextToken,
		})
		if err != nil {
			return false, err
		}
		for _, target := range out.Targets {
			if sdkaws.ToString(target.Name) == targetName {
				return true, nil
			}
		}
		if sdkaws.ToString(out.NextToken) == "" {
			return false, nil
		}
		nextToken = out.NextToken
	}
}

func organizationAccountExists(ctx context.Context, name string) (bool, error) {
	client := organizations.NewFromConfig(d.awsCfg)
	var nextToken *string
	for {
		out, err := client.ListAccounts(ctx, &organizations.ListAccountsInput{NextToken: nextToken})
		if err != nil {
			return false, err
		}
		for _, account := range out.Accounts {
			if sdkaws.ToString(account.Name) == name {
				return true, nil
			}
		}
		if sdkaws.ToString(out.NextToken) == "" {
			return false, nil
		}
		nextToken = out.NextToken
	}
}

func organizationRootID(ctx context.Context, client *organizations.Client) (string, error) {
	out, err := client.ListRoots(ctx, &organizations.ListRootsInput{})
	if err != nil {
		return "", err
	}
	if len(out.Roots) == 0 || out.Roots[0].Id == nil {
		return "", fmt.Errorf("organization root not found")
	}
	return sdkaws.ToString(out.Roots[0].Id), nil
}

func findOrganizationPolicyByName(ctx context.Context, client *organizations.Client, name string) (*organizationstypes.PolicySummary, error) {
	var nextToken *string
	for {
		out, err := client.ListPolicies(ctx, &organizations.ListPoliciesInput{
			Filter:    organizationstypes.PolicyTypeServiceControlPolicy,
			NextToken: nextToken,
		})
		if err != nil {
			return nil, err
		}
		for _, policy := range out.Policies {
			if sdkaws.ToString(policy.Name) == name {
				return &policy, nil
			}
		}
		if sdkaws.ToString(out.NextToken) == "" {
			return nil, nil
		}
		nextToken = out.NextToken
	}
}

func s3BucketExists(ctx context.Context, name string) (bool, error) {
	client := s3.NewFromConfig(d.awsCfg)
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: sdkaws.String(name)})
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func dynamoTableExists(ctx context.Context, name string) (bool, error) {
	client := dynamodb.NewFromConfig(d.awsCfg)
	_, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: sdkaws.String(name)})
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func iamRoleExists(ctx context.Context, name string) (bool, error) {
	client := iam.NewFromConfig(d.awsCfg)
	_, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: sdkaws.String(name)})
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func lambdaFunctionExists(ctx context.Context, name string) (bool, error) {
	client := lambda.NewFromConfig(d.awsCfg)
	_, err := client.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: sdkaws.String(name)})
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func logGroupExists(ctx context.Context, name string) (bool, error) {
	client := cloudwatchlogs.NewFromConfig(d.awsCfg)
	var nextToken *string
	for {
		out, err := client.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
			LogGroupNamePrefix: sdkaws.String(name),
			NextToken:          nextToken,
		})
		if err != nil {
			return false, err
		}
		for _, group := range out.LogGroups {
			if sdkaws.ToString(group.LogGroupName) == name {
				return true, nil
			}
		}
		if out.NextToken == nil || sdkaws.ToString(out.NextToken) == "" {
			return false, nil
		}
		nextToken = out.NextToken
	}
}

func resourceGroupExists(ctx context.Context, name string) (bool, error) {
	client := resourcegroups.NewFromConfig(d.awsCfg)
	var nextToken *string
	for {
		out, err := client.ListGroups(ctx, &resourcegroups.ListGroupsInput{NextToken: nextToken})
		if err != nil {
			if isNotFoundError(err) {
				return false, nil
			}
			return false, err
		}
		for _, group := range out.GroupIdentifiers {
			if sdkaws.ToString(group.GroupName) == name {
				return true, nil
			}
		}
		if sdkaws.ToString(out.NextToken) == "" {
			return false, nil
		}
		nextToken = out.NextToken
	}
}

func budgetExists(ctx context.Context, name string) (bool, error) {
	client := budgets.NewFromConfig(d.awsCfg)
	out, err := client.DescribeBudgets(ctx, &budgets.DescribeBudgetsInput{
		AccountId: sdkaws.String(d.accountID),
	})
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	for _, budget := range out.Budgets {
		if sdkaws.ToString(budget.BudgetName) == name {
			return true, nil
		}
	}
	return false, nil
}

func schedulerGroupExists(ctx context.Context, name string) (bool, error) {
	client := scheduler.NewFromConfig(d.awsCfg)
	var nextToken *string
	for {
		out, err := client.ListScheduleGroups(ctx, &scheduler.ListScheduleGroupsInput{NextToken: nextToken})
		if err != nil {
			if isNotFoundError(err) {
				return false, nil
			}
			return false, err
		}
		for _, group := range out.ScheduleGroups {
			if sdkaws.ToString(group.Name) == name {
				return true, nil
			}
		}
		if sdkaws.ToString(out.NextToken) == "" {
			return false, nil
		}
		nextToken = out.NextToken
	}
}

func printBudgetSection(ctx context.Context) {
	out := newWriterOutput(auditStdout, auditStdout, d.ui)
	out.Blank()
	out.Header("Budget & Cost Coverage", "")

	printBudgets(ctx)

	active, err := loadActiveCostTags(ctx)
	if err != nil {
		out.Status("warn", "warn", "cost-tags: could not list: "+err.Error())
		return
	}

	printCostTagStatuses(active)
}

func printBudgets(ctx context.Context) {
	out := newWriterOutput(auditStdout, auditStdout, d.ui)
	budgetsOut, err := describeBudgetsFn(ctx, &budgets.DescribeBudgetsInput{
		AccountId: sdkaws.String(d.accountID),
	})
	if err != nil {
		out.Status("warn", "warn", "budgets: could not list: "+err.Error())
		return
	}
	if len(budgetsOut.Budgets) == 0 {
		out.Status("warn", "warn", "budgets: none found; create a budget to track spend")
		return
	}
	for _, b := range budgetsOut.Budgets {
		out.Status("ok", "ok", fmt.Sprintf("budget %s  $%s/month", sdkaws.ToString(b.BudgetName), sdkaws.ToString(b.BudgetLimit.Amount)))
	}
}

func loadActiveCostTags(ctx context.Context) (map[string]bool, error) {
	active := make(map[string]bool)
	var nextToken *string
	for {
		ceOut, err := listCostAllocationTagsFn(ctx, &costexplorer.ListCostAllocationTagsInput{
			Status:    cetypes.CostAllocationTagStatusActive,
			NextToken: nextToken,
		})
		if err != nil {
			return nil, err
		}

		for _, t := range ceOut.CostAllocationTags {
			active[sdkaws.ToString(t.TagKey)] = true
		}

		if sdkaws.ToString(ceOut.NextToken) == "" {
			return active, nil
		}
		nextToken = ceOut.NextToken
	}
}

func printCostTagStatuses(active map[string]bool) {
	out := newWriterOutput(auditStdout, auditStdout, d.ui)
	requiredCostTags := []string{"Stack", "Project", "Layer", "Owner", "Environment"}
	for _, tag := range requiredCostTags {
		if active[tag] {
			out.Status("ok", "ok", fmt.Sprintf("cost-tag %s active", tag))
			continue
		}
		out.Status("warn", "warn", fmt.Sprintf("cost-tag %s not activated; run platform-org apply", tag))
	}
}

func auditStatusCell(status string) string {
	if d.ui != nil {
		switch status {
		case "OK":
			return d.ui.Badge("ok", "ok")
		case "SCHEDULED":
			return d.ui.Badge("info", "scheduled")
		case "WARN":
			return d.ui.Badge("warn", "warn")
		case "UNOWNED":
			return d.ui.Badge("error", "unowned")
		case "MISSING":
			return d.ui.Badge("error", "missing")
		default:
			return d.ui.Badge("info", strings.ToLower(status))
		}
	}

	switch status {
	case "OK":
		return "OK"
	case "SCHEDULED":
		return "SCHEDULED"
	case "WARN":
		return "WARN"
	case "UNOWNED":
		return "UNOWNED"
	case "MISSING":
		return "MISSING"
	default:
		return status
	}
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func init() {
	rootCmd.AddCommand(auditCmd)
}
