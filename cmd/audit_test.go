package cmd

import (
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	taggingtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
)

func testTag(key, value string) taggingtypes.Tag {
	return taggingtypes.Tag{
		Key:   sdkaws.String(key),
		Value: sdkaws.String(value),
	}
}

func testResourceTagMapping(arn string, tags ...taggingtypes.Tag) taggingtypes.ResourceTagMapping {
	return taggingtypes.ResourceTagMapping{
		ResourceARN: sdkaws.String(arn),
		Tags:        tags,
	}
}

func assertAuditStatus(t *testing.T, r auditResource, want string) {
	t.Helper()

	if r.status != want {
		t.Errorf("want %s got %q", want, r.status)
	}
}

func TestParseARN(t *testing.T) {
	cases := []struct {
		arn      string
		wantType string
		wantName string
	}{
		{
			arn:      "arn:aws:s3:::ffreis-tf-state-root",
			wantType: "s3",
			wantName: "ffreis-tf-state-root",
		},
		{
			arn:      "arn:aws:dynamodb:us-east-1:123456789012:table/ffreis-tf-locks-root",
			wantType: "dynamodb/table",
			wantName: "ffreis-tf-locks-root",
		},
		{
			arn:      "arn:aws:iam::123456789012:role/platform-admin",
			wantType: "iam/role",
			wantName: "platform-admin",
		},
		{
			arn:      "arn:aws:sns:us-east-1:123456789012:ffreis-platform-events",
			wantType: "sns",
			wantName: "ffreis-platform-events",
		},
		{
			arn:      "arn:aws:lambda:us-east-1:123456789012:function:my-function",
			wantType: "lambda/function",
			wantName: "my-function",
		},
		{
			arn:      "arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda/ffreis-activate-cost-tags",
			wantType: "logs/log-group",
			wantName: "/aws/lambda/ffreis-activate-cost-tags",
		},
	}

	for _, tc := range cases {
		t.Run(tc.arn, func(t *testing.T) {
			gotType, gotName := parseARN(tc.arn)
			if gotType != tc.wantType {
				t.Errorf("type: want %q got %q", tc.wantType, gotType)
			}
			if gotName != tc.wantName {
				t.Errorf("name: want %q got %q", tc.wantName, gotName)
			}
		})
	}
}

func TestClassifyResource_Bootstrap(t *testing.T) {
	const testARN = "arn:aws:dynamodb:us-east-1:123:table/ffreis-bootstrap-registry"
	m := testResourceTagMapping(
		testARN,
		testTag("Stack", "bootstrap"),
		testTag("Layer", "bootstrap"),
		testTag("ManagedBy", "platform-bootstrap"),
	)
	r := classifyResource(m)
	assertAuditStatus(t, r, "OK")
	if r.stack != "bootstrap" {
		t.Errorf("want bootstrap got %q", r.stack)
	}
	if r.arn != testARN {
		t.Errorf("arn: want %q got %q", testARN, r.arn)
	}
}

func TestClassifyResource_OwnedAllTags(t *testing.T) {
	m := testResourceTagMapping(
		"arn:aws:s3:::ffreis-tf-state-runtime",
		testTag("Stack", "platform-org"),
		testTag("Project", "platform"),
		testTag("Environment", "prod"),
		testTag("ManagedBy", "terraform"),
	)
	r := classifyResource(m)
	assertAuditStatus(t, r, "OK")
	if len(r.issues) != 0 {
		t.Errorf("want no issues got %v", r.issues)
	}
}

func TestClassifyResource_OwnedMissingTag(t *testing.T) {
	m := testResourceTagMapping(
		"arn:aws:s3:::ffreis-tf-state-runtime",
		testTag("Stack", "flemming"),
		testTag("ManagedBy", "terraform"),
	)
	r := classifyResource(m)
	assertAuditStatus(t, r, "WARN")
	if len(r.issues) == 0 {
		t.Error("want issues for missing tags")
	}
}

func TestClassifyResource_Unowned(t *testing.T) {
	const testARN = "arn:aws:s3:::some-manual-bucket"
	m := testResourceTagMapping(
		testARN,
		testTag("Name", "manual"),
	)
	r := classifyResource(m)
	assertAuditStatus(t, r, "UNOWNED")
	if r.arn != testARN {
		t.Errorf("arn: want %q got %q", testARN, r.arn)
	}
}

func TestClassifyResource_TerraformOwnedUnknownStack(t *testing.T) {
	// A resource with ManagedBy=terraform but an unrecognised Stack value is
	// still terraform-owned — it shows as WARN (missing Project/Environment),
	// not UNOWNED. This is intentional: new stacks are automatically recognised
	// without updating any hardcoded list.
	m := testResourceTagMapping(
		"arn:aws:lambda:us-east-1:123:function:new-stack-lambda",
		testTag("Stack", "new-future-stack"),
		testTag("ManagedBy", "terraform"),
		testTag("Project", "platform"),
		testTag("Environment", "prod"),
	)
	r := classifyResource(m)
	assertAuditStatus(t, r, "OK")
}

func TestClassifyResource_NoManagedBy(t *testing.T) {
	// A resource with a Stack tag but no ManagedBy=terraform is unowned.
	m := testResourceTagMapping(
		"arn:aws:lambda:us-east-1:123:function:rogue-lambda",
		testTag("Stack", "platform-org"),
	)
	r := classifyResource(m)
	assertAuditStatus(t, r, "UNOWNED")
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short string truncated unexpectedly: %q", got)
	}
	long := "hello world foo bar baz"
	got := truncate(long, 10)
	if len([]rune(got)) > 10 {
		t.Errorf("truncate did not shorten enough: %q (%d runes)", got, len([]rune(got)))
	}
}
