package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	platformui "github.com/ffreis/platform-org/internal/ui"
)

const testDoctorCommandBucketName = "ffreis-tf-state-root"

func restoreDoctorTestHooks(t *testing.T) {
	t.Helper()
	oldD := d
	oldRun := platformOrgDoctorRunFn
	oldBackend := platformOrgBackendDoctorSectionFn
	oldState := platformOrgStateDoctorSectionFn
	oldRuntime := platformOrgRuntimeDoctorSectionFn
	oldInventory := platformOrgInventoryDoctorSectionFn
	oldLoadState := loadPlatformOrgStateObjectsFn
	oldPolicy := getInlineRolePolicyDocumentFn
	oldSchedule := activationScheduleFn
	oldBucket := s3BucketExistsFn
	oldTable := dynamoTableExistsFn
	oldLambda := getLambdaFunctionFn
	oldLog := logGroupExistsFn
	oldBuild := buildAuditSectionsFn
	oldScan := scanResourcesFn
	oldPlan := terraformPlanJSONFn
	oldLoadBackend := loadBackendStateConfigForNukeFn
	oldS3 := newNukeBackendResetS3ClientFn
	oldDynamo := newNukeBackendResetDynamoClientFn
	t.Cleanup(func() {
		d = oldD
		platformOrgDoctorRunFn = oldRun
		platformOrgBackendDoctorSectionFn = oldBackend
		platformOrgStateDoctorSectionFn = oldState
		platformOrgRuntimeDoctorSectionFn = oldRuntime
		platformOrgInventoryDoctorSectionFn = oldInventory
		loadPlatformOrgStateObjectsFn = oldLoadState
		getInlineRolePolicyDocumentFn = oldPolicy
		activationScheduleFn = oldSchedule
		s3BucketExistsFn = oldBucket
		dynamoTableExistsFn = oldTable
		getLambdaFunctionFn = oldLambda
		logGroupExistsFn = oldLog
		buildAuditSectionsFn = oldBuild
		scanResourcesFn = oldScan
		terraformPlanJSONFn = oldPlan
		loadBackendStateConfigForNukeFn = oldLoadBackend
		newNukeBackendResetS3ClientFn = oldS3
		newNukeBackendResetDynamoClientFn = oldDynamo
		_ = doctorCmd.Flags().Set("json", "false")
		doctorCmd.SetOut(io.Discard)
	})
}

func testDoctorPlanJSON() []byte {
	return []byte(`{
		"planned_values": {
			"root_module": {
				"resources": [
					{
						"address": "aws_s3_bucket.state",
						"mode": "managed",
						"type": "aws_s3_bucket",
						"name": "state",
						"values": {
							"bucket": "` + testDoctorCommandBucketName + `",
							"arn": "arn:aws:s3:::` + testDoctorCommandBucketName + `",
							"tags": {
								"ManagedBy": "terraform",
								"Stack": "platform-org",
								"Project": "platform",
								"Environment": "prod"
							}
						}
					}
				]
			}
		},
		"resource_changes": []
	}`)
}

func TestRunPlatformOrgDoctorIncludesEnabledSections(t *testing.T) {
	restoreDoctorTestHooks(t)
	platformOrgBackendDoctorSectionFn = func(context.Context) (platformOrgDoctorSection, error) {
		return platformOrgDoctorSection{Title: "Backend", Checks: []platformOrgDoctorCheck{{Status: "ok"}}}, nil
	}
	platformOrgStateDoctorSectionFn = func(context.Context, platformOrgDoctorMode) (platformOrgDoctorSection, error) {
		return platformOrgDoctorSection{Title: "State", Checks: []platformOrgDoctorCheck{{Status: "warn"}}}, nil
	}
	platformOrgRuntimeDoctorSectionFn = func(context.Context, platformOrgDoctorMode) (platformOrgDoctorSection, error) {
		return platformOrgDoctorSection{Title: "Runtime", Checks: []platformOrgDoctorCheck{{Status: "fail", Blocking: true}}}, nil
	}
	platformOrgInventoryDoctorSectionFn = func(context.Context) (platformOrgDoctorSection, error) {
		return platformOrgDoctorSection{Title: "Inventory", Checks: []platformOrgDoctorCheck{{Status: "info"}}}, nil
	}

	report, err := runPlatformOrgDoctor(context.Background(), platformOrgDoctorMode{
		Name:             "doctor",
		IncludeBackend:   true,
		IncludeState:     true,
		IncludeRuntime:   true,
		IncludeInventory: true,
	})
	if err != nil {
		t.Fatalf("runPlatformOrgDoctor: %v", err)
	}
	if len(report.Sections) != 4 {
		t.Fatalf("expected 4 sections, got %d", len(report.Sections))
	}
	if report.Summary != (platformOrgDoctorSummary{OK: 1, Warn: 1, Fail: 1, Info: 1, Total: 4}) {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}
	if !report.HasFailures() {
		t.Fatal("expected blocking failure to be reported")
	}
}

