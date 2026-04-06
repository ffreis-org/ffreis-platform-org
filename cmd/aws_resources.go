package cmd

import (
	"context"
	"fmt"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroups"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sagemaker"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
)

var (
	newBudgetsDeleteClient    = budgets.NewFromConfig
	newCloudWatchLogsClient   = cloudwatchlogs.NewFromConfig
	newDynamoDeleteClient     = dynamodb.NewFromConfig
	newEC2Client              = ec2.NewFromConfig
	newECSClient              = ecs.NewFromConfig
	newEventBridgeClient      = eventbridge.NewFromConfig
	newIAMDeleteClient        = iam.NewFromConfig
	newLambdaDeleteClient     = lambda.NewFromConfig
	newLightsailClient        = lightsail.NewFromConfig
	newOrganizationsClient    = organizations.NewFromConfig
	newResourceGroupsClient   = resourcegroups.NewFromConfig
	newS3DeleteClient         = s3.NewFromConfig
	newSageMakerClient        = sagemaker.NewFromConfig
	newSchedulerDeleteClient  = scheduler.NewFromConfig
	newServiceDiscoveryClient = servicediscovery.NewFromConfig
)

type nativeResourceDeleteFn func(context.Context, auditResource, bool) (bool, error)

var nativeDeleteHandlers = map[string]nativeResourceDeleteFn{
	"ecs/task-definition": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		return true, deleteECSTaskDefinition(ctx, resource.arn)
	},
	"events/rule": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newEventBridgeClient(d.awsCfg)
		if err := deleteEventBridgeRuleTargets(ctx, client, resource.name); err != nil {
			return true, &purgeManualError{cause: err, hint: "remove targets manually before deleting rule"}
		}
		_, err := client.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: sdkaws.String(resource.name), Force: true})
		if err != nil && isTargetsStillPresentError(err) {
			return true, &purgeManualError{cause: err, hint: "disable the owning service (e.g. ECS capacity provider) before deleting this rule"}
		}
		return true, err
	},
	"servicediscovery/namespace": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newServiceDiscoveryClient(d.awsCfg)
		_, err := client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: sdkaws.String(resource.name)})
		return true, err
	},
	"sagemaker/notebook-instance": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newSageMakerClient(d.awsCfg)
		_, err := client.DeleteNotebookInstance(ctx, &sagemaker.DeleteNotebookInstanceInput{NotebookInstanceName: sdkaws.String(resource.name)})
		return true, err
	},
	"lightsail/StaticIp": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newLightsailClient(d.awsCfg)
		_, err := client.ReleaseStaticIp(ctx, &lightsail.ReleaseStaticIpInput{StaticIpName: sdkaws.String(resource.name)})
		return true, err
	},
	"lightsail/KeyPair": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newLightsailClient(d.awsCfg)
		_, err := client.DeleteKeyPair(ctx, &lightsail.DeleteKeyPairInput{KeyPairName: sdkaws.String(resource.name)})
		return true, err
	},
	"lambda/function": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newLambdaDeleteClient(d.awsCfg)
		_, err := client.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: sdkaws.String(resource.name)})
		return true, err
	},
	"iam/role": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		return true, forceDeleteIAMRole(ctx, resource.name)
	},
	"s3": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		return true, forceDeleteS3Bucket(ctx, resource.name)
	},
	"dynamodb/table": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newDynamoDeleteClient(d.awsCfg)
		_, err := client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: sdkaws.String(resource.name)})
		return true, err
	},
	"resource-groups/group": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newResourceGroupsClient(d.awsCfg)
		_, err := client.DeleteGroup(ctx, &resourcegroups.DeleteGroupInput{GroupName: sdkaws.String(resource.name)})
		return true, err
	},
	"budgets/budget": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newBudgetsDeleteClient(d.awsCfg)
		_, err := client.DeleteBudget(ctx, &budgets.DeleteBudgetInput{AccountId: sdkaws.String(d.accountID), BudgetName: sdkaws.String(resource.name)})
		return true, err
	},
	"iam/oidc-provider": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newIAMDeleteClient(d.awsCfg)
		_, err := client.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{OpenIDConnectProviderArn: sdkaws.String(resource.arn)})
		return true, err
	},
	"logs/log-group": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newCloudWatchLogsClient(d.awsCfg)
		_, err := client.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: sdkaws.String(resource.name)})
		return true, err
	},
	"scheduler/schedule-group": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		client := newSchedulerDeleteClient(d.awsCfg)
		_, err := client.DeleteScheduleGroup(ctx, &scheduler.DeleteScheduleGroupInput{Name: sdkaws.String(resource.name)})
		return true, err
	},
	"organizations/policy-attachment": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		return true, detachOrganizationPolicyBySyntheticName(ctx, resource.name)
	},
	"organizations/policy": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		return true, deleteOrganizationPolicyByName(ctx, resource.name)
	},
	"organizations/organizational-unit": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		return true, deleteOrganizationOUByName(ctx, resource.name)
	},
	"organizations/organization": func(ctx context.Context, _ auditResource, _ bool) (bool, error) {
		client := newOrganizationsClient(d.awsCfg)
		_, err := client.DeleteOrganization(ctx, &organizations.DeleteOrganizationInput{})
		return true, err
	},
	"organizations/account": func(ctx context.Context, resource auditResource, _ bool) (bool, error) {
		return true, closeOrganizationAccountByName(ctx, resource.name)
	},
	"ec2/internet-gateway": func(ctx context.Context, resource auditResource, force bool) (bool, error) {
		if !force {
			return false, nil
		}
		return true, forceDeleteInternetGateway(ctx, resource.name)
	},
	"ec2/route-table": func(ctx context.Context, resource auditResource, force bool) (bool, error) {
		if !force {
			return false, nil
		}
		return true, forceDeleteRouteTable(ctx, resource.name)
	},
}

