package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/spf13/cobra"
)

// cloudControlAPI is the minimal surface of the Cloud Control client used here.
type cloudControlAPI interface {
	DeleteResource(context.Context, *cloudcontrol.DeleteResourceInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error)
	GetResourceRequestStatus(context.Context, *cloudcontrol.GetResourceRequestStatusInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error)
}

type purgeFailureDisposition int

const (
	purgeFailureFatal purgeFailureDisposition = iota
	purgeFailureGone
	purgeFailureManual
	purgeFailureBlocked
	purgeFailureRetryable
)

const (
	purgeAlreadyAbsentFormat = "%s %s already absent"
	purgeRerunWithForceHint  = "; re-run with --force"
	purgeRequiresManualMsg   = "%s %s requires manual deletion"
	purgeBlockedByDepsMsg    = "%s %s is blocked by dependent resources"
	fmtDeleteResourceErr     = "delete %s %s: %v"
)

// purgeManualError wraps errors that require operator intervention — the
// resource exists but cannot be deleted automatically (e.g. ECS-managed
// EventBridge rules). The hint explains what to do manually.
type purgeManualError struct {
	cause error
	hint  string
}

func (e *purgeManualError) Error() string {
	if e.hint != "" {
		return e.cause.Error() + " (" + e.hint + ")"
	}
	return e.cause.Error()
}

func (e *purgeManualError) Unwrap() error { return e.cause }

type purgeDeleteCounts struct {
	deleted, gone, manual, blocked int
	errs                           []string
}

func recordPurgeDeleteResult(out *commandOutput, resource auditResource, err error, counts *purgeDeleteCounts, force bool) {
	switch classifyPurgeDeleteError(err) {
	case purgeFailureGone:
		counts.gone++
		out.Status("muted", "skip", fmt.Sprintf(purgeAlreadyAbsentFormat, resource.resourceType, resource.name))
	case purgeFailureManual:
		counts.manual++
		out.Status("warn", "skip", fmt.Sprintf(purgeRequiresManualMsg, resource.resourceType, resource.name))
	case purgeFailureBlocked:
		counts.blocked++
		detail := fmt.Sprintf(purgeBlockedByDepsMsg, resource.resourceType, resource.name)
		if !force {
			detail += purgeRerunWithForceHint
		}
		out.Status("warn", "wait", detail)
	default:
		counts.errs = append(counts.errs, fmt.Sprintf(fmtResourceNameErr, resource.resourceType, resource.name, err))
		out.Status("error", "fail", fmt.Sprintf(fmtDeleteResourceErr, resource.resourceType, resource.name, err))
	}
}

var (
	newCloudControlClient = func(cfg sdkaws.Config) cloudControlAPI {
		return cloudcontrol.NewFromConfig(cfg)
	}
	purgeStdout io.Writer = os.Stdout
	purgeForce  bool
	purgeAfter  = time.After
)

func iamToCloudControl(resourceType, name, arn string) (string, string) {
	switch resourceType {
	case "iam/role":
		return "AWS::IAM::Role", name
	case "iam/policy":
		return "AWS::IAM::ManagedPolicy", arn
	case "iam/user":
		return "AWS::IAM::User", name
	case "iam/group":
		return "AWS::IAM::Group", name
	}
	return "", ""
}

func ec2ToCloudControl(resourceType, name string) (string, string) {
	switch resourceType {
	case "ec2/internet-gateway":
		return "AWS::EC2::InternetGateway", name
	case "ec2/route-table":
		return "AWS::EC2::RouteTable", name
	case "ec2/subnet":
		return "AWS::EC2::Subnet", name
	case "ec2/vpc":
		return "AWS::EC2::VPC", name
	case "ec2/security-group":
		return "AWS::EC2::SecurityGroup", name
	}
	return "", ""
}

func ecsToCloudControl(resourceType, name, arn string) (string, string) {
	switch resourceType {
	case "ecs/cluster":
		return "AWS::ECS::Cluster", name
	case "ecs/service":
		return "AWS::ECS::Service", arn
	case "ecs/task-definition":
		return "AWS::ECS::TaskDefinition", arn
	}
	return "", ""
}

