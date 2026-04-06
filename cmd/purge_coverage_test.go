package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const (
	testPurgeBaseError          = "base error"
	testPurgeCoverageBucketName = "my-bucket"
	testPurgeSummaryErrorf      = "summary: %+v"
	testPurgeCountsErrorf       = "counts: %+v"
)

func TestPurgeManualErrorWithHint(t *testing.T) {
	cause := errors.New(testPurgeBaseError)
	e := &purgeManualError{cause: cause, hint: "do this instead"}
	got := e.Error()
	if !strings.Contains(got, testPurgeBaseError) || !strings.Contains(got, "do this instead") {
		t.Fatalf("purgeManualError.Error(): %q", got)
	}
}

func TestPurgeManualErrorWithoutHint(t *testing.T) {
	cause := errors.New(testPurgeBaseError)
	e := &purgeManualError{cause: cause}
	if e.Error() != testPurgeBaseError {
		t.Fatalf("purgeManualError.Error(): %q", e.Error())
	}
}

func TestPurgeManualErrorUnwrap(t *testing.T) {
	cause := errors.New("root cause")
	e := &purgeManualError{cause: cause}
	if !errors.Is(e, cause) {
		t.Fatal("Unwrap should expose the underlying cause")
	}
}

func TestParseServiceTypeSingle(t *testing.T) {
	svc, full := parseServiceType("s3")
	if svc != "s3" || full != "s3" {
		t.Fatalf("parseServiceType(s3) = (%q, %q)", svc, full)
	}
}

func TestParseServiceTypeWithSlash(t *testing.T) {
	svc, full := parseServiceType("lambda/function")
	if svc != "lambda" || full != "lambda/function" {
		t.Fatalf("parseServiceType(lambda/function) = (%q, %q)", svc, full)
	}
}

func TestIAMToCloudControlRole(t *testing.T) {
	cfnType, id := iamToCloudControl("iam/role", "my-role", "arn:aws:iam::123:role/my-role")
	if cfnType != "AWS::IAM::Role" || id != "my-role" {
		t.Fatalf("iamToCloudControl(iam/role) = (%q, %q)", cfnType, id)
	}
}

func TestIAMToCloudControlPolicy(t *testing.T) {
	arn := "arn:aws:iam::123:policy/my-policy"
	cfnType, id := iamToCloudControl("iam/policy", "my-policy", arn)
	if cfnType != "AWS::IAM::ManagedPolicy" || id != arn {
		t.Fatalf("iamToCloudControl(iam/policy) = (%q, %q)", cfnType, id)
	}
}

func TestIAMToCloudControlUnknown(t *testing.T) {
	cfnType, id := iamToCloudControl("iam/unknown", "name", "arn")
	if cfnType != "" || id != "" {
		t.Fatalf("expected empty for unknown iam type, got (%q, %q)", cfnType, id)
	}
}

func TestEC2ToCloudControlVPC(t *testing.T) {
	cfnType, id := ec2ToCloudControl("ec2/vpc", "vpc-123")
	if cfnType != "AWS::EC2::VPC" || id != "vpc-123" {
		t.Fatalf("ec2ToCloudControl(ec2/vpc) = (%q, %q)", cfnType, id)
	}
}

func TestEC2ToCloudControlUnknown(t *testing.T) {
	cfnType, id := ec2ToCloudControl("ec2/unknown", "name")
	if cfnType != "" || id != "" {
		t.Fatalf("expected empty for unknown ec2 type, got (%q, %q)", cfnType, id)
	}
}

func TestECSToCloudControlTaskDefinition(t *testing.T) {
	arn := "arn:aws:ecs:us-east-1:123:task-definition/name:1"
	cfnType, id := ecsToCloudControl("ecs/task-definition", "name:1", arn)
	if cfnType != "AWS::ECS::TaskDefinition" || id != arn {
		t.Fatalf("ecsToCloudControl(ecs/task-definition) = (%q, %q)", cfnType, id)
	}
}

func TestECSToCloudControlUnknown(t *testing.T) {
	cfnType, id := ecsToCloudControl("ecs/unknown", "name", "arn")
	if cfnType != "" || id != "" {
		t.Fatalf("expected empty for unknown ecs type, got (%q, %q)", cfnType, id)
	}
}

func TestLightsailToCloudControlStaticIP(t *testing.T) {
	cfnType, id := lightsailToCloudControl("lightsail/StaticIp", "my-ip")
	if cfnType != "AWS::Lightsail::StaticIp" || id != "my-ip" {
		t.Fatalf("lightsailToCloudControl(lightsail/StaticIp) = (%q, %q)", cfnType, id)
	}
}

