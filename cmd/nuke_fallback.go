package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type nukeBackendStateConfig struct {
	BucketName string
	TableName  string
	StateKey   string
}

type nukeFallbackSummary struct {
	Deleted int
	Gone    int
	Blocked int
	Manual  int
	Failed  int
}

type nukeBackendResetSummary struct {
	BucketName            string
	TableName             string
	StateKey              string
	DeletedStateVersions  int
	DeletedDeleteMarkers  int
	DeletedLockEntries    int
	RemovedLocalTerraform bool
	BackupDir             string
}

type nukeBackendResetS3API interface {
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type nukeBackendResetDynamoAPI interface {
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

const (
	resourceTypeOrganizationsPolicyAttachment = "organizations/policy-attachment"
	orgPolicyDenyLeaveOrganizationName        = "deny-leave-organization"
	resourceTypeOrganizationsPolicy           = "organizations/policy"
	resourceTypeIAMRole                       = "iam/role"
	resourceTypeIAMOIDCProvider               = "iam/oidc-provider"
	stackNamePlatformOrg                      = "platform-org"
	policyNameDenyIAMUserCreation             = "deny-iam-user-creation"
	policyNameDenyDisableCloudTrail           = "deny-disable-cloudtrail"
	githubActionsOIDCURL                      = "token.actions.githubusercontent.com"
	fmtResourceNameErr                        = "%s %s: %v"
)

var (
	ensureInitForNukeFn                   = ensureInit
	runTerraformForNukeFn                 = runTerraform
	scanManagedPlatformOrgResourcesNukeFn = scanManagedPlatformOrgResourcesForNuke
	platformOrgCleanupTargetsForNukeFn    = platformOrgCleanupTargetsForNuke
	runManagedSDKFallbackNukeFn           = runManagedSDKFallbackNuke
	resetBackendStateForNukeFn            = resetBackendStateForNuke
	loadBackendStateConfigForNukeFn       = loadBackendStateConfigForNuke
	newNukeBackendResetS3ClientFn         = func(cfg sdkaws.Config) nukeBackendResetS3API { return s3.NewFromConfig(cfg) }
	newNukeBackendResetDynamoClientFn     = func(cfg sdkaws.Config) nukeBackendResetDynamoAPI { return dynamodb.NewFromConfig(cfg) }
	checkStateBackendExistsFn             = checkStateBackendExists
	explicitPlatformOrgCleanupTargetsFn   = explicitPlatformOrgCleanupTargets
)

// checkStateBackendExists checks whether the bootstrap-managed Terraform state S3 bucket exists.
// Returns (false, nil) when the bucket is absent — that is not an error, just a missing backend.
func checkStateBackendExists(ctx context.Context, cfg sdkaws.Config, org string) (bool, error) {
	client := newNukeBackendResetS3ClientFn(cfg)
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: sdkaws.String(org + "-tf-state-root"),
	})
	if err == nil {
		return true, nil
	}
	var notFound *s3types.NoSuchBucket
	if errors.As(err, &notFound) {
		return false, nil
	}
	return false, err
}

func scanManagedPlatformOrgResourcesForNuke(ctx context.Context) ([]auditResource, error) {
	discovered, err := scanResourcesFn(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]auditResource, 0, len(discovered))
	seen := make(map[string]bool, len(discovered))
	for _, resource := range discovered {
		if resource.stack != stackNamePlatformOrg || resource.status == "UNOWNED" {
			continue
		}
		key := matchedDiscoveredResourceKey(resource)
		if seen[key] {
			continue
		}
		seen[key] = true
		filtered = append(filtered, resource)
	}
	sortOtherManagedResources(filtered)
	return filtered, nil
}