func lightsailToCloudControl(resourceType, name string) (string, string) {
	switch resourceType {
	case "lightsail/StaticIp":
		return "AWS::Lightsail::StaticIp", name
	case "lightsail/KeyPair":
		return "AWS::Lightsail::KeyPair", name
	case "lightsail/Instance":
		return "AWS::Lightsail::Instance", name
	case "lightsail/Disk":
		return "AWS::Lightsail::Disk", name
	case "lightsail/Bucket":
		return "AWS::Lightsail::Bucket", name
	}
	return "", ""
}

type cloudControlMapping struct {
	cfnType string
	useARN  bool
}

func (mapping cloudControlMapping) identifier(name, arn string) string {
	if mapping.useARN {
		return arn
	}
	return name
}

func cloudControlServiceMapping(service string) (cloudControlMapping, bool) {
	mapping, ok := map[string]cloudControlMapping{
		"s3":  {cfnType: "AWS::S3::Bucket"},
		"sns": {cfnType: "AWS::SNS::Topic", useARN: true},
		"sqs": {cfnType: "AWS::SQS::Queue", useARN: true},
	}[service]
	return mapping, ok
}

func cloudControlTypedMapping(service, resourceType string) (cloudControlMapping, bool) {
	mapping, ok := map[string]cloudControlMapping{
		"dynamodb|dynamodb/table":              {cfnType: "AWS::DynamoDB::Table"},
		"lambda|lambda/function":               {cfnType: "AWS::Lambda::Function"},
		"ecr|ecr/repository":                   {cfnType: "AWS::ECR::Repository"},
		"logs|logs/log-group":                  {cfnType: "AWS::Logs::LogGroup"},
		"secretsmanager|secretsmanager/secret": {cfnType: "AWS::SecretsManager::Secret", useARN: true},
		"ssm|ssm/parameter":                    {cfnType: "AWS::SSM::Parameter"},
		"kms|kms/key":                          {cfnType: "AWS::KMS::Key"},
		"elasticloadbalancing|elasticloadbalancing/loadbalancer": {cfnType: "AWS::ElasticLoadBalancingV2::LoadBalancer", useARN: true},
		"events|events/rule":                          {cfnType: "AWS::Events::Rule"},
		"sagemaker|sagemaker/notebook-instance":       {cfnType: "AWS::SageMaker::NotebookInstance"},
		"servicediscovery|servicediscovery/namespace": {cfnType: "AWS::ServiceDiscovery::PrivateDnsNamespace"},
	}[service+"|"+resourceType]
	return mapping, ok
}

// arnToCloudControl maps a parsed ARN (service, resourceType, name) to the
// CloudFormation type name and primary identifier expected by Cloud Control API.
func arnToCloudControl(arn, service, resourceType, name string) (cfnType, identifier string) {
	if mapping, ok := cloudControlServiceMapping(service); ok {
		return mapping.cfnType, mapping.identifier(name, arn)
	}

	if mapping, ok := cloudControlTypedMapping(service, resourceType); ok {
		return mapping.cfnType, mapping.identifier(name, arn)
	}

	switch service {
	case "iam":
		return iamToCloudControl(resourceType, name, arn)
	case "ecs":
		return ecsToCloudControl(resourceType, name, arn)
	case "ec2":
		return ec2ToCloudControl(resourceType, name)
	case "lightsail":
		return lightsailToCloudControl(resourceType, name)
	}
	return "", ""
}

// waitForDelete polls the Cloud Control progress event until the delete
// operation completes or fails. Cloud Control deletes are asynchronous.
func waitForDelete(ctx context.Context, cc cloudControlAPI, token string) error {
	for {
		out, err := cc.GetResourceRequestStatus(ctx, &cloudcontrol.GetResourceRequestStatusInput{
			RequestToken: sdkaws.String(token),
		})
		if err != nil {
			if classifyPurgeDeleteError(err) == purgeFailureRetryable {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-purgeAfter(2 * time.Second):
					continue
				}
			}
			return fmt.Errorf("polling delete status: %w", err)
		}

		status := out.ProgressEvent.OperationStatus
		switch status {
		case cctypes.OperationStatusSuccess:
			return nil
		case cctypes.OperationStatusFailed, cctypes.OperationStatusCancelComplete:
			msg := sdkaws.ToString(out.ProgressEvent.StatusMessage)
			return fmt.Errorf("delete failed: %s", msg)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-purgeAfter(2 * time.Second):
		}
	}
}

