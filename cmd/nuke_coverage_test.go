package cmd

import (
	"errors"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// --- existsByAPICall ---

func TestExistsByAPICallReturnsExistsWhenNoError(t *testing.T) {
	exists, err := existsByAPICall(nil)
	if err != nil || !exists {
		t.Fatalf("existsByAPICall(nil): exists=%v err=%v", exists, err)
	}
}

func TestExistsByAPICallReturnsFalseOnNotFound(t *testing.T) {
	exists, err := existsByAPICall(errors.New("resource not found"))
	if err != nil || exists {
		t.Fatalf("existsByAPICall(notFound): exists=%v err=%v", exists, err)
	}
}

func TestExistsByAPICallReturnsErrorOnOtherError(t *testing.T) {
	someErr := errors.New("access denied")
	exists, err := existsByAPICall(someErr)
	if err == nil || exists {
		t.Fatalf("existsByAPICall(other): exists=%v err=%v", exists, err)
	}
}

// --- nukeCleanupTargetRank ---

func TestNukeCleanupTargetRankSchedulerFirst(t *testing.T) {
	schedRank := nukeCleanupTargetRank(auditResource{resourceType: "scheduler/schedule"})
	orgRank := nukeCleanupTargetRank(auditResource{resourceType: "organizations/organization"})
	defaultRank := nukeCleanupTargetRank(auditResource{resourceType: "unknown/type"})

	if schedRank >= orgRank {
		t.Fatalf("scheduler/schedule should have lower rank than organizations/organization: %d vs %d", schedRank, orgRank)
	}
	if defaultRank <= orgRank {
		t.Fatalf("unknown type default rank (%d) should be higher than org rank (%d)", defaultRank, orgRank)
	}
}

func TestNukeCleanupTargetRankAllKnownTypes(t *testing.T) {
	types := []string{
		"scheduler/schedule",
		"lambda/function",
		"logs/log-group",
		resourceTypeIAMRole,
		"scheduler/schedule-group",
		"resource-groups/group",
		"budgets/budget",
		resourceTypeIAMOIDCProvider,
		"dynamodb/table",
		"s3",
		"organizations/policy-attachment",
		resourceTypeOrganizationsPolicy,
		"organizations/account",
		"organizations/organizational-unit",
		"organizations/organization",
	}
	// All known types should return a rank < 20 (the default).
	for _, rt := range types {
		rank := nukeCleanupTargetRank(auditResource{resourceType: rt})
		if rank >= 20 {
			t.Errorf("resource type %q has default rank %d, expected < 20", rt, rank)
		}
	}
}

// --- nukeCleanupTargetLess ---

func TestNukeCleanupTargetLessByRank(t *testing.T) {
	a := auditResource{resourceType: "scheduler/schedule", name: "z"}
	b := auditResource{resourceType: "organizations/organization", name: "a"}
	if !nukeCleanupTargetLess(a, b) {
		t.Fatal("scheduler/schedule should sort before organizations/organization")
	}
}

func TestNukeCleanupTargetLessByNameWhenSameRankAndType(t *testing.T) {
	a := auditResource{resourceType: "unknown/type", name: "a"}
	b := auditResource{resourceType: "unknown/type", name: "z"}
	if !nukeCleanupTargetLess(a, b) {
		t.Fatal("'a' should sort before 'z' when type and rank are equal")
	}
}

// --- lockItemString ---

func TestLockItemStringReturnsMemberSValue(t *testing.T) {
	item := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberS{Value: "prod/terraform.tfstate"},
	}
	got := lockItemString(item, "LockID")
	if got != "prod/terraform.tfstate" {
		t.Fatalf("lockItemString: %q", got)
	}
}

func TestLockItemStringReturnsEmptyForMissingKey(t *testing.T) {
	item := map[string]dbtypes.AttributeValue{}
	if got := lockItemString(item, "LockID"); got != "" {
		t.Fatalf("expected empty string for missing key, got %q", got)
	}
}

func TestLockItemStringReturnsEmptyForNonStringAttribute(t *testing.T) {
	item := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberN{Value: "42"},
	}
	if got := lockItemString(item, "LockID"); got != "" {
		t.Fatalf("expected empty string for non-string attribute, got %q", got)
	}
}

// --- matchesTerraformLockItem ---

func TestMatchesTerraformLockItemByStateKey(t *testing.T) {
	item := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberS{Value: "prod/terraform.tfstate"},
	}
	if !matchesTerraformLockItem(item, "my-bucket", "prod/terraform.tfstate") {
		t.Fatal("expected item to match by state key")
	}
}

func TestMatchesTerraformLockItemByBucketAndKey(t *testing.T) {
	item := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberS{Value: "my-bucket/prod/terraform.tfstate"},
	}
	if !matchesTerraformLockItem(item, "my-bucket", "prod/terraform.tfstate") {
		t.Fatal("expected item to match by bucket+key combination")
	}
}

func TestMatchesTerraformLockItemNoMatch(t *testing.T) {
	item := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberS{Value: "other-bucket/staging/terraform.tfstate"},
	}
	if matchesTerraformLockItem(item, "my-bucket", "prod/terraform.tfstate") {
		t.Fatal("expected item not to match")
	}
}

// --- appendMatchingVersions ---

func TestAppendMatchingVersionsFiltersOnKey(t *testing.T) {
	src := []s3types.ObjectVersion{
		{Key: sdkaws.String("prod/terraform.tfstate"), VersionId: sdkaws.String("v1")},
		{Key: sdkaws.String("other/key"), VersionId: sdkaws.String("v2")},
		{Key: sdkaws.String("prod/terraform.tfstate"), VersionId: sdkaws.String("v3")},
	}
	dst := appendMatchingVersions(nil, src, "prod/terraform.tfstate")
	if len(dst) != 2 {
		t.Fatalf("expected 2 matching versions, got %d: %+v", len(dst), dst)
	}
}

// --- appendMatchingMarkers ---

func TestAppendMatchingMarkersFiltersOnKey(t *testing.T) {
	src := []s3types.DeleteMarkerEntry{
		{Key: sdkaws.String("prod/terraform.tfstate"), VersionId: sdkaws.String("m1")},
		{Key: sdkaws.String("other/key"), VersionId: sdkaws.String("m2")},
	}
	dst := appendMatchingMarkers(nil, src, "prod/terraform.tfstate")
	if len(dst) != 1 {
		t.Fatalf("expected 1 matching marker, got %d: %+v", len(dst), dst)
	}
}

// --- runtimeStateBucketName / runtimeLockTableName ---

func TestRuntimeStateBucketNameIncludesOrg(t *testing.T) {
	got := runtimeStateBucketName("myorg")
	if got == "" || got == "myorg" {
		t.Fatalf("runtimeStateBucketName: %q", got)
	}
	if len(got) < len("myorg") {
		t.Fatalf("name too short: %q", got)
	}
}

func TestRuntimeLockTableNameIncludesOrg(t *testing.T) {
	got := runtimeLockTableName("myorg")
	if got == "" || got == "myorg" {
		t.Fatalf("runtimeLockTableName: %q", got)
	}
}