func TestRunPlatformOrgDoctorPropagatesSectionErrors(t *testing.T) {
	tests := []struct {
		name string
		mode platformOrgDoctorMode
		stub func()
	}{
		{
			name: "backend",
			mode: platformOrgDoctorMode{Name: "doctor", IncludeBackend: true},
			stub: func() {
				platformOrgBackendDoctorSectionFn = func(context.Context) (platformOrgDoctorSection, error) {
					return platformOrgDoctorSection{}, errors.New("backend boom")
				}
			},
		},
		{
			name: "state",
			mode: platformOrgDoctorMode{Name: "doctor", IncludeState: true},
			stub: func() {
				platformOrgStateDoctorSectionFn = func(context.Context, platformOrgDoctorMode) (platformOrgDoctorSection, error) {
					return platformOrgDoctorSection{}, errors.New("state boom")
				}
			},
		},
		{
			name: "runtime",
			mode: platformOrgDoctorMode{Name: "doctor", IncludeRuntime: true},
			stub: func() {
				platformOrgRuntimeDoctorSectionFn = func(context.Context, platformOrgDoctorMode) (platformOrgDoctorSection, error) {
					return platformOrgDoctorSection{}, errors.New("runtime boom")
				}
			},
		},
		{
			name: "inventory",
			mode: platformOrgDoctorMode{Name: "doctor", IncludeInventory: true},
			stub: func() {
				platformOrgInventoryDoctorSectionFn = func(context.Context) (platformOrgDoctorSection, error) {
					return platformOrgDoctorSection{}, errors.New("inventory boom")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			restoreDoctorTestHooks(t)
			tc.stub()
			_, err := runPlatformOrgDoctor(context.Background(), tc.mode)
			if err == nil || !strings.Contains(err.Error(), tc.name+" boom") {
				t.Fatalf("runPlatformOrgDoctor() error = %v", err)
			}
		})
	}
}

func TestDoctorCommandRunEJSON(t *testing.T) {
	restoreDoctorTestHooks(t)
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{Mode: "doctor", Summary: platformOrgDoctorSummary{OK: 1, Total: 1}}, nil
	}
	var out bytes.Buffer
	doctorCmd.SetOut(&out)
	doctorCmd.SetContext(context.Background())
	if err := doctorCmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json flag: %v", err)
	}

	if err := doctorCmd.RunE(doctorCmd, nil); err != nil {
		t.Fatalf("doctorCmd.RunE: %v", err)
	}

	var got PlatformOrgDoctorReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal json output: %v", err)
	}
	if got.Mode != "doctor" || got.Summary.OK != 1 {
		t.Fatalf("unexpected report: %+v", got)
	}
}