func recordFallbackDeleteResult(out *commandOutput, resource auditResource, err error, summary *nukeFallbackSummary, errs []string) []string {
	switch classifyPurgeDeleteError(err) {
	case purgeFailureGone:
		summary.Gone++
		out.Status("muted", "skip", fmt.Sprintf("%s %s already absent", resource.resourceType, resource.name))
	case purgeFailureManual:
		summary.Manual++
		errs = append(errs, fmt.Sprintf(fmtResourceNameErr, resource.resourceType, resource.name, err))
		out.Status("warn", "skip", fmt.Sprintf("%s %s requires manual cleanup", resource.resourceType, resource.name))
	case purgeFailureBlocked:
		summary.Blocked++
		errs = append(errs, fmt.Sprintf(fmtResourceNameErr, resource.resourceType, resource.name, err))
		out.Status("warn", "wait", fmt.Sprintf("%s %s is blocked by dependent resources", resource.resourceType, resource.name))
	case purgeFailureFatal, purgeFailureRetryable:
		if err != nil {
			summary.Failed++
			errs = append(errs, fmt.Sprintf(fmtResourceNameErr, resource.resourceType, resource.name, err))
			out.Status("error", "fail", fmt.Sprintf("delete %s %s: %v", resource.resourceType, resource.name, err))
		} else {
			summary.Deleted++
			out.Status("ok", "ok", fmt.Sprintf("deleted %s %s", resource.resourceType, resource.name))
		}
	}
	return errs
}

func runManagedSDKFallbackNuke(ctx context.Context, out *commandOutput, resources []auditResource, force bool) (nukeFallbackSummary, error) {
	summary := nukeFallbackSummary{}
	cc := newCloudControlClient(d.awsCfg)
	var errs []string

	for _, resource := range resources {
		out.Status("info", "cleanup", fmt.Sprintf("deleting %s %s", resource.resourceType, resource.name))
		err := deleteManagedResourceWithFallback(ctx, cc, resource, force)
		errs = recordFallbackDeleteResult(out, resource, err, &summary, errs)
	}

	if len(errs) > 0 {
		return summary, fmt.Errorf("AWS fallback cleanup incomplete (%d blocked, %d manual, %d failed)", summary.Blocked, summary.Manual, summary.Failed)
	}
	return summary, nil
}

func platformOrgCleanupTargetsForNuke(ctx context.Context) ([]auditResource, error) {
	targets := make(map[string]auditResource)

	discovered, err := scanManagedPlatformOrgResourcesNukeFn(ctx)
	if err != nil {
		// Tagging API may fail when credentials are invalid (e.g. bootstrap IAM role was deleted).
		// Fall back to explicit org API checks only — they cover the critical resources.
		d.log.Warn("resource tag scan failed during nuke verification, using explicit checks only", "err", err)
		discovered = nil
	}
	// typeNameIndex maps the name-based key (stack|type|name) to the primary ARN key so that
	// explicit checks (which have no ARN) can find their already-stored tag-scan counterpart.
	typeNameIndex := make(map[string]string, len(discovered))
	for _, resource := range discovered {
		primaryKey := matchedDiscoveredResourceKey(resource)
		targets[primaryKey] = resource
		if resource.arn != "" {
			nameKey := strings.ToLower(resource.stack + "|" + resource.resourceType + "|" + resource.name)
			typeNameIndex[nameKey] = primaryKey
		}
	}

	explicit, err := explicitPlatformOrgCleanupTargetsFn(ctx)
	if err != nil {
		return nil, err
	}
	for _, resource := range explicit {
		key := matchedDiscoveredResourceKey(resource)
		// For explicit entries (no ARN), also check if the same resource was already stored
		// under its ARN key from the tag scan.
		if arnKey, found := typeNameIndex[key]; found {
			key = arnKey
		}
		if existing, ok := targets[key]; ok {
			if existing.arn == "" && resource.arn != "" {
				existing.arn = resource.arn
			}
			targets[key] = existing
			continue
		}
		targets[key] = resource
	}

	out := make([]auditResource, 0, len(targets))
	for _, resource := range targets {
		out = append(out, resource)
	}
	sort.Slice(out, func(i, j int) bool { return nukeCleanupTargetLess(out[i], out[j]) })
	return out, nil
}

func deleteManagedResourceWithFallback(ctx context.Context, cc cloudControlAPI, resource auditResource, force bool) error {
	if handled, err := deleteResourceNatively(ctx, resource, force); handled {
		return err
	}

	service, _ := parseServiceType(resource.resourceType)
	cfnType, identifier := arnToCloudControl(resource.arn, service, resource.resourceType, resource.name)
	if cfnType == "" || identifier == "" {
		return &purgeManualError{
			cause: fmt.Errorf("no delete strategy for %s", resource.resourceType),
			hint:  "add a native delete strategy for this resource type",
		}
	}

	resp, err := deleteResourceWithRetry(ctx, cc, &cloudcontrol.DeleteResourceInput{
		TypeName:    sdkaws.String(cfnType),
		Identifier:  sdkaws.String(identifier),
		ClientToken: sdkaws.String(purgeClientToken(cfnType, identifier)),
	})
	if err != nil {
		return err
	}
	return waitForDelete(ctx, cc, sdkaws.ToString(resp.ProgressEvent.RequestToken))
}

