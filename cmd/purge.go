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

var (
	newCloudControlClient = func(cfg sdkaws.Config) cloudControlAPI {
		return cloudcontrol.NewFromConfig(cfg)
	}
	purgeStdout io.Writer = os.Stdout
	purgeForce  bool
)

// arnToCloudControl maps a parsed ARN (service, resourceType, name) to the
// CloudFormation type name and primary identifier expected by Cloud Control API.
func arnToCloudControl(arn, service, resourceType, name string) (cfnType, identifier string) {
	switch service {
	case "s3":
		return "AWS::S3::Bucket", name
	case "dynamodb":
		if resourceType == "dynamodb/table" {
			return "AWS::DynamoDB::Table", name
		}
	case "sns":
		return "AWS::SNS::Topic", arn
	case "sqs":
		return "AWS::SQS::Queue", arn
	case "iam":
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
	case "lambda":
		if resourceType == "lambda/function" {
			return "AWS::Lambda::Function", name
		}
	case "ecr":
		if resourceType == "ecr/repository" {
			return "AWS::ECR::Repository", name
		}
	case "logs":
		if resourceType == "logs/log-group" {
			return "AWS::Logs::LogGroup", name
		}
	case "secretsmanager":
		if resourceType == "secretsmanager/secret" {
			return "AWS::SecretsManager::Secret", arn
		}
	case "ssm":
		if resourceType == "ssm/parameter" {
			return "AWS::SSM::Parameter", name
		}
	case "kms":
		if resourceType == "kms/key" {
			return "AWS::KMS::Key", name
		}
	case "elasticloadbalancing":
		if resourceType == "elasticloadbalancing/loadbalancer" {
			return "AWS::ElasticLoadBalancingV2::LoadBalancer", arn
		}
	case "ecs":
		switch resourceType {
		case "ecs/cluster":
			return "AWS::ECS::Cluster", name
		case "ecs/service":
			return "AWS::ECS::Service", arn
		case "ecs/task-definition":
			return "AWS::ECS::TaskDefinition", arn
		}
	case "ec2":
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
	case "events":
		if resourceType == "events/rule" {
			return "AWS::Events::Rule", name
		}
	case "sagemaker":
		if resourceType == "sagemaker/notebook-instance" {
			return "AWS::SageMaker::NotebookInstance", name
		}
	case "servicediscovery":
		if resourceType == "servicediscovery/namespace" {
			return "AWS::ServiceDiscovery::PrivateDnsNamespace", name
		}
	case "lightsail":
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
				case <-time.After(2 * time.Second):
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
		case <-time.After(2 * time.Second):
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
		case <-time.After(backoff):
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
		var deleteErrs []string
		deleted := 0
		gone := 0
		manual := 0
		blocked := 0
		for _, t := range targets {
			out.Status("running", "...", fmt.Sprintf("deleting %s %s", t.resource.resourceType, t.resource.name))
			if handled, err := deleteResourceNatively(ctx, t.resource, purgeForce); handled {
				switch classifyPurgeDeleteError(err) {
				case purgeFailureGone:
					gone++
					out.Status("muted", "skip", fmt.Sprintf(purgeAlreadyAbsentFormat, t.resource.resourceType, t.resource.name))
				case purgeFailureManual:
					manual++
					out.Status("warn", "skip", fmt.Sprintf("%s %s requires manual deletion", t.resource.resourceType, t.resource.name))
				case purgeFailureBlocked:
					blocked++
					detail := fmt.Sprintf("%s %s is blocked by dependent resources", t.resource.resourceType, t.resource.name)
					if !purgeForce {
						detail += purgeRerunWithForceHint
					}
					out.Status("warn", "wait", detail)
				case purgeFailureFatal:
					if err != nil {
						deleteErrs = append(deleteErrs, fmt.Sprintf("%s %s: %v", t.resource.resourceType, t.resource.name, err))
						out.Status("error", "fail", fmt.Sprintf("delete %s %s: %v", t.resource.resourceType, t.resource.name, err))
					} else {
						deleted++
						out.Status("ok", "ok", fmt.Sprintf("deleted %s %s", t.resource.resourceType, t.resource.name))
					}
				case purgeFailureRetryable:
					deleteErrs = append(deleteErrs, fmt.Sprintf("%s %s: %v", t.resource.resourceType, t.resource.name, err))
					out.Status("error", "fail", fmt.Sprintf("delete %s %s: %v", t.resource.resourceType, t.resource.name, err))
				}
				continue
			}
			resp, err := deleteResourceWithRetry(ctx, cc, &cloudcontrol.DeleteResourceInput{
				TypeName:      sdkaws.String(t.cfnType),
				Identifier:    sdkaws.String(t.identifier),
				ClientToken:   sdkaws.String(purgeClientToken(t.cfnType, t.identifier)),
				RoleArn:       nil,
				TypeVersionId: nil,
			})
			if err != nil {
				switch classifyPurgeDeleteError(err) {
				case purgeFailureGone:
					gone++
					out.Status("muted", "skip", fmt.Sprintf(purgeAlreadyAbsentFormat, t.resource.resourceType, t.resource.name))
				case purgeFailureManual:
					manual++
					out.Status("warn", "skip", fmt.Sprintf("%s %s requires manual deletion", t.resource.resourceType, t.resource.name))
				case purgeFailureBlocked:
					blocked++
					detail := fmt.Sprintf("%s %s is blocked by dependent resources", t.resource.resourceType, t.resource.name)
					if !purgeForce {
						detail += purgeRerunWithForceHint
					}
					out.Status("warn", "wait", detail)
				default:
					deleteErrs = append(deleteErrs, fmt.Sprintf("%s %s: %v", t.resource.resourceType, t.resource.name, err))
					out.Status("error", "fail", fmt.Sprintf("delete %s %s: %v", t.resource.resourceType, t.resource.name, err))
				}
				continue
			}
			if err := waitForDelete(ctx, cc, sdkaws.ToString(resp.ProgressEvent.RequestToken)); err != nil {
				switch classifyPurgeDeleteError(err) {
				case purgeFailureGone:
					gone++
					out.Status("muted", "skip", fmt.Sprintf(purgeAlreadyAbsentFormat, t.resource.resourceType, t.resource.name))
				case purgeFailureManual:
					manual++
					out.Status("warn", "skip", fmt.Sprintf("%s %s requires manual deletion", t.resource.resourceType, t.resource.name))
				case purgeFailureBlocked:
					blocked++
					detail := fmt.Sprintf("%s %s is blocked by dependent resources", t.resource.resourceType, t.resource.name)
					if !purgeForce {
						detail += purgeRerunWithForceHint
					}
					out.Status("warn", "wait", detail)
				default:
					deleteErrs = append(deleteErrs, fmt.Sprintf("%s %s: %v", t.resource.resourceType, t.resource.name, err))
					out.Status("error", "fail", fmt.Sprintf("delete %s %s: %v", t.resource.resourceType, t.resource.name, err))
				}
				continue
			}
			deleted++
			out.Status("ok", "ok", fmt.Sprintf("deleted %s %s", t.resource.resourceType, t.resource.name))
		}

		out.Blank()
		out.Summary(
			"Summary",
			countPart("deleted", deleted),
			countPart("gone", gone),
			countPart("manual", manual),
			countPart("blocked", blocked),
			countPart("failed", len(deleteErrs)),
		)
		if len(deleteErrs) > 0 {
			for _, msg := range deleteErrs {
				out.Status("error", "fail", msg)
			}
			return fmt.Errorf("purge completed with %d deletion failure(s)", len(deleteErrs))
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
