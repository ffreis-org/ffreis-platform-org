package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
)

const (
	testPurgeParameterARN           = "arn:aws:ssm:us-east-1:123456789012:parameter/my-param"
	testPurgeRequestToken           = "token-1"
	testPurgeRunEErrorf             = "purgeCmd.RunE() error = %v"
	testPurgeRunEUnexpectedErrorf   = "purgeCmd.RunE() unexpected error: %v"
	testPurgeUnexpectedOutputErrorf = "unexpected output: %q"
	testPurgeParameterResourceType  = "ssm/parameter"
	testPurgeParameterName          = "/my-param"
)

type mockCloudControlAPI struct {
	deleteFn func(context.Context, *cloudcontrol.DeleteResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error)
	statusFn func(context.Context, *cloudcontrol.GetResourceRequestStatusInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error)
}

func (m *mockCloudControlAPI) DeleteResource(ctx context.Context, input *cloudcontrol.DeleteResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error) {
	if m.deleteFn == nil {
		return nil, errors.New("unexpected DeleteResource call")
	}
	return m.deleteFn(ctx, input, optFns...)
}

func (m *mockCloudControlAPI) GetResourceRequestStatus(ctx context.Context, input *cloudcontrol.GetResourceRequestStatusInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error) {
	if m.statusFn == nil {
		return nil, errors.New("unexpected GetResourceRequestStatus call")
	}
	return m.statusFn(ctx, input, optFns...)
}

func setImmediatePurgeAfter(t *testing.T) {
	t.Helper()
	old := purgeAfter
	purgeAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	t.Cleanup(func() { purgeAfter = old })
}

func setupPurgeCommandTest(t *testing.T, stdin string, resources []auditResource, scanErr error, cc cloudControlAPI) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	oldStdout := purgeStdout
	oldScan := scanResourcesFn
	oldClient := newCloudControlClient
	oldForce := purgeForce
	purgeStdout = &out
	purgeForce = true
	scanResourcesFn = func(context.Context) ([]auditResource, error) {
		if scanErr != nil {
			return nil, scanErr
		}
		return resources, nil
	}
	newCloudControlClient = func(sdkaws.Config) cloudControlAPI {
		if cc == nil {
			return &mockCloudControlAPI{}
		}
		return cc
	}
	d.ui = nil
	d.env = testEnv
	d.accountID = testAccountID
	d.region = testRegion
	d.awsCfg = sdkaws.Config{}
	setStdinText(t, stdin)
	t.Cleanup(func() {
		purgeStdout = oldStdout
		scanResourcesFn = oldScan
		newCloudControlClient = oldClient
		purgeForce = oldForce
	})
	return &out
}

func TestWaitForDeleteRetriesThenSucceeds(t *testing.T) {
	setImmediatePurgeAfter(t)
	statusCalls := 0
	cc := &mockCloudControlAPI{
		statusFn: func(context.Context, *cloudcontrol.GetResourceRequestStatusInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error) {
			statusCalls++
			switch statusCalls {
			case 1:
				return nil, errors.New("ThrottlingException: Rate exceeded")
			case 2:
				return &cloudcontrol.GetResourceRequestStatusOutput{ProgressEvent: &cctypes.ProgressEvent{OperationStatus: cctypes.OperationStatusInProgress}}, nil
			default:
				return &cloudcontrol.GetResourceRequestStatusOutput{ProgressEvent: &cctypes.ProgressEvent{OperationStatus: cctypes.OperationStatusSuccess}}, nil
			}
		},
	}

	if err := waitForDelete(context.Background(), cc, testPurgeRequestToken); err != nil {
		t.Fatalf("waitForDelete() unexpected error: %v", err)
	}
	if statusCalls != 3 {
		t.Fatalf("expected 3 status calls, got %d", statusCalls)
	}
}

func TestWaitForDeleteFailureStatus(t *testing.T) {
	cc := &mockCloudControlAPI{
		statusFn: func(context.Context, *cloudcontrol.GetResourceRequestStatusInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error) {
			return &cloudcontrol.GetResourceRequestStatusOutput{
				ProgressEvent: &cctypes.ProgressEvent{
					OperationStatus: cctypes.OperationStatusFailed,
					StatusMessage:   sdkaws.String("dependency violation"),
				},
			}, nil
		},
	}

	err := waitForDelete(context.Background(), cc, testPurgeRequestToken)
	if err == nil || !strings.Contains(err.Error(), "delete failed: dependency violation") {
		t.Fatalf("waitForDelete() error = %v", err)
	}
}