func fallbackNukeAfterTerraformFailure(ctx context.Context, out *commandOutput, root, stack, backupDir string, cause error) error {
	out.Status("warn", "fallback", fmt.Sprintf("terraform cleanup could not complete cleanly; falling back to AWS SDK cleanup (%v)", cause))

	resources, err := platformOrgCleanupTargetsForNukeFn(ctx)
	if err != nil {
		return fmt.Errorf("scan managed platform-org resources for fallback: %w", err)
	}
	if len(resources) == 0 {
		out.Status("muted", "cleanup", "no platform-org resources remain; resetting backend state")
	} else {
		summary, err := runManagedSDKFallbackNukeFn(ctx, out, resources, true)
		out.Summary("AWS Fallback Cleanup",
			countPart("deleted", summary.Deleted),
			countPart("gone", summary.Gone),
			countPart("blocked", summary.Blocked),
			countPart("manual", summary.Manual),
			countPart("failed", summary.Failed),
		)
		if err != nil {
			return err
		}
	}

	remaining, err := platformOrgCleanupTargetsForNukeFn(ctx)
	if err != nil {
		// Scan failure (e.g. credentials gone after IAM role deletion) is not fatal here:
		// we've already done our best to clean up. Warn and continue.
		out.Status("warn", "verify", fmt.Sprintf("post-cleanup verification scan failed: %v", err))
		out.Status("info", "note", "run 'platform-org audit' to confirm cleanup once credentials are valid")
	} else if len(remaining) > 0 {
		names := make([]string, 0, len(remaining))
		for _, resource := range remaining {
			names = append(names, resource.resourceType+" "+resource.name)
		}
		return fmt.Errorf("AWS fallback cleanup left %d managed platform-org resource(s): %s", len(remaining), strings.Join(names, ", "))
	}

	if backupDir == "" {
		backupDir = defaultRuntimeBackupDirForNukeFn(root, d.env)
	}
	resetSummary, err := resetBackendStateForNukeFn(ctx, root, stack, d.env, backupDir)
	if err != nil {
		if isNotFoundError(err) {
			// State backend bucket is already gone — nothing to reset.
			out.Status("muted", "reset", "state backend already absent; nothing to reset")
			out.Blank()
			out.Status("ok", "ok", "AWS fallback cleanup complete; terraform backend reset")
			return nil
		}
		return fmt.Errorf("reset terraform backend state: %w", err)
	}
	out.Status("ok", "reset", fmt.Sprintf("deleted backend state object versions for %s from %s", resetSummary.StateKey, resetSummary.BucketName))
	out.Status("ok", "reset", fmt.Sprintf("deleted %d matching lock row(s) from %s", resetSummary.DeletedLockEntries, resetSummary.TableName))
	if resetSummary.RemovedLocalTerraform {
		out.Status("ok", "reset", "removed local .terraform cache so the next init starts clean")
	}
	if resetSummary.BackupDir != "" {
		out.Status("info", "backup", "backend state backup written to "+resetSummary.BackupDir)
	}

	out.Blank()
	out.Status("ok", "ok", "AWS fallback cleanup complete; terraform backend reset")
	return nil
}

