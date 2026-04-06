package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
)

func TestNativeDeleteHandlersAllEntriesAreExecutable(t *testing.T) {
	t.Parallel()

	oldD := d
	t.Cleanup(func() { d = oldD })
	d.awsCfg = sdkaws.Config{
		Region:      testRegion,
		Credentials: credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
	}
	d.accountID = testAccountID

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []auditResource{
		{resourceType: "ecs/task-definition", arn: "arn:aws:ecs:us-east-1:123:task-definition/my:1"},
		{resourceType: "servicediscovery/namespace", name: "ns-123"},
		{resourceType: "sagemaker/notebook-instance", name: "nb-123"},
		{resourceType: "lightsail/StaticIp", name: "ip-123"},
		{resourceType: "lightsail/KeyPair", name: "kp-123"},
		{resourceType: "lambda/function", name: "fn-123"},
		{resourceType: "iam/role", name: "role-123"},
		{resourceType: "s3", name: "bucket-123"},
		{resourceType: "dynamodb/table", name: "table-123"},
		{resourceType: "resource-groups/group", name: "rg-123"},
		{resourceType: "budgets/budget", name: "budget-123"},
		{resourceType: "iam/oidc-provider", arn: "arn:aws:iam::123:oidc-provider/token.actions.githubusercontent.com"},
		{resourceType: "logs/log-group", name: "/aws/lambda/fn-123"},
		{resourceType: "scheduler/schedule-group", name: "group-123"},
		{resourceType: "organizations/policy-attachment", name: "policy@target"},
		{resourceType: "organizations/policy", name: "policy-name"},
		{resourceType: "organizations/organizational-unit", name: "environments"},
		{resourceType: resourceTypeOrganizationsOrganization},
		{resourceType: "organizations/account", name: "my-account"},
		{resourceType: "ec2/internet-gateway", name: "igw-123"},
		{resourceType: "ec2/route-table", name: "rtb-123"},
	}

	for _, resource := range cases {
		resource := resource
		t.Run(resource.resourceType, func(t *testing.T) {
			handled, _ := deleteResourceNatively(ctx, resource, true)
			if !handled {
				t.Fatalf("expected handler for %q to be selected", resource.resourceType)
			}
		})
	}
}

func TestNativeDeleteHandlerEventsRuleWrapsRemoveTargetsErrorAsManual(t *testing.T) {
	t.Parallel()

	old := newEventBridgeClient
	t.Cleanup(func() { newEventBridgeClient = old })
	newEventBridgeClient = func(_ sdkaws.Config, _ ...func(*eventbridge.Options)) *eventbridge.Client {
		return testEventBridgeClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"__type":"InternalException","message":"boom"}`)
		})
	}

	handled, err := deleteResourceNatively(context.Background(), auditResource{resourceType: "events/rule", name: "rule-1"}, true)
	if !handled {
		t.Fatal("expected events/rule to be handled natively")
	}
	var manual *purgeManualError
	if !errors.As(err, &manual) {
		t.Fatalf("expected purgeManualError, got %v", err)
	}
	if !strings.Contains(manual.Error(), "remove targets manually before deleting rule") {
		t.Fatalf("unexpected hint: %v", manual)
	}
}

func TestNativeDeleteHandlerEventsRuleWrapsTargetsStillPresentAsManual(t *testing.T) {
	t.Parallel()

	old := newEventBridgeClient
	t.Cleanup(func() { newEventBridgeClient = old })
	newEventBridgeClient = func(_ sdkaws.Config, _ ...func(*eventbridge.Options)) *eventbridge.Client {
		return testEventBridgeClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
			target := r.Header.Get("X-Amz-Target")
			switch {
			case strings.Contains(target, "ListTargetsByRule"):
				_, _ = io.WriteString(w, `{"Targets":[]}`)
			case strings.Contains(target, "DeleteRule"):
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"__type":"ValidationException","message":"rule can't be deleted since it has targets"}`)
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		})
	}

	handled, err := deleteResourceNatively(context.Background(), auditResource{resourceType: "events/rule", name: "rule-1"}, true)
	if !handled {
		t.Fatal("expected events/rule to be handled natively")
	}
	var manual *purgeManualError
	if !errors.As(err, &manual) {
		t.Fatalf("expected purgeManualError, got %v", err)
	}
	if !strings.Contains(manual.Error(), "disable the owning service") {
		t.Fatalf("unexpected hint: %v", manual)
	}
}

func TestNativeDeleteHandlersEC2ForceGateBothTypes(t *testing.T) {
	t.Parallel()

	for _, resourceType := range []string{"ec2/internet-gateway", "ec2/route-table"} {
		resourceType := resourceType
		t.Run(resourceType, func(t *testing.T) {
			handled, err := deleteResourceNatively(context.Background(), auditResource{resourceType: resourceType, name: "id-123"}, false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if handled {
				t.Fatalf("expected %q to skip when force=false", resourceType)
			}
		})
	}
}