func TestDoctorCommandRunEPlainAndFailure(t *testing.T) {
	tests := []struct {
		name    string
		report  PlatformOrgDoctorReport
		wantErr string
	}{
		{
			name: "success",
			report: PlatformOrgDoctorReport{
				Mode:     "doctor",
				Sections: []platformOrgDoctorSection{{Title: "Backend Contract", Checks: []platformOrgDoctorCheck{{Status: "ok", Title: "ok", Detail: "fine"}}}},
				Summary:  platformOrgDoctorSummary{OK: 1, Total: 1},
			},
		},
		{
			name: "blocking failure",
			report: PlatformOrgDoctorReport{
				Mode:     "doctor",
				Sections: []platformOrgDoctorSection{{Title: "Backend Contract", Checks: []platformOrgDoctorCheck{{Status: "fail", Title: "broken", Detail: "nope", Blocking: true}}}},
				Summary:  platformOrgDoctorSummary{Fail: 1, Total: 1},
			},
			wantErr: "doctor found 1 blocking integrity issue(s)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			restoreDoctorTestHooks(t)
			platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
				return tc.report, nil
			}
			d.env = testEnv
			d.org = "ffreis"
			d.accountID = testAccountID
			d.region = testRegion
			d.ui = nil
			var out bytes.Buffer
			doctorCmd.SetOut(&out)
			doctorCmd.SetContext(context.Background())

			err := doctorCmd.RunE(doctorCmd, nil)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("doctorCmd.RunE: %v", err)
				}
				got := out.String()
				if !strings.Contains(got, "Platform Org Doctor") || !strings.Contains(got, "Integrity Summary: ok=1") {
					t.Fatalf("unexpected output: %q", got)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("doctorCmd.RunE() error = %v", err)
			}
		})
	}
}

func TestDoctorCommandRunEPropagatesRunnerError(t *testing.T) {
	restoreDoctorTestHooks(t)
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{}, errors.New("runner failed")
	}
	doctorCmd.SetContext(context.Background())
	err := doctorCmd.RunE(doctorCmd, nil)
	if err == nil || err.Error() != "runner failed" {
		t.Fatalf("doctorCmd.RunE() error = %v", err)
	}
}

func TestPlatformOrgBackendDoctorSectionHealthy(t *testing.T) {
	restoreDoctorTestHooks(t)
	d.env = testEnv
	d.org = "ffreis"
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	writeFile(t, filepath.Join(stack, "backend.local.hcl"), "bucket = \"ffreis-tf-state-root\"\ndynamodb_table = \"ffreis-tf-locks-root\"\nregion = \"us-east-1\"\n")
	writeFile(t, filepath.Join(root, envsDirName, testEnv, "backend.hcl"), "key = \"platform-org/prod/terraform.tfstate\"\n")
	withWorkingDir(t, root)
	loadBackendStateConfigForNukeFn = func(string, string) (nukeBackendStateConfig, error) {
		return nukeBackendStateConfig{BucketName: "ffreis-tf-state-root", TableName: "ffreis-tf-locks-root", StateKey: "platform-org/prod/terraform.tfstate"}, nil
	}
	s3BucketExistsFn = func(context.Context, string) (bool, error) { return true, nil }
	dynamoTableExistsFn = func(context.Context, string) (bool, error) { return true, nil }
	newNukeBackendResetS3ClientFn = func(sdkaws.Config) nukeBackendResetS3API {
		return &mockNukeResetS3API{
			headFn: func(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
				return &s3.HeadBucketOutput{}, nil
			},
			listFn: func(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
				return &s3.ListObjectVersionsOutput{Versions: []s3types.ObjectVersion{{Key: sdkaws.String("platform-org/prod/terraform.tfstate"), VersionId: sdkaws.String("v1")}}, IsTruncated: sdkaws.Bool(false)}, nil
			},
		}
	}
	newNukeBackendResetDynamoClientFn = func(sdkaws.Config) nukeBackendResetDynamoAPI {
		return &mockNukeResetDynamoAPI{
			describeFn: func(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
				return &dynamodb.DescribeTableOutput{}, nil
			},
			scanFn: func(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
				return &dynamodb.ScanOutput{Items: []map[string]dbtypes.AttributeValue{{"LockID": &dbtypes.AttributeValueMemberS{Value: "other-root/different/key.tfstate"}}}}, nil
			},
		}
	}

	section, err := platformOrgBackendDoctorSection(context.Background())
	if err != nil {
		t.Fatalf("platformOrgBackendDoctorSection: %v", err)
	}
	if section.Title != "Backend Contract" || len(section.Checks) < 6 {
		t.Fatalf("unexpected section: %+v", section)
	}
	if section.Checks[2].Status != "ok" {
		t.Fatalf("expected backend identity ok, got %+v", section.Checks[2])
	}
	if section.Checks[len(section.Checks)-1].Status != "warn" {
		t.Fatalf("expected lock rows warning, got %+v", section.Checks[len(section.Checks)-1])
	}
}