func TestDeleteResourceWithRetryRetriesThenSucceeds(t *testing.T) {
	setImmediatePurgeAfter(t)
	deleteCalls := 0
	cc := &mockCloudControlAPI{
		deleteFn: func(context.Context, *cloudcontrol.DeleteResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error) {
			deleteCalls++
			if deleteCalls < 3 {
				return nil, errors.New("Too Many Requests")
			}
			return &cloudcontrol.DeleteResourceOutput{ProgressEvent: &cctypes.ProgressEvent{RequestToken: sdkaws.String(testPurgeRequestToken)}}, nil
		},
	}

	resp, err := deleteResourceWithRetry(context.Background(), cc, &cloudcontrol.DeleteResourceInput{})
	if err != nil {
		t.Fatalf("deleteResourceWithRetry() unexpected error: %v", err)
	}
	if sdkaws.ToString(resp.ProgressEvent.RequestToken) != testPurgeRequestToken {
		t.Fatalf("unexpected request token: %#v", resp.ProgressEvent)
	}
	if deleteCalls != 3 {
		t.Fatalf("expected 3 delete calls, got %d", deleteCalls)
	}
}

func TestDeleteResourceWithRetryReturnsFatalError(t *testing.T) {
	cc := &mockCloudControlAPI{
		deleteFn: func(context.Context, *cloudcontrol.DeleteResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error) {
			return nil, errors.New("access denied")
		},
	}

	_, err := deleteResourceWithRetry(context.Background(), cc, &cloudcontrol.DeleteResourceInput{})
	if err == nil || err.Error() != "access denied" {
		t.Fatalf("deleteResourceWithRetry() error = %v", err)
	}
}

func TestPurgeCommandScanError(t *testing.T) {
	setupPurgeCommandTest(t, "", nil, errors.New("scan failed"), nil)
	purgeCmd.SetContext(context.Background())

	err := purgeCmd.RunE(purgeCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "scanning resources: scan failed") {
		t.Fatalf(testPurgeRunEErrorf, err)
	}
}

func TestPurgeCommandNoUnownedResources(t *testing.T) {
	out := setupPurgeCommandTest(t, "", []auditResource{{status: "OK", resourceType: "s3", name: "bucket"}}, nil, nil)
	purgeCmd.SetContext(context.Background())

	if err := purgeCmd.RunE(purgeCmd, nil); err != nil {
		t.Fatalf(testPurgeRunEUnexpectedErrorf, err)
	}
	if !strings.Contains(out.String(), "no unowned resources found") {
		t.Fatalf(testPurgeUnexpectedOutputErrorf, out.String())
	}
}

func TestPurgeCommandUnsupportedResourcesOnly(t *testing.T) {
	resources := []auditResource{{status: "UNOWNED", resourceType: "unknown/type", name: "mystery"}}
	out := setupPurgeCommandTest(t, "", resources, nil, nil)
	purgeCmd.SetContext(context.Background())

	if err := purgeCmd.RunE(purgeCmd, nil); err != nil {
		t.Fatalf(testPurgeRunEUnexpectedErrorf, err)
	}
	got := out.String()
	if !strings.Contains(got, "some resource types are unsupported") || !strings.Contains(got, "no supported resource types to delete automatically") {
		t.Fatalf(testPurgeUnexpectedOutputErrorf, got)
	}
}

func TestPurgeCommandCancelledByConfirmationMismatch(t *testing.T) {
	resources := []auditResource{{status: "UNOWNED", resourceType: testPurgeParameterResourceType, name: testPurgeParameterName, arn: testPurgeParameterARN}}
	out := setupPurgeCommandTest(t, "nope\n", resources, nil, nil)
	purgeCmd.SetContext(context.Background())

	if err := purgeCmd.RunE(purgeCmd, nil); err != nil {
		t.Fatalf(testPurgeRunEUnexpectedErrorf, err)
	}
	if !strings.Contains(out.String(), "Cancelled.") {
		t.Fatalf(testPurgeUnexpectedOutputErrorf, out.String())
	}
}