func explicitPlatformOrgCleanupTargets(ctx context.Context) ([]auditResource, error) {
	cfg, err := loadPlatformOrgEnvConfig()
	if err != nil {
		return nil, err
	}

	type existsCheck struct {
		resource auditResource
		exists   func(context.Context) (bool, error)
	}

	checks := []existsCheck{
		{
			resource: auditResource{status: "OK", resourceType: "organizations/organization", name: "organization", stack: stackNamePlatformOrg},
			exists:   organizationExists,
		},
		{
			resource: auditResource{status: "OK", resourceType: "organizations/organizational-unit", name: "environments", stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return organizationalUnitExists(ctx, "environments")
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: resourceTypeOrganizationsPolicy, name: policyNameDenyIAMUserCreation, stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return organizationPolicyExists(ctx, policyNameDenyIAMUserCreation)
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: resourceTypeOrganizationsPolicy, name: policyNameDenyDisableCloudTrail, stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return organizationPolicyExists(ctx, policyNameDenyDisableCloudTrail)
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: resourceTypeOrganizationsPolicy, name: orgPolicyDenyLeaveOrganizationName, stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return organizationPolicyExists(ctx, "deny-leave-organization")
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: resourceTypeOrganizationsPolicyAttachment, name: "deny-iam-user-creation@environments", stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return organizationPolicyAttachmentExists(ctx, policyNameDenyIAMUserCreation, "environments")
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: resourceTypeOrganizationsPolicyAttachment, name: "deny-disable-cloudtrail@environments", stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return organizationPolicyAttachmentExists(ctx, policyNameDenyDisableCloudTrail, "environments")
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: resourceTypeOrganizationsPolicyAttachment, name: "deny-leave-organization@environments", stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return organizationPolicyAttachmentExists(ctx, orgPolicyDenyLeaveOrganizationName, "environments")
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: "budgets/budget", name: platformAdminBudgetName(d.org), stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return budgetExists(ctx, platformAdminBudgetName(d.org))
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: resourceTypeIAMRole, name: activateLambdaName(d.org), stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return iamRoleExists(ctx, activateLambdaName(d.org))
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: resourceTypeIAMRole, name: schedulerInvokeRoleName(d.org), stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return iamRoleExists(ctx, schedulerInvokeRoleName(d.org))
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: "lambda/function", name: activateLambdaName(d.org), stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return lambdaFunctionExists(ctx, activateLambdaName(d.org))
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: "logs/log-group", name: activateLambdaLogGroupName(d.org), stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return logGroupExists(ctx, activateLambdaLogGroupName(d.org))
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: "resource-groups/group", name: bootstrapLayerGroupName(d.org), stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return resourceGroupExists(ctx, bootstrapLayerGroupName(d.org))
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: "scheduler/schedule-group", name: activationScheduleGroupName(d.org), stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return schedulerGroupExists(ctx, activationScheduleGroupName(d.org))
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: "dynamodb/table", name: runtimeLockTableName(d.org), stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return dynamoTableExists(ctx, runtimeLockTableName(d.org))
			},
		},
		{
			resource: auditResource{status: "OK", resourceType: "s3", name: runtimeStateBucketName(d.org), stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return s3BucketExists(ctx, runtimeStateBucketName(d.org))
			},
		},
	}

	for accountName := range cfg.Accounts {
		name := accountName
		checks = append(checks, existsCheck{
			resource: auditResource{status: "OK", resourceType: "organizations/account", name: name, stack: stackNamePlatformOrg},
			exists: func(ctx context.Context) (bool, error) {
				return organizationAccountExists(ctx, name)
			},
		})
	}

	resources := make([]auditResource, 0, len(checks)+1)
	oidcARN, oidcExists, err := oidcProviderARNByURL(ctx, githubActionsOIDCURL)
	if err != nil {
		if d.log != nil {
			d.log.Warn("explicit platform-org inventory check failed", "resource_type", resourceTypeIAMOIDCProvider, "name", githubActionsOIDCURL, "error", err)
		}
	} else if oidcExists {
		resources = append(resources, auditResource{
			status:       "OK",
			resourceType: resourceTypeIAMOIDCProvider,
			name:         githubActionsOIDCURL,
			arn:          oidcARN,
			stack:        stackNamePlatformOrg,
		})
	}
	for _, check := range checks {
		exists, err := check.exists(ctx)
		if err != nil {
			if d.log != nil {
				d.log.Warn("explicit platform-org inventory check failed", "resource_type", check.resource.resourceType, "name", check.resource.name, "error", err)
			}
			continue
		}
		if exists {
			resources = append(resources, check.resource)
		}
	}
	return resources, nil
}

func oidcProviderARNByURL(ctx context.Context, url string) (string, bool, error) {
	client := newIAMDeleteClient(d.awsCfg)
	out, err := client.ListOpenIDConnectProviders(ctx, &iam.ListOpenIDConnectProvidersInput{})
	if err != nil {
		return "", false, err
	}
	for _, provider := range out.OpenIDConnectProviderList {
		if provider.Arn == nil {
			continue
		}
		resp, err := client.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{
			OpenIDConnectProviderArn: provider.Arn,
		})
		if err != nil {
			return "", false, err
		}
		if sdkaws.ToString(resp.Url) == url {
			return sdkaws.ToString(provider.Arn), true, nil
		}
	}
	return "", false, nil
}