func existsByAPICall(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if isNotFoundError(err) {
		return false, nil
	}
	return false, err
}

func resourceExists(ctx context.Context, resource auditResource) (bool, error) {
	switch resource.resourceType {
	case "ecs/task-definition":
		client := newECSClient(d.awsCfg)
		_, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
			TaskDefinition: sdkaws.String(resource.arn),
		})
		return existsByAPICall(err)
	case "lightsail/StaticIp":
		client := newLightsailClient(d.awsCfg)
		_, err := client.GetStaticIp(ctx, &lightsail.GetStaticIpInput{
			StaticIpName: sdkaws.String(resource.name),
		})
		return existsByAPICall(err)
	case "lightsail/KeyPair":
		client := newLightsailClient(d.awsCfg)
		_, err := client.GetKeyPair(ctx, &lightsail.GetKeyPairInput{
			KeyPairName: sdkaws.String(resource.name),
		})
		return existsByAPICall(err)
	default:
		return true, nil
	}
}

func deleteResourceNatively(ctx context.Context, resource auditResource, force bool) (bool, error) {
	handler, ok := nativeDeleteHandlers[resource.resourceType]
	if !ok {
		return false, nil
	}
	return handler(ctx, resource, force)
}

func deleteAllInlineRolePolicies(ctx context.Context, client *iam.Client, roleName string) error {
	var marker *string
	for {
		out, err := client.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{
			RoleName: sdkaws.String(roleName),
			Marker:   marker,
		})
		if err != nil {
			if isNotFoundError(err) {
				return nil
			}
			return err
		}
		for _, policyName := range out.PolicyNames {
			_, err := client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
				RoleName:   sdkaws.String(roleName),
				PolicyName: sdkaws.String(policyName),
			})
			if err != nil && !isNotFoundError(err) {
				return err
			}
		}
		if !out.IsTruncated {
			break
		}
		marker = out.Marker
	}
	return nil
}

func detachAllManagedRolePolicies(ctx context.Context, client *iam.Client, roleName string) error {
	var marker *string
	for {
		out, err := client.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
			RoleName: sdkaws.String(roleName),
			Marker:   marker,
		})
		if err != nil {
			if isNotFoundError(err) {
				return nil
			}
			return err
		}
		for _, policy := range out.AttachedPolicies {
			_, err := client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
				RoleName:  sdkaws.String(roleName),
				PolicyArn: policy.PolicyArn,
			})
			if err != nil && !isNotFoundError(err) {
				return err
			}
		}
		if !out.IsTruncated {
			break
		}
		marker = out.Marker
	}
	return nil
}