// classifyPurgeDeleteError maps a delete error to its disposition.
// Called with err=nil, it returns purgeFailureFatal; callers then check err!=nil
// in the purgeFailureFatal/Retryable branch to distinguish success from failure.
func classifyPurgeDeleteError(err error) purgeFailureDisposition {
	if err == nil {
		return purgeFailureFatal
	}

	var manual *purgeManualError
	if errors.As(err, &manual) {
		return purgeFailureManual
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "was not found"),
		strings.Contains(msg, "does not exist"),
		strings.Contains(msg, "doesn't exist"),
		strings.Contains(msg, "cannot be found"),
		strings.Contains(msg, "not found"),
		strings.Contains(msg, "resourcenotfoundexception"),
		strings.Contains(msg, "nosuchentity"),
		strings.Contains(msg, "nosuchbucket"):
		return purgeFailureGone
	case strings.Contains(msg, "throttlingexception"),
		strings.Contains(msg, "rate exceeded"),
		strings.Contains(msg, "too many requests"):
		return purgeFailureRetryable
	case strings.Contains(msg, "unsupportedactionexception"),
		strings.Contains(msg, "does not support delete action"),
		strings.Contains(msg, "typenotfoundexception"),
		strings.Contains(msg, "managed rule"):
		return purgeFailureManual
	case strings.Contains(msg, "has dependencies and cannot be deleted"),
		strings.Contains(msg, "can't be deleted since it has targets"),
		strings.Contains(msg, "dependencyviolation"),
		strings.Contains(msg, "resourceinuse"):
		return purgeFailureBlocked
	default:
		return purgeFailureFatal
	}
}