func TestPlatformOrgBackendDoctorSectionIdentityMismatchAndMissingInfra(t *testing.T) {
	restoreDoctorTestHooks(t)
	d.env = testEnv
	d.org = "ffreis"
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	writeFile(t, filepath.Join(stack, "backend.local.hcl"), "bucket = \"other-root\"\ndynamodb_table = \"other-locks\"\nregion = \"us-east-1\"\n")
	writeFile(t, filepath.Join(root, envsDirName, testEnv, "backend.hcl"), "key = \"different/key.tfstate\"\n")
	withWorkingDir(t, root)
	loadBackendStateConfigForNukeFn = func(string, string) (nukeBackendStateConfig, error) {
		return nukeBackendStateConfig{BucketName: "other-root", TableName: "other-locks", StateKey: "different/key.tfstate"}, nil
	}
	s3BucketExistsFn = func(context.Context, string) (bool, error) { return false, nil }
	dynamoTableExistsFn = func(context.Context, string) (bool, error) { return false, nil }
	newNukeBackendResetS3ClientFn = func(sdkaws.Config) nukeBackendResetS3API {
		return &mockNukeResetS3API{
			headFn: func(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
				return &s3.HeadBucketOutput{}, nil
			},
			listFn: func(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
				return &s3.ListObjectVersionsOutput{IsTruncated: sdkaws.Bool(false)}, nil
			},
		}
	}
	newNukeBackendResetDynamoClientFn = func(sdkaws.Config) nukeBackendResetDynamoAPI {
		return &mockNukeResetDynamoAPI{
			describeFn: func(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
				return &dynamodb.DescribeTableOutput{}, nil
			},
			scanFn: func(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
				return &dynamodb.ScanOutput{Items: []map[string]dbtypes.AttributeValue{{"LockID": &dbtypes.AttributeValueMemberS{Value: "lock-1"}}}}, nil
			},
		}
	}

	section, err := platformOrgBackendDoctorSection(context.Background())
	if err != nil {
		t.Fatalf("platformOrgBackendDoctorSection: %v", err)
	}
	if section.Checks[2].Status != "fail" || section.Checks[3].Status != "fail" || section.Checks[4].Status != "fail" {
		t.Fatalf("expected failing identity/bucket/table checks, got %+v", section.Checks)
	}
	if got := section.Checks[len(section.Checks)-1].Status; got != "ok" && got != "warn" && got != "fail" {
		t.Fatalf("unexpected lock-rows status %q in %+v", got, section.Checks[len(section.Checks)-1])
	}
}