func forceDeleteIAMRole(ctx context.Context, roleName string) error {
	client := newIAMDeleteClient(d.awsCfg)
	if err := deleteAllInlineRolePolicies(ctx, client, roleName); err != nil {
		return err
	}
	if err := detachAllManagedRolePolicies(ctx, client, roleName); err != nil {
		return err
	}
	_, err := client.DeleteRole(ctx, &iam.DeleteRoleInput{
		RoleName: sdkaws.String(roleName),
	})
	if err != nil && isNotFoundError(err) {
		return nil
	}
	return err
}

func forceDeleteS3Bucket(ctx context.Context, bucket string) error {
	client := newS3DeleteClient(d.awsCfg)

	var keyMarker, versionMarker *string
	for {
		out, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:          sdkaws.String(bucket),
			KeyMarker:       keyMarker,
			VersionIdMarker: versionMarker,
		})
		if err != nil {
			if isS3BucketMissing(err) {
				return nil
			}
			return err
		}
		if err := deleteS3ObjectVersions(ctx, client, bucket, out.Versions); err != nil {
			return err
		}
		if err := deleteS3DeleteMarkers(ctx, client, bucket, out.DeleteMarkers); err != nil {
			return err
		}
		if !sdkaws.ToBool(out.IsTruncated) {
			break
		}
		keyMarker = out.NextKeyMarker
		versionMarker = out.NextVersionIdMarker
	}

	_, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: sdkaws.String(bucket)})
	if err != nil && isS3BucketMissing(err) {
		return nil
	}
	return err
}

func deleteS3ObjectVersions(ctx context.Context, client *s3.Client, bucket string, versions []s3types.ObjectVersion) error {
	for _, version := range versions {
		_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket:    sdkaws.String(bucket),
			Key:       version.Key,
			VersionId: version.VersionId,
		})
		if err != nil && !isNotFoundError(err) {
			return err
		}
	}
	return nil
}

func deleteS3DeleteMarkers(ctx context.Context, client *s3.Client, bucket string, markers []s3types.DeleteMarkerEntry) error {
	for _, marker := range markers {
		_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket:    sdkaws.String(bucket),
			Key:       marker.Key,
			VersionId: marker.VersionId,
		})
		if err != nil && !isNotFoundError(err) {
			return err
		}
	}
	return nil
}