func TestLightsailToCloudControlUnknown(t *testing.T) {
	cfnType, id := lightsailToCloudControl("lightsail/Unknown", "name")
	if cfnType != "" || id != "" {
		t.Fatalf("expected empty for unknown lightsail type, got (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlS3(t *testing.T) {
	cfnType, id := arnToCloudControl("arn:aws:s3:::"+testPurgeCoverageBucketName, "s3", "s3", testPurgeCoverageBucketName)
	if cfnType != "AWS::S3::Bucket" || id != testPurgeCoverageBucketName {
		t.Fatalf("arnToCloudControl(s3) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlDynamoDB(t *testing.T) {
	cfnType, id := arnToCloudControl("arn", "dynamodb", "dynamodb/table", "my-table")
	if cfnType != "AWS::DynamoDB::Table" || id != "my-table" {
		t.Fatalf("arnToCloudControl(dynamodb/table) = (%q, %q)", cfnType, id)
	}
}

func TestArnToCloudControlUnknownService(t *testing.T) {
	cfnType, id := arnToCloudControl("arn", "unknownsvc", "unknownsvc/type", "name")
	if cfnType != "" || id != "" {
		t.Fatalf("expected empty for unknown service, got (%q, %q)", cfnType, id)
	}
}

func TestRecordFallbackDeleteResultDeleted(t *testing.T) {
	o, _, _ := newPlainOutput(t)
	summary := nukeFallbackSummary{}
	errs := recordFallbackDeleteResult(o, auditResource{resourceType: "s3", name: "my-bucket"}, nil, &summary, nil)
	if summary.Deleted != 1 || summary.Failed != 0 {
		t.Fatalf(testPurgeSummaryErrorf, summary)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestRecordFallbackDeleteResultGone(t *testing.T) {
	o, _, _ := newPlainOutput(t)
	summary := nukeFallbackSummary{}
	errs := recordFallbackDeleteResult(o, auditResource{resourceType: "s3", name: "b"}, errors.New("not found"), &summary, nil)
	if summary.Gone != 1 {
		t.Fatalf(testPurgeSummaryErrorf, summary)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestRecordFallbackDeleteResultFailed(t *testing.T) {
	o, _, _ := newPlainOutput(t)
	summary := nukeFallbackSummary{}
	errs := recordFallbackDeleteResult(o, auditResource{resourceType: "s3", name: "b"}, errors.New("access denied"), &summary, nil)
	if summary.Failed != 1 {
		t.Fatalf(testPurgeSummaryErrorf, summary)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got: %v", errs)
	}
}

func TestRecordPurgeDeleteResultGone(t *testing.T) {
	o, out, _ := newPlainOutput(t)
	counts := purgeDeleteCounts{}
	recordPurgeDeleteResult(o, auditResource{resourceType: "s3", name: "b"}, errors.New("not found"), &counts, false)
	if counts.gone != 1 {
		t.Fatalf(testPurgeCountsErrorf, counts)
	}
	if !strings.Contains(out.String(), "skip") {
		t.Fatalf("expected skip in output: %q", out.String())
	}
}

func TestRecordPurgeDeleteResultBlocked(t *testing.T) {
	var out bytes.Buffer
	o := newWriterOutput(&out, &out, nil)
	counts := purgeDeleteCounts{}
	recordPurgeDeleteResult(o, auditResource{resourceType: "s3", name: "b"}, errors.New("has dependencies and cannot be deleted"), &counts, false)
	if counts.blocked != 1 {
		t.Fatalf(testPurgeCountsErrorf, counts)
	}
	got := out.String()
	if !strings.Contains(got, "re-run with --force") {
		t.Fatalf("expected --force hint in output: %q", got)
	}
}

func TestRecordPurgeDeleteResultBlockedWithForce(t *testing.T) {
	var out bytes.Buffer
	o := newWriterOutput(&out, &out, nil)
	counts := purgeDeleteCounts{}
	recordPurgeDeleteResult(o, auditResource{resourceType: "s3", name: "b"}, errors.New("has dependencies and cannot be deleted"), &counts, true)
	if counts.blocked != 1 {
		t.Fatalf(testPurgeCountsErrorf, counts)
	}
	if strings.Contains(out.String(), "re-run with --force") {
		t.Fatalf("should not show --force hint when force=true: %q", out.String())
	}
}

func TestRecordPurgeDeleteResultManual(t *testing.T) {
	o, _, _ := newPlainOutput(t)
	counts := purgeDeleteCounts{}
	recordPurgeDeleteResult(o, auditResource{resourceType: "s3", name: "b"}, &purgeManualError{cause: errors.New("manual")}, &counts, false)
	if counts.manual != 1 {
		t.Fatalf(testPurgeCountsErrorf, counts)
	}
}