func TestPlatformOrgStateDoctorSectionSuccessAndLiveScanFailure(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		restoreDoctorTestHooks(t)
		d.env = testEnv
		root := t.TempDir()
		initRepoLayout(t, root, testEnv)
		withWorkingDir(t, root)
		loadBackendStateConfigForNukeFn = func(string, string) (nukeBackendStateConfig, error) {
			return nukeBackendStateConfig{BucketName: "bucket", TableName: "table", StateKey: testDoctorStateKey}, nil
		}
		terraformPlanJSONFn = func(context.Context) ([]byte, error) { return testDoctorPlanJSON(), nil }
		loadPlatformOrgStateObjectsFn = func(context.Context, nukeBackendStateConfig) ([]terraformStateObject, error) {
			return []terraformStateObject{{Address: "aws_s3_bucket.state", Type: "aws_s3_bucket", Name: testDoctorCommandBucketName, ARN: "arn:aws:s3:::" + testDoctorCommandBucketName}}, nil
		}
		platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
			return []auditResource{{resourceType: "s3", name: testDoctorCommandBucketName, arn: "arn:aws:s3:::" + testDoctorCommandBucketName, stack: platformOrgStackTag, status: "OK"}}, nil
		}

		section, err := platformOrgStateDoctorSection(context.Background(), platformOrgDoctorMode{Name: "doctor"})
		if err != nil {
			t.Fatalf("platformOrgStateDoctorSection: %v", err)
		}
		if section.Title != tfStateIntegrityTitle || len(section.Checks) < 2 {
			t.Fatalf("unexpected section: %+v", section)
		}
		if section.Checks[0].Status != "ok" {
			t.Fatalf("expected summary check ok, got %+v", section.Checks[0])
		}
	})

	t.Run("live scan failure", func(t *testing.T) {
		restoreDoctorTestHooks(t)
		d.env = testEnv
		root := t.TempDir()
		initRepoLayout(t, root, testEnv)
		withWorkingDir(t, root)
		loadBackendStateConfigForNukeFn = func(string, string) (nukeBackendStateConfig, error) {
			return nukeBackendStateConfig{BucketName: "bucket", TableName: "table", StateKey: testDoctorStateKey}, nil
		}
		terraformPlanJSONFn = func(context.Context) ([]byte, error) { return testDoctorPlanJSON(), nil }
		loadPlatformOrgStateObjectsFn = func(context.Context, nukeBackendStateConfig) ([]terraformStateObject, error) {
			return []terraformStateObject{}, nil
		}
		platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
			return nil, errors.New("live scan failed")
		}

		section, err := platformOrgStateDoctorSection(context.Background(), platformOrgDoctorMode{Name: "doctor"})
		if err != nil {
			t.Fatalf("platformOrgStateDoctorSection: %v", err)
		}
		if len(section.Checks) != 1 || section.Checks[0].Status != "fail" || !strings.Contains(section.Checks[0].Detail, "live scan failed") {
			t.Fatalf("unexpected section checks: %+v", section.Checks)
		}
	})
}

func TestPlatformOrgStateDoctorSectionPlanError(t *testing.T) {
	restoreDoctorTestHooks(t)
	d.env = testEnv
	root := t.TempDir()
	initRepoLayout(t, root, testEnv)
	withWorkingDir(t, root)
	loadBackendStateConfigForNukeFn = func(string, string) (nukeBackendStateConfig, error) {
		return nukeBackendStateConfig{BucketName: "bucket", TableName: "table", StateKey: testDoctorStateKey}, nil
	}
	terraformPlanJSONFn = func(context.Context) ([]byte, error) { return nil, errors.New("plan failed") }

	_, err := platformOrgStateDoctorSection(context.Background(), platformOrgDoctorMode{Name: "doctor"})
	if err == nil || !strings.Contains(err.Error(), "terraform plan inventory: plan failed") {
		t.Fatalf("platformOrgStateDoctorSection() error = %v", err)
	}
}

func TestPlatformOrgRuntimeDoctorSectionSuccess(t *testing.T) {
	restoreDoctorTestHooks(t)
	d.org = "ffreis"
	d.accountID = testAccountID
	d.region = testRegion
	activationScheduleFn = func(context.Context, string) (*activationScheduleDetails, error) {
		return perfectSchedule("ffreis", "arn:aws:lambda:us-east-1:123456789012:function:ffreis-activate-cost-tags", "arn:aws:iam::123456789012:role/ffreis-scheduler-invoke-activate"), nil
	}
	getInlineRolePolicyDocumentFn = func(_ context.Context, roleName, policyName string) (string, bool, error) {
		switch policyName {
		case "invoke-activate-lambda":
			return `{"Resource":"arn:aws:lambda:us-east-1:123456789012:function:ffreis-activate-cost-tags"}`, true, nil
		case "activate-cost-tags":
			return `{"Resource":["arn:aws:sns:us-east-1:123456789012:ffreis-platform-events","arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda/ffreis-activate-cost-tags:*"]}`, true, nil
		default:
			return "", false, nil
		}
	}
	getLambdaFunctionFn = func(context.Context, string) (*lambda.GetFunctionOutput, error) {
		return &lambda.GetFunctionOutput{Configuration: &lambdatypes.FunctionConfiguration{Environment: &lambdatypes.EnvironmentResponse{Variables: map[string]string{"PLATFORM_EVENTS_TOPIC_ARN": "arn:aws:sns:us-east-1:123456789012:ffreis-platform-events"}}}}, nil
	}
	logGroupExistsFn = func(context.Context, string) (bool, error) { return true, nil }

	section, err := platformOrgRuntimeDoctorSection(context.Background(), platformOrgDoctorMode{Name: "doctor"})
	if err != nil {
		t.Fatalf("platformOrgRuntimeDoctorSection: %v", err)
	}
	if section.Title != "Runtime Wiring" || len(section.Checks) != 5 {
		t.Fatalf("unexpected section: %+v", section)
	}
	for _, check := range section.Checks {
		if check.Status != "ok" {
			t.Fatalf("expected all runtime checks ok, got %+v", section.Checks)
		}
	}
}