func deleteEventBridgeRuleTargets(ctx context.Context, client *eventbridge.Client, ruleName string) error {
	targetIDs, err := listEventBridgeTargetIDs(ctx, client, ruleName)
	if err != nil {
		return err
	}
	for start := 0; start < len(targetIDs); start += 10 {
		end := start + 10
		if end > len(targetIDs) {
			end = len(targetIDs)
		}
		if err := removeEventBridgeTargetBatch(ctx, client, ruleName, targetIDs[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func listEventBridgeTargetIDs(ctx context.Context, client *eventbridge.Client, ruleName string) ([]string, error) {
	var nextToken *string
	var targetIDs []string
	for {
		out, err := client.ListTargetsByRule(ctx, &eventbridge.ListTargetsByRuleInput{
			Rule:      sdkaws.String(ruleName),
			NextToken: nextToken,
		})
		if err != nil {
			return nil, err
		}
		for _, target := range out.Targets {
			if target.Id != nil && sdkaws.ToString(target.Id) != "" {
				targetIDs = append(targetIDs, sdkaws.ToString(target.Id))
			}
		}
		if out.NextToken == nil || sdkaws.ToString(out.NextToken) == "" {
			return targetIDs, nil
		}
		nextToken = out.NextToken
	}
}

func removeEventBridgeTargetBatch(ctx context.Context, client *eventbridge.Client, ruleName string, targetIDs []string) error {
	out, err := client.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{
		Rule:  sdkaws.String(ruleName),
		Ids:   targetIDs,
		Force: true,
	})
	if err != nil {
		return err
	}
	if out.FailedEntryCount == 0 {
		return nil
	}
	failed := make([]string, 0, len(out.FailedEntries))
	for _, entry := range out.FailedEntries {
		if entry.TargetId != nil && sdkaws.ToString(entry.TargetId) != "" {
			failed = append(failed, sdkaws.ToString(entry.TargetId))
		}
	}
	if len(failed) == 0 {
		return fmt.Errorf("failed to remove %d EventBridge target(s) from rule %s", out.FailedEntryCount, ruleName)
	}
	return fmt.Errorf("failed to remove EventBridge targets from rule %s: %s", ruleName, strings.Join(failed, ", "))
}

func forceDeleteInternetGateway(ctx context.Context, gatewayID string) error {
	client := newEC2Client(d.awsCfg)
	out, err := client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		InternetGatewayIds: []string{gatewayID},
	})
	if err != nil {
		return err
	}
	if len(out.InternetGateways) == 0 {
		return fmt.Errorf("internet gateway %s was not found", gatewayID)
	}

	for _, attachment := range out.InternetGateways[0].Attachments {
		if attachment.VpcId == nil || sdkaws.ToString(attachment.VpcId) == "" {
			continue
		}
		if _, err := client.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
			InternetGatewayId: sdkaws.String(gatewayID),
			VpcId:             attachment.VpcId,
		}); err != nil && !isNotFoundError(err) {
			return err
		}
	}

	_, err = client.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{
		InternetGatewayId: sdkaws.String(gatewayID),
	})
	return err
}

func forceDeleteRouteTable(ctx context.Context, routeTableID string) error {
	client := newEC2Client(d.awsCfg)
	out, err := client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		RouteTableIds: []string{routeTableID},
	})
	if err != nil {
		return err
	}
	if len(out.RouteTables) == 0 {
		return fmt.Errorf("route table %s was not found", routeTableID)
	}

	for _, association := range out.RouteTables[0].Associations {
		if association.Main != nil && *association.Main {
			continue
		}
		if association.RouteTableAssociationId == nil || sdkaws.ToString(association.RouteTableAssociationId) == "" {
			continue
		}
		if _, err := client.DisassociateRouteTable(ctx, &ec2.DisassociateRouteTableInput{
			AssociationId: association.RouteTableAssociationId,
		}); err != nil && !isNotFoundError(err) {
			return err
		}
	}

	_, err = client.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{
		RouteTableId: sdkaws.String(routeTableID),
	})
	return err
}

func detachOrganizationPolicyBySyntheticName(ctx context.Context, synthetic string) error {
	policyName, targetName, ok := strings.Cut(synthetic, "@")
	if !ok {
		return fmt.Errorf("invalid organization policy attachment identifier %q", synthetic)
	}
	client := newOrganizationsClient(d.awsCfg)
	policy, err := findOrganizationPolicyByName(ctx, client, policyName)
	if err != nil {
		return err
	}
	if policy == nil || policy.Id == nil {
		return nil
	}
	targetID, err := findOrganizationTargetIDByName(ctx, client, targetName)
	if err != nil {
		return err
	}
	if targetID == "" {
		return nil
	}
	_, err = client.DetachPolicy(ctx, &organizations.DetachPolicyInput{
		PolicyId: policy.Id,
		TargetId: sdkaws.String(targetID),
	})
	if err != nil && isNotFoundError(err) {
		return nil
	}
	return err
}

func deleteOrganizationPolicyByName(ctx context.Context, name string) error {
	client := newOrganizationsClient(d.awsCfg)
	policy, err := findOrganizationPolicyByName(ctx, client, name)
	if err != nil {
		return err
	}
	if policy == nil || policy.Id == nil {
		return nil
	}
	_, err = client.DeletePolicy(ctx, &organizations.DeletePolicyInput{
		PolicyId: policy.Id,
	})
	if err != nil && isNotFoundError(err) {
		return nil
	}
	return err
}