func nukeCleanupTargetRank(resource auditResource) int {
	switch resource.resourceType {
	case "scheduler/schedule":
		return 0
	case "lambda/function":
		return 1
	case "logs/log-group":
		return 2
	case resourceTypeIAMRole:
		return 3
	case "scheduler/schedule-group":
		return 4
	case "resource-groups/group":
		return 5
	case "budgets/budget":
		return 6
	case resourceTypeIAMOIDCProvider:
		return 7
	case "dynamodb/table":
		return 8
	case "s3":
		return 9
	case "organizations/policy-attachment":
		return 10
	case resourceTypeOrganizationsPolicy:
		return 11
	case "organizations/account":
		return 12
	case "organizations/organizational-unit":
		return 13
	case "organizations/organization":
		return 14
	default:
		return 20
	}
}

func nukeCleanupTargetLess(a, b auditResource) bool {
	if nukeCleanupTargetRank(a) != nukeCleanupTargetRank(b) {
		return nukeCleanupTargetRank(a) < nukeCleanupTargetRank(b)
	}
	if a.resourceType != b.resourceType {
		return a.resourceType < b.resourceType
	}
	return a.name < b.name
}

func loadBackendStateConfigForNuke(root, env string) (nukeBackendStateConfig, error) {
	stackPath := filepath.Join(root, stackDirName)
	localConfig, err := parseBackendConfigFile(filepath.Join(stackPath, "backend.local.hcl"))
	if err != nil {
		return nukeBackendStateConfig{}, err
	}
	envConfig, err := parseBackendConfigFile(filepath.Join(root, envsDirName, env, "backend.hcl"))
	if err != nil {
		return nukeBackendStateConfig{}, err
	}

	cfg := nukeBackendStateConfig{
		BucketName: localConfig["bucket"],
		TableName:  localConfig["dynamodb_table"],
		StateKey:   envConfig["key"],
	}
	if cfg.BucketName == "" || cfg.TableName == "" || cfg.StateKey == "" {
		return nukeBackendStateConfig{}, fmt.Errorf("backend config incomplete: bucket=%q table=%q key=%q", cfg.BucketName, cfg.TableName, cfg.StateKey)
	}
	return cfg, nil
}

func parseBackendConfigFile(path string) (map[string]string, error) {
	//nolint:gosec // path is derived from known internal config locations, not user input
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	values := map[string]string{}
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\"")
		if key != "" && value != "" {
			values[key] = value
		}
	}
	return values, nil
}

func backupBackendStateData(ctx context.Context, s3Client nukeBackendResetS3API, dynamoClient nukeBackendResetDynamoAPI, cfg nukeBackendStateConfig, stateVersions []s3types.ObjectVersion, deleteMarkers []s3types.DeleteMarkerEntry, lockItems []map[string]dbtypes.AttributeValue, backupDir string) (string, error) {
	if len(stateVersions) == 0 && len(deleteMarkers) == 0 && len(lockItems) == 0 {
		return "", nil
	}
	targetDir := filepath.Join(backupDir, "backend-reset")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	if len(stateVersions) > 0 || len(deleteMarkers) > 0 {
		metadata, err := backupStateObjectVersions(ctx, s3Client, cfg.BucketName, cfg.StateKey, filepath.Join(targetDir, "s3"))
		if err != nil {
			return "", err
		}
		if err := writeJSONFile(filepath.Join(targetDir, "s3", "manifest.json"), metadata); err != nil {
			return "", err
		}
	}
	if len(lockItems) > 0 {
		if err := backupLockItems(lockItems, filepath.Join(targetDir, "dynamodb", "lock-items.json")); err != nil {
			return "", err
		}
	}
	return targetDir, nil
}

