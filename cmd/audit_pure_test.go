package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	taggingtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
)

// ---------------------------------------------------------------------------
// appendTaggedResource
// ---------------------------------------------------------------------------

func TestAppendTaggedResourceExists(t *testing.T) {
	old := resourceExistsFn
	defer func() { resourceExistsFn = old }()
	resourceExistsFn = func(context.Context, auditResource) (bool, error) { return true, nil }

	mapping := testResourceTagMapping(
		"arn:aws:s3:::my-bucket",
		testTag("Stack", platformOrgStackTag),
		testTag("Project", "platform"),
		testTag("Environment", "prod"),
		testTag("ManagedBy", "terraform"),
	)
	results := appendTaggedResource(context.Background(), mapping, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(results))
	}
}

func TestAppendTaggedResourceNotExists(t *testing.T) {
	old := resourceExistsFn
	defer func() { resourceExistsFn = old }()
	resourceExistsFn = func(context.Context, auditResource) (bool, error) { return false, nil }

	mapping := testResourceTagMapping(
		"arn:aws:s3:::my-bucket",
		testTag("Stack", platformOrgStackTag),
		testTag("ManagedBy", "terraform"),
	)
	results := appendTaggedResource(context.Background(), mapping, nil)
	if len(results) != 0 {
		t.Fatalf("expected 0 resources (filtered), got %d", len(results))
	}
}

