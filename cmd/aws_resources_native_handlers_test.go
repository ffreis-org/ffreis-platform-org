package cmd

import (
	"context"
	"testing"
)

func TestNativeDeleteHandlersAllEntriesAreExecutable(t *testing.T) {
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

func TestNativeDeleteHandlersEC2ForceGateBothTypes(t *testing.T) {
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