func deleteStateVersionsAndMarkers(ctx context.Context, client nukeBackendResetS3API, cfg nukeBackendStateConfig, stateVersions []s3types.ObjectVersion, deleteMarkers []s3types.DeleteMarkerEntry) (int, int, error) {
	deletedVersions, deletedMarkers := 0, 0
	for _, version := range stateVersions {
		_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket:    sdkaws.String(cfg.BucketName),
			Key:       sdkaws.String(cfg.StateKey),
			VersionId: version.VersionId,
		})
		if err != nil && !isNotFoundError(err) {
			return deletedVersions, deletedMarkers, fmt.Errorf("delete backend state version %s: %w", sdkaws.ToString(version.VersionId), err)
		}
		deletedVersions++
	}
	for _, marker := range deleteMarkers {
		_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket:    sdkaws.String(cfg.BucketName),
			Key:       sdkaws.String(cfg.StateKey),
			VersionId: marker.VersionId,
		})
		if err != nil && !isNotFoundError(err) {
			return deletedVersions, deletedMarkers, fmt.Errorf("delete backend state delete marker %s: %w", sdkaws.ToString(marker.VersionId), err)
		}
		deletedMarkers++
	}
	return deletedVersions, deletedMarkers, nil
}

func deleteLockRows(ctx context.Context, client nukeBackendResetDynamoAPI, tableName string, lockItems []map[string]dbtypes.AttributeValue) (int, error) {
	deleted := 0
	for _, item := range lockItems {
		lockID := lockItemString(item, "LockID")
		if lockID == "" {
			continue
		}
		_, err := client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: sdkaws.String(tableName),
			Key: map[string]dbtypes.AttributeValue{
				"LockID": &dbtypes.AttributeValueMemberS{Value: lockID},
			},
		})
		if err != nil && !isNotFoundError(err) {
			return deleted, fmt.Errorf("delete backend lock row %s: %w", lockID, err)
		}
		deleted++
	}
	return deleted, nil
}

func removeLocalTerraformArtifacts(stack string) error {
	for _, target := range []string{
		filepath.Join(stack, ".terraform"),
		filepath.Join(stack, ".terraform.tfstate.lock.info"),
	} {
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove %s: %w", target, err)
		}
	}
	return nil
}

func resetBackendStateForNuke(ctx context.Context, root, stack, env, backupDir string) (nukeBackendResetSummary, error) {
	cfg, err := loadBackendStateConfigForNukeFn(root, env)
	if err != nil {
		return nukeBackendResetSummary{}, err
	}
	summary := nukeBackendResetSummary{BucketName: cfg.BucketName, TableName: cfg.TableName, StateKey: cfg.StateKey}

	s3Client := newNukeBackendResetS3ClientFn(d.awsCfg)
	dynamoClient := newNukeBackendResetDynamoClientFn(d.awsCfg)

	stateVersions, deleteMarkers, err := listStateObjectVersions(ctx, s3Client, cfg.BucketName, cfg.StateKey)
	if err != nil {
		return nukeBackendResetSummary{}, err
	}
	lockItems, err := findMatchingLockItems(ctx, dynamoClient, cfg.TableName, cfg.BucketName, cfg.StateKey)
	if err != nil {
		return nukeBackendResetSummary{}, err
	}

	summary.BackupDir, err = backupBackendStateData(ctx, s3Client, dynamoClient, cfg, stateVersions, deleteMarkers, lockItems, backupDir)
	if err != nil {
		return nukeBackendResetSummary{}, err
	}

	summary.DeletedStateVersions, summary.DeletedDeleteMarkers, err = deleteStateVersionsAndMarkers(ctx, s3Client, cfg, stateVersions, deleteMarkers)
	if err != nil {
		return nukeBackendResetSummary{}, err
	}

	summary.DeletedLockEntries, err = deleteLockRows(ctx, dynamoClient, cfg.TableName, lockItems)
	if err != nil {
		return nukeBackendResetSummary{}, err
	}

	if err := removeLocalTerraformArtifacts(stack); err != nil {
		return nukeBackendResetSummary{}, err
	}
	summary.RemovedLocalTerraform = true

	return summary, nil
}