func deleteResourceWithRetry(ctx context.Context, cc cloudControlAPI, input *cloudcontrol.DeleteResourceInput) (*cloudcontrol.DeleteResourceOutput, error) {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		resp, err := cc.DeleteResource(ctx, input)
		if err == nil {
			return resp, nil
		}
		if classifyPurgeDeleteError(err) != purgeFailureRetryable || attempt >= 5 {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-purgeAfter(backoff):
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
}

var purgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Delete all UNOWNED resources found by audit",
	Long: `purge scans for unowned resources (same logic as audit) and deletes them.

A resource is unowned if it lacks ManagedBy=terraform or Layer=bootstrap.
This is intended to clean up manually-created or abandoned resources.

Uses the AWS Cloud Control API for generic deletion — no per-resource-type
code required. Unsupported resource types are listed but skipped.

	Always prompts for confirmation before deleting anything.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		out := newWriterOutput(purgeStdout, purgeStdout, d.ui)
		out.Header("Platform Org Purge", envAccountRegionSummary(d.env, d.accountID, d.region))
		out.Blank()

		resources, err := scanResourcesFn(ctx)
		if err != nil {
			return fmt.Errorf("scanning resources: %w", err)
		}

		var unowned []auditResource
		for _, r := range resources {
			if r.status == "UNOWNED" {
				unowned = append(unowned, r)
			}
		}

		if len(unowned) == 0 {
			out.Status("ok", "ok", "no unowned resources found")
			return nil
		}

		out.Summary("Summary", countPart("unowned", len(unowned)))
		rows := make([][]string, 0, len(unowned))
		for _, r := range unowned {
			rows = append(rows, []string{auditStatusCell(r.status), r.resourceType, r.name})
		}
		_ = out.Table([]string{"STATUS", "TYPE", "NAME"}, rows)

		type deleteTarget struct {
			resource   auditResource
			cfnType    string
			identifier string
		}
		var targets []deleteTarget
		var skipped []auditResource

		for _, r := range unowned {
			service, rtype := parseServiceType(r.resourceType)
			cfnType, identifier := arnToCloudControl(r.arn, service, rtype, r.name)
			if cfnType == "" {
				skipped = append(skipped, r)
				continue
			}
			targets = append(targets, deleteTarget{resource: r, cfnType: cfnType, identifier: identifier})
		}

		if len(skipped) > 0 {
			out.Blank()
			out.Status("warn", "skip", "some resource types are unsupported and must be deleted manually")
			skippedRows := make([][]string, 0, len(skipped))
			for _, r := range skipped {
				skippedRows = append(skippedRows, []string{"skip", r.resourceType, r.name})
			}
			_ = out.Table([]string{"STATUS", "TYPE", "NAME"}, skippedRows)
		}

		if len(targets) == 0 {
			out.Blank()
			out.Status("warn", "skip", "no supported resource types to delete automatically")
			return nil
		}

		out.Blank()
		out.Status("warn", "warn", fmt.Sprintf("will delete %d resource(s) via Cloud Control API", len(targets)))
		_, _ = fmt.Fprint(purgeStdout, "Type \"purge\" to confirm: ")

		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return fmt.Errorf("no input received")
		}
		if strings.TrimSpace(scanner.Text()) != "purge" {
			if d.ui != nil {
				out.Status("muted", "skip", "operator confirmation did not match")
			} else {
				out.Line("Cancelled.")
			}
			return nil
		}
		out.Blank()

		cc := newCloudControlClient(d.awsCfg)
		var counts purgeDeleteCounts
		for _, t := range targets {
			out.Status("running", "...", fmt.Sprintf("deleting %s %s", t.resource.resourceType, t.resource.name))
			if handled, err := deleteResourceNatively(ctx, t.resource, purgeForce); handled {
				if err == nil {
					counts.deleted++
					out.Status("ok", "ok", fmt.Sprintf("deleted %s %s", t.resource.resourceType, t.resource.name))
				} else {
					recordPurgeDeleteResult(out, t.resource, err, &counts, purgeForce)
				}
				continue
			}
			resp, err := deleteResourceWithRetry(ctx, cc, &cloudcontrol.DeleteResourceInput{
				TypeName:    sdkaws.String(t.cfnType),
				Identifier:  sdkaws.String(t.identifier),
				ClientToken: sdkaws.String(purgeClientToken(t.cfnType, t.identifier)),
			})
			if err != nil {
				recordPurgeDeleteResult(out, t.resource, err, &counts, purgeForce)
				continue
			}
			if err := waitForDelete(ctx, cc, sdkaws.ToString(resp.ProgressEvent.RequestToken)); err != nil {
				recordPurgeDeleteResult(out, t.resource, err, &counts, purgeForce)
				continue
			}
			counts.deleted++
			out.Status("ok", "ok", fmt.Sprintf("deleted %s %s", t.resource.resourceType, t.resource.name))
		}

		out.Blank()
		out.Summary(
			"Summary",
			countPart("deleted", counts.deleted),
			countPart("gone", counts.gone),
			countPart("manual", counts.manual),
			countPart("blocked", counts.blocked),
			countPart("failed", len(counts.errs)),
		)
		if len(counts.errs) > 0 {
			for _, msg := range counts.errs {
				out.Status("error", "fail", msg)
			}
			return fmt.Errorf("purge completed with %d deletion failure(s)", len(counts.errs))
		}
		return nil
	},
}

func parseServiceType(resourceType string) (service, fullType string) {
	parts := strings.SplitN(resourceType, "/", 2)
	if len(parts) == 1 {
		return resourceType, resourceType
	}
	return parts[0], resourceType
}

func purgeClientToken(cfnType, identifier string) string {
	sum := sha256.Sum256([]byte(cfnType + "|" + identifier))
	return "platform-org-purge-" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func init() {
	rootCmd.AddCommand(purgeCmd)
	purgeCmd.Flags().BoolVar(&purgeForce, "force", true, "Force dependency cleanup for supported resource types before deletion")
}