func TestAppendTaggedResourceExistenceError(t *testing.T) {
	old := resourceExistsFn
	defer func() { resourceExistsFn = old }()
	resourceExistsFn = func(context.Context, auditResource) (bool, error) {
		return false, errors.New("existence check failed")
	}

	mapping := testResourceTagMapping(
		"arn:aws:s3:::my-bucket",
		testTag("Stack", platformOrgStackTag),
		testTag("ManagedBy", "terraform"),
	)
	// On error, resource should still be appended (logged + appended)
	results := appendTaggedResource(context.Background(), mapping, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 resource (error path appends), got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// addDiscoveredMatchCandidate
// ---------------------------------------------------------------------------

func TestAddDiscoveredMatchCandidateNoARNNoName(t *testing.T) {
	byARN := make(map[string]auditResource)
	byName := make(map[string][]auditResource)
	resource := auditResource{resourceType: "s3", status: "OK"}
	// neither arn nor name: nothing should be indexed
	addDiscoveredMatchCandidate(byARN, byName, resource)
	if len(byARN) != 0 || len(byName) != 0 {
		t.Fatal("expected empty indexes for resource with no arn and no name")
	}
}

func TestAddDiscoveredMatchCandidateARNOnly(t *testing.T) {
	byARN := make(map[string]auditResource)
	byName := make(map[string][]auditResource)
	resource := auditResource{arn: "arn:aws:s3:::bucket", resourceType: "s3"}
	addDiscoveredMatchCandidate(byARN, byName, resource)
	if _, ok := byARN["arn:aws:s3:::bucket"]; !ok {
		t.Fatal("expected resource to be in ARN index")
	}
	if len(byName) != 0 {
		t.Fatal("expected name index to be empty")
	}
}

func TestAddDiscoveredMatchCandidateNameOnly(t *testing.T) {
	byARN := make(map[string]auditResource)
	byName := make(map[string][]auditResource)
	resource := auditResource{name: "MyBucket", resourceType: "s3"}
	addDiscoveredMatchCandidate(byARN, byName, resource)
	if len(byARN) != 0 {
		t.Fatal("expected ARN index to be empty")
	}
	if list, ok := byName["mybucket"]; !ok || len(list) != 1 {
		t.Fatal("expected resource to be in name index under lowercase key")
	}
}

// ---------------------------------------------------------------------------
// matchExpectedAuditResource
// ---------------------------------------------------------------------------

func TestMatchExpectedAuditResourceByARN(t *testing.T) {
	discovered := auditResource{arn: "arn:aws:s3:::bucket", name: "bucket", status: "OK"}
	byARN := map[string]auditResource{"arn:aws:s3:::bucket": discovered}
	byName := map[string][]auditResource{}

	def := expectedAuditResource{arn: "arn:aws:s3:::bucket", name: "bucket", stack: platformOrgStackTag}
	got, ok := matchExpectedAuditResource(def, byARN, byName)
	if !ok {
		t.Fatal("expected match by ARN")
	}
	if got.arn != "arn:aws:s3:::bucket" {
		t.Fatalf("unexpected matched resource: %#v", got)
	}
}

func TestMatchExpectedAuditResourceNameNoCandidates(t *testing.T) {
	def := expectedAuditResource{name: "missing-resource", stack: platformOrgStackTag}
	_, ok := matchExpectedAuditResource(def, map[string]auditResource{}, map[string][]auditResource{})
	if ok {
		t.Fatal("expected no match when no candidates")
	}
}

func TestMatchExpectedAuditResourceNameOnlyOneCandidate(t *testing.T) {
	candidate := auditResource{name: "my-resource", stack: platformOrgStackTag, status: "OK"}
	byName := map[string][]auditResource{"my-resource": {candidate}}
	def := expectedAuditResource{name: "my-resource", stack: platformOrgStackTag}
	got, ok := matchExpectedAuditResource(def, map[string]auditResource{}, byName)
	if !ok {
		t.Fatal("expected match with single candidate")
	}
	if got.name != "my-resource" {
		t.Fatalf("unexpected matched resource: %#v", got)
	}
}

func TestMatchExpectedAuditResourceNameMultipleCandidatesStackMatch(t *testing.T) {
	c1 := auditResource{name: "my-resource", stack: "other-stack", status: "OK"}
	c2 := auditResource{name: "my-resource", stack: platformOrgStackTag, status: "WARN"}
	byName := map[string][]auditResource{"my-resource": {c1, c2}}
	def := expectedAuditResource{name: "my-resource", stack: platformOrgStackTag}
	got, ok := matchExpectedAuditResource(def, map[string]auditResource{}, byName)
	if !ok {
		t.Fatal("expected match by stack disambiguation")
	}
	if got.stack != platformOrgStackTag {
		t.Fatalf("unexpected matched resource: %#v", got)
	}
}

func TestMatchExpectedAuditResourceNameMultipleCandidatesNoStackMatch(t *testing.T) {
	c1 := auditResource{name: "my-resource", stack: "stack-a", status: "OK"}
	c2 := auditResource{name: "my-resource", stack: "stack-b", status: "OK"}
	byName := map[string][]auditResource{"my-resource": {c1, c2}}
	def := expectedAuditResource{name: "my-resource", stack: platformOrgStackTag}
	_, ok := matchExpectedAuditResource(def, map[string]auditResource{}, byName)
	if ok {
		t.Fatal("expected no match when multiple candidates with no stack match")
	}
}

func TestMatchExpectedAuditResourceNoARNNoName(t *testing.T) {
	def := expectedAuditResource{stack: platformOrgStackTag}
	_, ok := matchExpectedAuditResource(def, map[string]auditResource{}, map[string][]auditResource{})
	if ok {
		t.Fatal("expected no match when def has no arn and no name")
	}
}

// ---------------------------------------------------------------------------
// matchedDiscoveredResourceKey
// ---------------------------------------------------------------------------

func TestMatchedDiscoveredResourceKeyWithARN(t *testing.T) {
	r := auditResource{arn: "arn:aws:s3:::my-bucket", resourceType: "s3", name: "my-bucket", stack: "platform-org"}
	key := matchedDiscoveredResourceKey(r)
	if !strings.HasPrefix(key, "arn:") {
		t.Fatalf("expected key to start with 'arn:' for resource with arn, got %q", key)
	}
	if key != "arn:arn:aws:s3:::my-bucket" {
		t.Fatalf("unexpected key %q", key)
	}
}

func TestMatchedDiscoveredResourceKeyWithoutARN(t *testing.T) {
	r := auditResource{resourceType: "s3", name: "my-bucket", stack: "platform-org"}
	key := matchedDiscoveredResourceKey(r)
	if strings.HasPrefix(key, "arn:") {
		t.Fatalf("expected key not to start with 'arn:' for resource without arn, got %q", key)
	}
	// should be stack|type|name lowercased
	expected := "platform-org|s3|my-bucket"
	if key != expected {
		t.Fatalf("expected key %q, got %q", expected, key)
	}
}

// ---------------------------------------------------------------------------
// parseARN
// ---------------------------------------------------------------------------

func TestParseARNColonBeforeSlash(t *testing.T) {
	// colon appears before slash: e.g. lambda function ARN
	arn := "arn:aws:lambda:us-east-1:123456789012:function:my-function"
	rtype, name := parseARN(arn)
	if rtype != "lambda/function" {
		t.Errorf("rtype: want lambda/function got %q", rtype)
	}
	if name != "my-function" {
		t.Errorf("name: want my-function got %q", name)
	}
}

func TestParseARNSlashBeforeColon(t *testing.T) {
	// slash appears before colon: e.g. dynamodb table
	arn := "arn:aws:dynamodb:us-east-1:123456789012:table/my-table"
	rtype, name := parseARN(arn)
	if rtype != "dynamodb/table" {
		t.Errorf("rtype: want dynamodb/table got %q", rtype)
	}
	if name != "my-table" {
		t.Errorf("name: want my-table got %q", name)
	}
}

func TestParseARNServiceOnly(t *testing.T) {
	// No separator: service-level resource (e.g. S3 bucket with no path)
	arn := "arn:aws:s3:::my-bucket"
	rtype, name := parseARN(arn)
	if rtype != "s3" {
		t.Errorf("rtype: want s3 got %q", rtype)
	}
	if name != "my-bucket" {
		t.Errorf("name: want my-bucket got %q", name)
	}
}

func TestParseARNShortARN(t *testing.T) {
	// Fewer than 6 colon-separated parts: fallback to unknown + whole arn
	arn := "arn:aws:s3"
	rtype, name := parseARN(arn)
	if rtype != "unknown" {
		t.Errorf("rtype: want unknown got %q", rtype)
	}
	if name != arn {
		t.Errorf("name: want %q got %q", arn, name)
	}
}

// ---------------------------------------------------------------------------
// sortExpectedResources
// ---------------------------------------------------------------------------

func TestSortExpectedResourcesDifferentSourceOrders(t *testing.T) {
	defs := []expectedAuditResource{
		{address: "addr-a", sourceOrder: 1, order: 0},
		{address: "addr-b", sourceOrder: 0, order: 0},
	}
	resources := []auditResource{
		{address: "addr-a", status: "OK"},
		{address: "addr-b", status: "OK"},
	}
	sortExpectedResources(resources, defs)
	// addr-b has lower sourceOrder (0) so it should come first
	if resources[0].address != "addr-b" {
		t.Errorf("expected addr-b first, got %q", resources[0].address)
	}
}

func TestSortExpectedResourcesDifferentOrders(t *testing.T) {
	defs := []expectedAuditResource{
		{address: "addr-x", sourceOrder: 0, order: 2},
		{address: "addr-y", sourceOrder: 0, order: 1},
	}
	resources := []auditResource{
		{address: "addr-x", status: "OK"},
		{address: "addr-y", status: "OK"},
	}
	sortExpectedResources(resources, defs)
	// addr-y has lower order (1) so it should come first
	if resources[0].address != "addr-y" {
		t.Errorf("expected addr-y first, got %q", resources[0].address)
	}
}

func TestSortExpectedResourcesByStatus(t *testing.T) {
	defs := []expectedAuditResource{
		{address: "missing-res", sourceOrder: 0, order: 0},
		{address: "ok-res", sourceOrder: 0, order: 1},
	}
	resources := []auditResource{
		{address: "missing-res", status: "MISSING"},
		{address: "ok-res", status: "OK"},
	}
	sortExpectedResources(resources, defs)
	if resources[0].status != "OK" {
		t.Errorf("expected OK first, got %q", resources[0].status)
	}
	if resources[1].status != "MISSING" {
		t.Errorf("expected MISSING second, got %q", resources[1].status)
	}
}

// ---------------------------------------------------------------------------
// parseExpectedPlatformOrgResources
// ---------------------------------------------------------------------------

func TestParseExpectedPlatformOrgResourcesInvalidJSON(t *testing.T) {
	_, err := parseExpectedPlatformOrgResources([]byte(`not valid json`))
	if err == nil || !strings.Contains(err.Error(), "parse terraform show json") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestParseExpectedPlatformOrgResourcesNoOpAction(t *testing.T) {
	data := []byte(`{
		"planned_values": {
			"root_module": {
				"resources": [
					{
						"address": "aws_s3_bucket.test",
						"mode": "managed",
						"type": "aws_s3_bucket",
						"name": "test",
						"values": {"bucket": "test-bucket"}
					}
				]
			}
		},
		"resource_changes": [
			{
				"address": "aws_s3_bucket.test",
				"mode": "managed",
				"type": "aws_s3_bucket",
				"change": {"actions": ["no-op"]}
			}
		]
	}`)
	defs, err := parseExpectedPlatformOrgResources(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defs) != 1 || defs[0].status != "OK" {
		t.Fatalf("expected 1 no-op resource with status OK, got %#v", defs)
	}
}

func TestParseExpectedPlatformOrgResourcesCreateAction(t *testing.T) {
	data := []byte(`{
		"planned_values": {
			"root_module": {
				"resources": [
					{
						"address": "aws_sqs_queue.new",
						"mode": "managed",
						"type": "aws_sqs_queue",
						"name": "new",
						"values": {"name": "my-queue"}
					}
				]
			}
		},
		"resource_changes": [
			{
				"address": "aws_sqs_queue.new",
				"mode": "managed",
				"type": "aws_sqs_queue",
				"change": {"actions": ["create"]}
			}
		]
	}`)
	defs, err := parseExpectedPlatformOrgResources(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defs) != 1 || defs[0].status != "MISSING" {
		t.Fatalf("expected 1 create resource with status MISSING, got %#v", defs)
	}
}

func TestParseExpectedPlatformOrgResourcesWarnAction(t *testing.T) {
	data := []byte(`{
		"planned_values": {
			"root_module": {
				"resources": [
					{
						"address": "aws_sqs_queue.replacing",
						"mode": "managed",
						"type": "aws_sqs_queue",
						"name": "replacing",
						"values": {"name": "my-queue"}
					}
				]
			}
		},
		"resource_changes": [
			{
				"address": "aws_sqs_queue.replacing",
				"mode": "managed",
				"type": "aws_sqs_queue",
				"change": {"actions": ["delete", "create"]}
			}
		]
	}`)
	defs, err := parseExpectedPlatformOrgResources(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defs) != 1 || defs[0].status != "WARN" {
		t.Fatalf("expected 1 replace resource with status WARN, got %#v", defs)
	}
	if len(defs[0].issues) == 0 || !strings.Contains(defs[0].issues[0], "terraform planned action") {
		t.Fatalf("expected issue about planned action, got %#v", defs[0].issues)
	}
}

func TestParseExpectedPlatformOrgResourcesExcludedType(t *testing.T) {
	data := []byte(`{
		"planned_values": {
			"root_module": {
				"resources": [
					{
						"address": "aws_s3_bucket_policy.one",
						"mode": "managed",
						"type": "aws_s3_bucket_policy",
						"name": "one",
						"values": {}
					}
				]
			}
		},
		"resource_changes": []
	}`)
	defs, err := parseExpectedPlatformOrgResources(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defs) != 0 {
		t.Fatalf("expected excluded type to be filtered, got %#v", defs)
	}
}

// ---------------------------------------------------------------------------
// collectTerraformPlannedResources
// ---------------------------------------------------------------------------

func TestCollectTerraformPlannedResourcesNilModule(t *testing.T) {
	var out []terraformPlanResource
	collectTerraformPlannedResources(nil, &out)
	if len(out) != 0 {
		t.Fatalf("expected 0 resources from nil module, got %d", len(out))
	}
}

func TestCollectTerraformPlannedResourcesManagedResources(t *testing.T) {
	module := &terraformPlanModule{
		Resources: []terraformPlanResource{
			{Address: "aws_s3_bucket.a", Mode: "managed", Type: "aws_s3_bucket"},
			{Address: "data.aws_region.current", Mode: "data", Type: "aws_region"},
		},
	}
	var out []terraformPlanResource
	collectTerraformPlannedResources(module, &out)
	if len(out) != 1 || out[0].Address != "aws_s3_bucket.a" {
		t.Fatalf("expected only managed resource, got %#v", out)
	}
}

func TestCollectTerraformPlannedResourcesChildModules(t *testing.T) {
	module := &terraformPlanModule{
		Resources: []terraformPlanResource{
			{Address: "aws_iam_role.parent", Mode: "managed", Type: "aws_iam_role"},
		},
		ChildModules: []terraformPlanModule{
			{
				Address: "module.child",
				Resources: []terraformPlanResource{
					{Address: "module.child.aws_sqs_queue.q", Mode: "managed", Type: "aws_sqs_queue"},
				},
			},
		},
	}
	var out []terraformPlanResource
	collectTerraformPlannedResources(module, &out)
	if len(out) != 2 {
		t.Fatalf("expected 2 resources (parent + child), got %d: %#v", len(out), out)
	}
}

func TestCollectTerraformPlannedResourcesExcludedType(t *testing.T) {
	module := &terraformPlanModule{
		Resources: []terraformPlanResource{
			{Address: "aws_iam_role_policy.p", Mode: "managed", Type: "aws_iam_role_policy"},
			{Address: "aws_s3_bucket.b", Mode: "managed", Type: "aws_s3_bucket"},
		},
	}
	var out []terraformPlanResource
	collectTerraformPlannedResources(module, &out)
	if len(out) != 1 || out[0].Type != "aws_s3_bucket" {
		t.Fatalf("expected excluded type to be filtered, got %#v", out)
	}
}

// ---------------------------------------------------------------------------
// statusFromTerraformChange
// ---------------------------------------------------------------------------

func TestStatusFromTerraformChangeNoActions(t *testing.T) {
	status, issues := statusFromTerraformChange(terraformResourceChange{})
	if status != "OK" || len(issues) != 0 {
		t.Fatalf("expected OK with no issues, got status=%q issues=%v", status, issues)
	}
}

func TestStatusFromTerraformChangeNoOp(t *testing.T) {
	change := terraformResourceChange{Change: terraformPlanChange{Actions: []string{"no-op"}}}
	status, issues := statusFromTerraformChange(change)
	if status != "OK" || len(issues) != 0 {
		t.Fatalf("expected OK with no issues, got status=%q issues=%v", status, issues)
	}
}

func TestStatusFromTerraformChangeRead(t *testing.T) {
	change := terraformResourceChange{Change: terraformPlanChange{Actions: []string{"read"}}}
	status, issues := statusFromTerraformChange(change)
	if status != "OK" || len(issues) != 0 {
		t.Fatalf("expected OK with no issues, got status=%q issues=%v", status, issues)
	}
}

func TestStatusFromTerraformChangeCreate(t *testing.T) {
	change := terraformResourceChange{Change: terraformPlanChange{Actions: []string{"create"}}}
	status, issues := statusFromTerraformChange(change)
	if status != "MISSING" || len(issues) != 0 {
		t.Fatalf("expected MISSING, got status=%q issues=%v", status, issues)
	}
}

func TestStatusFromTerraformChangeReplace(t *testing.T) {
	change := terraformResourceChange{Change: terraformPlanChange{Actions: []string{"delete", "create"}}}
	status, issues := statusFromTerraformChange(change)
	if status != "WARN" {
		t.Fatalf("expected WARN, got %q", status)
	}
	if len(issues) == 0 || !strings.Contains(issues[0], "delete,create") {
		t.Fatalf("expected issue with actions, got %v", issues)
	}
}

// ---------------------------------------------------------------------------
// terraformResourceDisplayName
// ---------------------------------------------------------------------------

func TestTerraformResourceDisplayNameBucket(t *testing.T) {
	r := terraformPlanResource{Address: "aws_s3_bucket.x", Values: map[string]any{"bucket": "my-bucket"}}
	if got := terraformResourceDisplayName(r); got != "my-bucket" {
		t.Errorf("bucket: want my-bucket got %q", got)
	}
}

func TestTerraformResourceDisplayNameFunctionName(t *testing.T) {
	r := terraformPlanResource{Address: "aws_lambda_function.x", Values: map[string]any{"function_name": "my-fn"}}
	if got := terraformResourceDisplayName(r); got != "my-fn" {
		t.Errorf("function_name: want my-fn got %q", got)
	}
}

func TestTerraformResourceDisplayNameGroupName(t *testing.T) {
	r := terraformPlanResource{Address: "aws_iam_group.x", Values: map[string]any{"group_name": "my-group"}}
	if got := terraformResourceDisplayName(r); got != "my-group" {
		t.Errorf("group_name: want my-group got %q", got)
	}
}

func TestTerraformResourceDisplayNameRole(t *testing.T) {
	r := terraformPlanResource{Address: "aws_iam_role.x", Values: map[string]any{"role": "my-role"}}
	if got := terraformResourceDisplayName(r); got != "my-role" {
		t.Errorf("role: want my-role got %q", got)
	}
}

func TestTerraformResourceDisplayNameTableName(t *testing.T) {
	r := terraformPlanResource{Address: "aws_dynamodb_table.x", Values: map[string]any{"table_name": "my-table"}}
	if got := terraformResourceDisplayName(r); got != "my-table" {
		t.Errorf("table_name: want my-table got %q", got)
	}
}

func TestTerraformResourceDisplayNameURL(t *testing.T) {
	r := terraformPlanResource{Address: "some_resource.x", Values: map[string]any{"url": "https://example.com"}}
	if got := terraformResourceDisplayName(r); got != "https://example.com" {
		t.Errorf("url: want https://example.com got %q", got)
	}
}

func TestTerraformResourceDisplayNameID(t *testing.T) {
	r := terraformPlanResource{Address: "some_resource.x", Values: map[string]any{"id": "res-id-123"}}
	if got := terraformResourceDisplayName(r); got != "res-id-123" {
		t.Errorf("id: want res-id-123 got %q", got)
	}
}

func TestTerraformResourceDisplayNameFallback(t *testing.T) {
	r := terraformPlanResource{Address: "some_resource.x", Values: map[string]any{}}
	if got := terraformResourceDisplayName(r); got != "some_resource.x" {
		t.Errorf("fallback: want some_resource.x got %q", got)
	}
}

// ---------------------------------------------------------------------------
// terraformResourceTaggable
// ---------------------------------------------------------------------------

func TestTerraformResourceTaggableWithTagsField(t *testing.T) {
	values := map[string]any{"tags": map[string]any{"Stack": "platform-org"}}
	if !terraformResourceTaggable("aws_s3_bucket", values) {
		t.Fatal("expected taggable when tags field present")
	}
}

func TestTerraformResourceTaggableWithTagsAllField(t *testing.T) {
	values := map[string]any{"tags_all": map[string]any{"Stack": "platform-org"}}
	if !terraformResourceTaggable("aws_sqs_queue", values) {
		t.Fatal("expected taggable when tags_all field present")
	}
}

func TestTerraformResourceTaggableAWSTypeNoTags(t *testing.T) {
	values := map[string]any{}
	if !terraformResourceTaggable("aws_iam_role", values) {
		t.Fatal("expected aws_ type to be taggable even without tags field")
	}
}

func TestTerraformResourceTaggableNonAWSType(t *testing.T) {
	values := map[string]any{}
	if terraformResourceTaggable("null_resource", values) {
		t.Fatal("expected non-aws_ type to not be taggable")
	}
}

func TestTerraformResourceTaggableExcludedOrganizationsTypes(t *testing.T) {
	values := map[string]any{}
	for _, typ := range []string{
		"aws_organizations_organization",
		"aws_organizations_organizational_unit",
		"aws_organizations_policy_attachment",
	} {
		if terraformResourceTaggable(typ, values) {
			t.Errorf("expected %q to not be taggable", typ)
		}
	}
}

// ---------------------------------------------------------------------------
// activationSchedule
// ---------------------------------------------------------------------------

func TestActivationScheduleFound(t *testing.T) {
	old := getScheduleFn
	defer func() { getScheduleFn = old }()

	schedName := "my-org-activate-cost-tags"
	groupName := "my-org-platform-org"
	schedARN := "arn:aws:scheduler:::schedule/" + groupName + "/" + schedName

	getScheduleFn = func(_ context.Context, input *scheduler.GetScheduleInput) (*scheduler.GetScheduleOutput, error) {
		return &scheduler.GetScheduleOutput{
			Name:               sdkaws.String(schedName),
			GroupName:          sdkaws.String(groupName),
			Arn:                sdkaws.String(schedARN),
			ScheduleExpression: sdkaws.String("at(2026-01-01T00:00:00)"),
			State:              schedulertypes.ScheduleStateEnabled,
			Target: &schedulertypes.Target{
				Arn:     sdkaws.String("arn:aws:lambda:::function:my-fn"),
				RoleArn: sdkaws.String("arn:aws:iam:::role/my-role"),
			},
		}, nil
	}

	details, err := activationSchedule(context.Background(), "my-org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if details == nil {
		t.Fatal("expected schedule details, got nil")
	}
	if details.Name != schedName || details.ARN != schedARN {
		t.Fatalf("unexpected details: %#v", details)
	}
	if details.TargetARN == "" || details.TargetRoleARN == "" {
		t.Fatalf("expected target ARNs to be populated: %#v", details)
	}
}

func TestActivationScheduleNotFound(t *testing.T) {
	old := getScheduleFn
	defer func() { getScheduleFn = old }()

	getScheduleFn = func(context.Context, *scheduler.GetScheduleInput) (*scheduler.GetScheduleOutput, error) {
		return nil, errors.New("resource not found")
	}

	details, err := activationSchedule(context.Background(), "my-org")
	if err != nil {
		t.Fatalf("expected nil error for not-found, got: %v", err)
	}
	if details != nil {
		t.Fatalf("expected nil details for not-found, got: %#v", details)
	}
}

func TestActivationScheduleError(t *testing.T) {
	old := getScheduleFn
	defer func() { getScheduleFn = old }()

	getScheduleFn = func(context.Context, *scheduler.GetScheduleInput) (*scheduler.GetScheduleOutput, error) {
		return nil, errors.New("unexpected scheduler failure")
	}

	details, err := activationSchedule(context.Background(), "my-org")
	if err == nil {
		t.Fatal("expected error for scheduler failure")
	}
	if details != nil {
		t.Fatalf("expected nil details on error, got: %#v", details)
	}
}

// ---------------------------------------------------------------------------
// listPlatformOrgSchedules
// ---------------------------------------------------------------------------

func TestListPlatformOrgSchedulesEmpty(t *testing.T) {
	old := listSchedulesFn
	defer func() { listSchedulesFn = old }()

	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return &scheduler.ListSchedulesOutput{Schedules: nil}, nil
	}

	schedules, err := listPlatformOrgSchedules(context.Background(), "my-org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(schedules) != 0 {
		t.Fatalf("expected 0 schedules, got %d", len(schedules))
	}
}

func TestListPlatformOrgSchedulesPaginated(t *testing.T) {
	old := listSchedulesFn
	defer func() { listSchedulesFn = old }()

	calls := 0
	listSchedulesFn = func(_ context.Context, input *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		calls++
		switch calls {
		case 1:
			return &scheduler.ListSchedulesOutput{
				Schedules: []schedulertypes.ScheduleSummary{
					{Name: sdkaws.String("sched-1"), GroupName: sdkaws.String("my-org-platform-org"), Arn: sdkaws.String("arn:1"), State: schedulertypes.ScheduleStateEnabled},
				},
				NextToken: sdkaws.String("page2"),
			}, nil
		case 2:
			if sdkaws.ToString(input.NextToken) != "page2" {
				t.Errorf("expected page2 token, got %q", sdkaws.ToString(input.NextToken))
			}
			return &scheduler.ListSchedulesOutput{
				Schedules: []schedulertypes.ScheduleSummary{
					{Name: sdkaws.String("sched-2"), GroupName: sdkaws.String("my-org-platform-org"), Arn: sdkaws.String("arn:2"), State: schedulertypes.ScheduleStateDisabled},
				},
			}, nil
		default:
			t.Errorf("unexpected call %d", calls)
			return nil, nil
		}
	}

	schedules, err := listPlatformOrgSchedules(context.Background(), "my-org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(schedules) != 2 {
		t.Fatalf("expected 2 schedules, got %d", len(schedules))
	}
}

func TestListPlatformOrgSchedulesNotFound(t *testing.T) {
	old := listSchedulesFn
	defer func() { listSchedulesFn = old }()

	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return nil, errors.New("resource not found")
	}

	schedules, err := listPlatformOrgSchedules(context.Background(), "my-org")
	if err != nil {
		t.Fatalf("expected nil error for not-found, got: %v", err)
	}
	if schedules != nil {
		t.Fatalf("expected nil schedules for not-found, got: %#v", schedules)
	}
}

func TestListPlatformOrgSchedulesError(t *testing.T) {
	old := listSchedulesFn
	defer func() { listSchedulesFn = old }()

	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return nil, errors.New("list schedules failed")
	}

	_, err := listPlatformOrgSchedules(context.Background(), "my-org")
	if err == nil || !strings.Contains(err.Error(), "list schedules failed") {
		t.Fatalf("expected error containing 'list schedules failed', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// printExpectedAuditSection
// ---------------------------------------------------------------------------

func TestPrintExpectedAuditSectionContainsHeadersAndData(t *testing.T) {
	var buf bytes.Buffer
	out := newWriterOutput(&buf, &buf, nil)
	resources := []auditResource{
		{
			status:       "OK",
			source:       "terraform",
			address:      "aws_s3_bucket.test",
			resourceType: "s3",
			name:         "test-bucket",
			issues:       nil,
		},
		{
			status:       "WARN",
			source:       "terraform",
			address:      "aws_iam_role.admin",
			resourceType: "iam/role",
			name:         "admin-role",
			issues:       []string{"missing tag: Project"},
		},
		{
			status:       "MISSING",
			source:       "terraform",
			address:      "aws_sqs_queue.jobs",
			resourceType: "sqs",
			name:         "",
			issues:       nil,
		},
	}
	printExpectedAuditSection(out, "Expected Platform Org Resources", resources)

	output := buf.String()
	for _, want := range []string{"Expected Platform Org Resources", "STATUS", "SOURCE", "ADDRESS", "TYPE", "NAME", "ISSUES"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, output)
		}
	}
	if !strings.Contains(output, "test-bucket") {
		t.Errorf("expected output to contain resource name 'test-bucket'")
	}
	if !strings.Contains(output, "missing tag: Project") {
		t.Errorf("expected output to contain issue text")
	}
	// Missing name should be rendered as dash
	if !strings.Contains(output, "-") {
		t.Errorf("expected output to contain '-' for missing name")
	}
}

// ---------------------------------------------------------------------------
// buildAuditSections — liveManaged error branch (not included)
// ---------------------------------------------------------------------------

func TestBuildAuditSectionsLiveManagedErrorFallback(t *testing.T) {
	oldSources := inventorySourcesFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	defer func() {
		inventorySourcesFn = oldSources
		platformOrgCleanupTargetsForNukeFn = oldTargets
	}()

	inventorySourcesFn = func() []inventorySource {
		return []inventorySource{
			stubInventorySource{
				id: "terraform",
				loadFn: func(context.Context) (inventorySourceResult, error) {
					return inventorySourceResult{
						expected: []expectedAuditResource{
							{
								address:      "aws_s3_bucket.runtime",
								resourceType: "s3",
								name:         "runtime-bucket",
								stack:        platformOrgStackTag,
								status:       "MISSING",
							},
						},
					}, nil
				},
			},
		}
	}
	// Force liveManaged to error so the fallback path is taken
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, errors.New("live inventory unavailable")
	}

	discovered := []auditResource{
		{status: "OK", resourceType: "sns", name: "some-topic", stack: "other"},
	}

	sections, err := buildAuditSections(context.Background(), discovered)
	if err != nil {
		t.Fatalf("buildAuditSections: %v", err)
	}
	// expected should still have the resource (as MISSING since live inventory failed)
	if len(sections.expected) != 1 {
		t.Fatalf("expected 1 expected resource, got %d", len(sections.expected))
	}
	if sections.expected[0].status != "MISSING" {
		t.Fatalf("expected MISSING status when live inventory fails, got %q", sections.expected[0].status)
	}
}

// ---------------------------------------------------------------------------
// appendTaggedResource helpers — testing with no-log path
// ---------------------------------------------------------------------------

func TestAppendTaggedResourceExistenceErrorNoLog(t *testing.T) {
	old := resourceExistsFn
	defer func() { resourceExistsFn = old }()
	resourceExistsFn = func(context.Context, auditResource) (bool, error) {
		return false, errors.New("check failed")
	}

	// Ensure d.log is nil so the log branch is skipped
	oldLog := d.log
	d.log = nil
	defer func() { d.log = oldLog }()

	mapping := taggingtypes.ResourceTagMapping{
		ResourceARN: sdkaws.String("arn:aws:s3:::test-bucket"),
		Tags: []taggingtypes.Tag{
			{Key: sdkaws.String("ManagedBy"), Value: sdkaws.String("terraform")},
			{Key: sdkaws.String("Stack"), Value: sdkaws.String(platformOrgStackTag)},
		},
	}
	results := appendTaggedResource(context.Background(), mapping, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 resource on error (no log), got %d", len(results))
	}
}