func listStateObjectVersions(ctx context.Context, client nukeBackendResetS3API, bucket, stateKey string) ([]s3types.ObjectVersion, []s3types.DeleteMarkerEntry, error) {
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: sdkaws.String(bucket)})
	if err != nil {
		if isS3BucketMissing(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("check backend bucket %s: %w", bucket, err)
	}

	var versions []s3types.ObjectVersion
	var markers []s3types.DeleteMarkerEntry
	var keyMarker, versionMarker *string
	for {
		out, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:          sdkaws.String(bucket),
			Prefix:          sdkaws.String(stateKey),
			KeyMarker:       keyMarker,
			VersionIdMarker: versionMarker,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list backend state versions %s/%s: %w", bucket, stateKey, err)
		}
		versions = appendMatchingVersions(versions, out.Versions, stateKey)
		markers = appendMatchingMarkers(markers, out.DeleteMarkers, stateKey)
		if !sdkaws.ToBool(out.IsTruncated) {
			break
		}
		keyMarker = out.NextKeyMarker
		versionMarker = out.NextVersionIdMarker
	}
	return versions, markers, nil
}

func appendMatchingVersions(dst []s3types.ObjectVersion, src []s3types.ObjectVersion, key string) []s3types.ObjectVersion {
	for _, v := range src {
		if sdkaws.ToString(v.Key) == key {
			dst = append(dst, v)
		}
	}
	return dst
}

func appendMatchingMarkers(dst []s3types.DeleteMarkerEntry, src []s3types.DeleteMarkerEntry, key string) []s3types.DeleteMarkerEntry {
	for _, m := range src {
		if sdkaws.ToString(m.Key) == key {
			dst = append(dst, m)
		}
	}
	return dst
}

func backupStateObjectVersions(ctx context.Context, client nukeBackendResetS3API, bucket, stateKey, dir string) ([]map[string]any, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	versions, markers, err := listStateObjectVersions(ctx, client, bucket, stateKey)
	if err != nil {
		return nil, err
	}

	metadata := make([]map[string]any, 0, len(versions)+len(markers))
	for i, version := range versions {
		name := fmt.Sprintf("state-%06d.bin", i+1)
		target := filepath.Join(dir, name)
		if err := downloadBucketVersion(ctx, client, bucket, stateKey, sdkaws.ToString(version.VersionId), target); err != nil {
			return nil, err
		}
		metadata = append(metadata, map[string]any{
			"file":       name,
			"key":        stateKey,
			"version_id": sdkaws.ToString(version.VersionId),
			"size":       version.Size,
			"is_latest":  sdkaws.ToBool(version.IsLatest),
		})
	}
	for _, marker := range markers {
		metadata = append(metadata, map[string]any{
			"delete_marker": true,
			"key":           stateKey,
			"version_id":    sdkaws.ToString(marker.VersionId),
			"is_latest":     sdkaws.ToBool(marker.IsLatest),
		})
	}
	return metadata, nil
}

func findMatchingLockItems(ctx context.Context, client nukeBackendResetDynamoAPI, table, bucket, stateKey string) ([]map[string]dbtypes.AttributeValue, error) {
	_, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: sdkaws.String(table)})
	if err != nil {
		var notFound *dbtypes.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("describe backend lock table %s: %w", table, err)
	}

	var matches []map[string]dbtypes.AttributeValue
	var startKey map[string]dbtypes.AttributeValue
	for {
		out, err := client.Scan(ctx, &dynamodb.ScanInput{
			TableName:         sdkaws.String(table),
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("scan backend lock table %s: %w", table, err)
		}
		for _, item := range out.Items {
			if matchesTerraformLockItem(item, bucket, stateKey) {
				matches = append(matches, item)
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}
	return matches, nil
}

func matchesTerraformLockItem(item map[string]dbtypes.AttributeValue, bucket, stateKey string) bool {
	needles := []string{
		strings.ToLower(stateKey),
		strings.ToLower(bucket + "/" + stateKey),
	}
	for _, field := range []string{"LockID", "Info", "Path"} {
		value := strings.ToLower(lockItemString(item, field))
		for _, needle := range needles {
			if needle != "" && strings.Contains(value, needle) {
				return true
			}
		}
	}
	return false
}

func lockItemString(item map[string]dbtypes.AttributeValue, field string) string {
	raw, ok := item[field]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case *dbtypes.AttributeValueMemberS:
		return v.Value
	default:
		return ""
	}
}

func backupLockItems(items []map[string]dbtypes.AttributeValue, path string) error {
	decoded := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var value map[string]any
		if err := attributevalue.UnmarshalMap(item, &value); err != nil {
			return fmt.Errorf("decode backend lock item: %w", err)
		}
		decoded = append(decoded, value)
	}
	return writeJSONFile(path, decoded)
}