func TestPlatformOrgInventoryDoctorSectionUsesAuditSections(t *testing.T) {
	restoreDoctorTestHooks(t)
	scanResourcesFn = func(context.Context) ([]auditResource, error) { return []auditResource{}, nil }
	buildAuditSectionsFn = func(context.Context, []auditResource) (auditSections, error) {
		return auditSections{
			expected:     []auditResource{{source: "terraform", status: "WARN", address: "aws_s3_bucket.state"}},
			extra:        []auditResource{{resourceType: "s3", name: "extra-bucket"}},
			otherManaged: []auditResource{{stack: "bootstrap"}},
		}, nil
	}

	section, err := platformOrgInventoryDoctorSection(context.Background())
	if err != nil {
		t.Fatalf("platformOrgInventoryDoctorSection: %v", err)
	}
	if len(section.Checks) != 3 {
		t.Fatalf("expected 3 inventory checks, got %d", len(section.Checks))
	}
	if section.Checks[0].Status != "fail" || section.Checks[1].Status != "warn" || section.Checks[2].Status != "ok" {
		t.Fatalf("unexpected inventory statuses: %+v", section.Checks)
	}
}

func TestPlatformOrgInventoryDoctorSectionBuildError(t *testing.T) {
	restoreDoctorTestHooks(t)
	scanResourcesFn = func(context.Context) ([]auditResource, error) { return []auditResource{}, nil }
	buildAuditSectionsFn = func(context.Context, []auditResource) (auditSections, error) {
		return auditSections{}, errors.New("build failed")
	}

	_, err := platformOrgInventoryDoctorSection(context.Background())
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("platformOrgInventoryDoctorSection() error = %v", err)
	}
}

func TestGetInlineRolePolicyDocumentEdgeCases(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		restoreDoctorTestHooks(t)
		d.awsCfg = testIAMConfig(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, iamXMLError("NoSuchEntity", "not found"))
		})
		doc, exists, err := getInlineRolePolicyDocument(context.Background(), "role", "policy")
		if err != nil || exists || doc != "" {
			t.Fatalf("doc=%q exists=%v err=%v", doc, exists, err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		restoreDoctorTestHooks(t)
		d.awsCfg = testIAMConfig(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
			_, _ = io.WriteString(w, `<GetRolePolicyResponse><GetRolePolicyResult><PolicyDocument>%zz</PolicyDocument></GetRolePolicyResult></GetRolePolicyResponse>`)
		})
		_, _, err := getInlineRolePolicyDocument(context.Background(), "role", "policy")
		if err == nil || !strings.Contains(err.Error(), "decode policy role/policy") {
			t.Fatalf("getInlineRolePolicyDocument() error = %v", err)
		}
	})
}

func TestPlatformOrgDoctorStatusCellRichMode(t *testing.T) {
	restoreDoctorTestHooks(t)
	t.Setenv("NO_COLOR", "")
	ui, err := platformui.New(platformui.ModeRich)
	if err != nil {
		t.Fatalf("platformui.New: %v", err)
	}
	d.ui = ui
	for _, status := range []string{"ok", "warn", "fail", "info"} {
		if got := platformOrgDoctorStatusCell(status); got == "" {
			t.Fatalf("empty rich badge for %q", status)
		}
	}
}