func TestPurgeCommandNoInputReceived(t *testing.T) {
	resources := []auditResource{{status: "UNOWNED", resourceType: testPurgeParameterResourceType, name: testPurgeParameterName, arn: testPurgeParameterARN}}
	setupPurgeCommandTest(t, "", resources, nil, nil)
	purgeCmd.SetContext(context.Background())

	err := purgeCmd.RunE(purgeCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "no input received") {
		t.Fatalf(testPurgeRunEErrorf, err)
	}
}

func TestPurgeCommandDeletesSupportedResource(t *testing.T) {
	setImmediatePurgeAfter(t)
	deleteCalls := 0
	statusCalls := 0
	cc := &mockCloudControlAPI{
		deleteFn: func(_ context.Context, input *cloudcontrol.DeleteResourceInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error) {
			deleteCalls++
			if got := sdkaws.ToString(input.TypeName); got != "AWS::SSM::Parameter" {
				t.Fatalf("TypeName = %q", got)
			}
			if got := sdkaws.ToString(input.Identifier); got != testPurgeParameterName {
				t.Fatalf("Identifier = %q", got)
			}
			if !strings.HasPrefix(sdkaws.ToString(input.ClientToken), "platform-org-purge-") {
				t.Fatalf("ClientToken = %q", sdkaws.ToString(input.ClientToken))
			}
			return &cloudcontrol.DeleteResourceOutput{ProgressEvent: &cctypes.ProgressEvent{RequestToken: sdkaws.String(testPurgeRequestToken)}}, nil
		},
		statusFn: func(context.Context, *cloudcontrol.GetResourceRequestStatusInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error) {
			statusCalls++
			if statusCalls == 1 {
				return &cloudcontrol.GetResourceRequestStatusOutput{ProgressEvent: &cctypes.ProgressEvent{OperationStatus: cctypes.OperationStatusInProgress}}, nil
			}
			return &cloudcontrol.GetResourceRequestStatusOutput{ProgressEvent: &cctypes.ProgressEvent{OperationStatus: cctypes.OperationStatusSuccess}}, nil
		},
	}
	resources := []auditResource{{status: "UNOWNED", resourceType: testPurgeParameterResourceType, name: testPurgeParameterName, arn: testPurgeParameterARN}}
	out := setupPurgeCommandTest(t, "purge\n", resources, nil, cc)
	purgeCmd.SetContext(context.Background())

	if err := purgeCmd.RunE(purgeCmd, nil); err != nil {
		t.Fatalf(testPurgeRunEUnexpectedErrorf, err)
	}
	if deleteCalls != 1 || statusCalls != 2 {
		t.Fatalf("deleteCalls=%d statusCalls=%d", deleteCalls, statusCalls)
	}
	got := out.String()
	if !strings.Contains(got, "will delete 1 resource(s) via Cloud Control API") || !strings.Contains(got, "deleted "+testPurgeParameterResourceType+" "+testPurgeParameterName) {
		t.Fatalf(testPurgeUnexpectedOutputErrorf, got)
	}
}

func TestPurgeCommandReturnsDeletionFailures(t *testing.T) {
	cc := &mockCloudControlAPI{
		deleteFn: func(context.Context, *cloudcontrol.DeleteResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error) {
			return nil, errors.New("AccessDenied")
		},
	}
	resources := []auditResource{{status: "UNOWNED", resourceType: testPurgeParameterResourceType, name: testPurgeParameterName, arn: testPurgeParameterARN}}
	out := setupPurgeCommandTest(t, "purge\n", resources, nil, cc)
	purgeCmd.SetContext(context.Background())

	err := purgeCmd.RunE(purgeCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "purge completed with 1 deletion failure") {
		t.Fatalf(testPurgeRunEErrorf, err)
	}
	if !strings.Contains(out.String(), "delete "+testPurgeParameterResourceType+" "+testPurgeParameterName+": AccessDenied") {
		t.Fatalf(testPurgeUnexpectedOutputErrorf, out.String())
	}
}