func deleteOrganizationOUByName(ctx context.Context, name string) error {
	client := newOrganizationsClient(d.awsCfg)
	ouID, err := findOrganizationalUnitIDByName(ctx, client, name)
	if err != nil {
		return err
	}
	if ouID == "" {
		return nil
	}
	_, err = client.DeleteOrganizationalUnit(ctx, &organizations.DeleteOrganizationalUnitInput{
		OrganizationalUnitId: sdkaws.String(ouID),
	})
	if err != nil && isNotFoundError(err) {
		return nil
	}
	return err
}

func closeOrganizationAccountByName(ctx context.Context, name string) error {
	client := newOrganizationsClient(d.awsCfg)
	accountID, err := findOrganizationAccountIDByName(ctx, client, name)
	if err != nil {
		return err
	}
	if accountID == "" {
		return nil
	}
	_, err = client.CloseAccount(ctx, &organizations.CloseAccountInput{
		AccountId: sdkaws.String(accountID),
	})
	if err != nil && isNotFoundError(err) {
		return nil
	}
	return err
}

func findOrganizationalUnitIDByName(ctx context.Context, client *organizations.Client, name string) (string, error) {
	rootID, err := organizationRootID(ctx, client)
	if err != nil {
		return "", err
	}
	var nextToken *string
	for {
		out, err := client.ListOrganizationalUnitsForParent(ctx, &organizations.ListOrganizationalUnitsForParentInput{
			ParentId:  sdkaws.String(rootID),
			NextToken: nextToken,
		})
		if err != nil {
			return "", err
		}
		for _, ou := range out.OrganizationalUnits {
			if sdkaws.ToString(ou.Name) == name {
				return sdkaws.ToString(ou.Id), nil
			}
		}
		if sdkaws.ToString(out.NextToken) == "" {
			return "", nil
		}
		nextToken = out.NextToken
	}
}

func findOrganizationAccountIDByName(ctx context.Context, client *organizations.Client, name string) (string, error) {
	var nextToken *string
	for {
		out, err := client.ListAccounts(ctx, &organizations.ListAccountsInput{NextToken: nextToken})
		if err != nil {
			return "", err
		}
		for _, account := range out.Accounts {
			if sdkaws.ToString(account.Name) == name {
				return sdkaws.ToString(account.Id), nil
			}
		}
		if sdkaws.ToString(out.NextToken) == "" {
			return "", nil
		}
		nextToken = out.NextToken
	}
}

func findOrganizationTargetIDByName(ctx context.Context, client *organizations.Client, name string) (string, error) {
	if name == "environments" {
		return findOrganizationalUnitIDByName(ctx, client, name)
	}
	return findOrganizationAccountIDByName(ctx, client, name)
}

// deleteECSTaskDefinition deregisters (if still ACTIVE) then fully deletes an
// ECS task definition by ARN. Cloud Control only deregisters, leaving the
// resource in INACTIVE state where it remains visible to the Tagging API.
func deleteECSTaskDefinition(ctx context.Context, arn string) error {
	client := newECSClient(d.awsCfg)

	desc, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: sdkaws.String(arn),
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil // already gone
		}
		return err
	}

	// Deregister first if still ACTIVE — DeleteTaskDefinitions requires INACTIVE.
	if desc.TaskDefinition != nil && string(desc.TaskDefinition.Status) == "ACTIVE" {
		if _, err := client.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{
			TaskDefinition: sdkaws.String(arn),
		}); err != nil && !isNotFoundError(err) {
			return err
		}
	}

	out, err := client.DeleteTaskDefinitions(ctx, &ecs.DeleteTaskDefinitionsInput{
		TaskDefinitions: []string{arn},
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil
		}
		return err
	}
	if len(out.Failures) > 0 {
		msgs := make([]string, 0, len(out.Failures))
		for _, f := range out.Failures {
			msgs = append(msgs, sdkaws.ToString(f.Reason))
		}
		return fmt.Errorf("DeleteTaskDefinitions failures: %s", strings.Join(msgs, "; "))
	}
	return nil
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "cannot be found")
}

func isTargetsStillPresentError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "has targets") ||
		(strings.Contains(msg, "rule can") && strings.Contains(msg, "deleted since it has"))
}
