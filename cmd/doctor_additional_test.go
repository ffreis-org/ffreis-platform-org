package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
)

const testDoctorStateKey = "state.tfstate"

func testIAMConfig(t *testing.T, handler http.HandlerFunc) sdkaws.Config {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return sdkaws.Config{
		Region:       testRegion,
		Credentials:  credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
		BaseEndpoint: sdkaws.String(server.URL),
		HTTPClient:   server.Client(),
	}
}

func TestRunPlatformOrgDoctorEmptyMode(t *testing.T) {
	report, err := runPlatformOrgDoctor(context.Background(), platformOrgDoctorMode{Name: "unit"})
	if err != nil {
		t.Fatalf("runPlatformOrgDoctor: %v", err)
	}
	if report.Mode != "unit" {
		t.Fatalf("Mode = %q, want %q", report.Mode, "unit")
	}
	if len(report.Sections) != 0 {
		t.Fatalf("expected no sections, got %d", len(report.Sections))
	}
	if report.Summary != (platformOrgDoctorSummary{}) {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}
}

func TestPlatformOrgBackendDoctorSectionBackendConfigFailure(t *testing.T) {
	oldD := d
	oldLoad := loadBackendStateConfigForNukeFn
	t.Cleanup(func() {
		d = oldD
		loadBackendStateConfigForNukeFn = oldLoad
	})

	d.env = testEnv
	d.org = "ffreis"

	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	writeFile(t, filepath.Join(stack, "backend.local.hcl"), "bucket = \"ffreis-tf-state-root\"\ndynamodb_table = \"ffreis-tf-locks-root\"\nregion = \"us-east-1\"\n")
	writeFile(t, filepath.Join(root, envsDirName, testEnv, "backend.hcl"), "key = \"platform-org/prod/terraform.tfstate\"\n")
	withWorkingDir(t, root)

	loadBackendStateConfigForNukeFn = func(root, env string) (nukeBackendStateConfig, error) {
		return nukeBackendStateConfig{}, errors.New("ambiguous backend config")
	}

	section, err := platformOrgBackendDoctorSection(context.Background())
	if err != nil {
		t.Fatalf("platformOrgBackendDoctorSection: %v", err)
	}
	if section.Title != "Backend Contract" {
		t.Fatalf("Title = %q, want %q", section.Title, "Backend Contract")
	}
	if len(section.Checks) < 3 {
		t.Fatalf("expected backend checks plus contract failure, got %d checks", len(section.Checks))
	}
	last := section.Checks[len(section.Checks)-1]
	if last.Key != "backend.contract" || last.Status != "fail" || !last.Blocking {
		t.Fatalf("unexpected contract check: %+v", last)
	}
	if !strings.Contains(last.Detail, "ambiguous backend config") {
		t.Fatalf("unexpected contract detail: %q", last.Detail)
	}
}

func TestPlatformOrgStateDoctorSectionBackendConfigFailure(t *testing.T) {
	oldD := d
	oldLoad := loadBackendStateConfigForNukeFn
	t.Cleanup(func() {
		d = oldD
		loadBackendStateConfigForNukeFn = oldLoad
	})

	d.env = testEnv
	root := t.TempDir()
	initRepoLayout(t, root, testEnv)
	withWorkingDir(t, root)

	loadBackendStateConfigForNukeFn = func(root, env string) (nukeBackendStateConfig, error) {
		return nukeBackendStateConfig{}, errors.New("backend unavailable")
	}

	section, err := platformOrgStateDoctorSection(context.Background(), platformOrgDoctorMode{Name: "doctor"})
	if err != nil {
		t.Fatalf("platformOrgStateDoctorSection: %v", err)
	}
	if section.Title != tfStateIntegrityTitle {
		t.Fatalf("Title = %q, want %q", section.Title, tfStateIntegrityTitle)
	}
	if len(section.Checks) != 1 {
		t.Fatalf("expected exactly one failure check, got %d", len(section.Checks))
	}
	check := section.Checks[0]
	if check.Key != "state.backend-config" || check.Status != "fail" || !check.Blocking {
		t.Fatalf("unexpected state check: %+v", check)
	}
}

func TestPlatformOrgRuntimeDoctorSectionWrapsScheduleError(t *testing.T) {
	oldD := d
	oldGetSchedule := getScheduleFn
	t.Cleanup(func() {
		d = oldD
		getScheduleFn = oldGetSchedule
	})

	d.org = "ffreis"
	d.accountID = testAccountID
	d.region = testRegion

	getScheduleFn = func(_ context.Context, _ *scheduler.GetScheduleInput) (*scheduler.GetScheduleOutput, error) {
		return nil, errors.New("scheduler down")
	}

	_, err := platformOrgRuntimeDoctorSection(context.Background(), platformOrgDoctorMode{Name: "doctor"})
	if err == nil {
		t.Fatal("expected error from schedule lookup")
	}
	if !strings.Contains(err.Error(), "inspect activation schedule: scheduler down") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPlatformOrgInventoryDoctorSectionReturnsScanError(t *testing.T) {
	oldScan := scanResourcesFn
	t.Cleanup(func() { scanResourcesFn = oldScan })

	scanResourcesFn = func(context.Context) ([]auditResource, error) {
		return nil, errors.New("scan failed")
	}

	_, err := platformOrgInventoryDoctorSection(context.Background())
	if err == nil {
		t.Fatal("expected scan error")
	}
	if !strings.Contains(err.Error(), "scan failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadPlatformOrgStateObjectsParsesManagedResources(t *testing.T) {
	oldD := d
	oldS3 := newNukeBackendResetS3ClientFn
	t.Cleanup(func() {
		d = oldD
		newNukeBackendResetS3ClientFn = oldS3
	})

	newNukeBackendResetS3ClientFn = func(_ sdkaws.Config) nukeBackendResetS3API {
		return &mockNukeResetS3API{
			getFn: func(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
				if got := sdkaws.ToString(input.Bucket); got != "bucket" {
					return nil, fmt.Errorf("bucket = %q", got)
				}
				if got := sdkaws.ToString(input.Key); got != testDoctorStateKey {
					return nil, fmt.Errorf("key = %q", got)
				}
				body := `{"resources":[{"module":"module.runtime","mode":"managed","type":"aws_lambda_function","name":"activate","instances":[{"index_key":"primary","attributes":{"function_name":"ffreis-activate-cost-tags","arn":"arn:aws:lambda:us-east-1:123:function:ffreis-activate-cost-tags"}}]},{"mode":"managed","type":"aws_s3_bucket","name":"state","instances":[{"attributes":{"bucket":"ffreis-tf-state-root","arn":"arn:aws:s3:::ffreis-tf-state-root"}}]},{"mode":"data","type":"aws_caller_identity","name":"current","instances":[{"attributes":{"id":"123456789012"}}]}]}`
				return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body))}, nil
			},
		}
	}

	objects, err := loadPlatformOrgStateObjects(context.Background(), nukeBackendStateConfig{BucketName: "bucket", StateKey: testDoctorStateKey})
	if err != nil {
		t.Fatalf("loadPlatformOrgStateObjects: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("expected 2 managed objects, got %d", len(objects))
	}
	if objects[0].Address != `module.runtime.aws_lambda_function.activate["primary"]` {
		t.Fatalf("unexpected first address: %q", objects[0].Address)
	}
	if objects[1].Address != "aws_s3_bucket.state" {
		t.Fatalf("unexpected second address: %q", objects[1].Address)
	}
}

func TestLoadPlatformOrgStateObjectsReturnsNilForMissingState(t *testing.T) {
	oldD := d
	oldS3 := newNukeBackendResetS3ClientFn
	t.Cleanup(func() {
		d = oldD
		newNukeBackendResetS3ClientFn = oldS3
	})

	newNukeBackendResetS3ClientFn = func(_ sdkaws.Config) nukeBackendResetS3API {
		return &mockNukeResetS3API{
			getFn: func(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
				return nil, errors.New("object not found")
			},
		}
	}

	objects, err := loadPlatformOrgStateObjects(context.Background(), nukeBackendStateConfig{BucketName: "bucket", StateKey: testDoctorStateKey})
	if err != nil {
		t.Fatalf("loadPlatformOrgStateObjects: %v", err)
	}
	if objects != nil {
		t.Fatalf("expected nil objects for missing state, got %+v", objects)
	}
}

func TestGetInlineRolePolicyDocumentReturnsDecodedPolicy(t *testing.T) {
	oldD := d
	t.Cleanup(func() { d = oldD })

	policyDoc := `{"Version":"2012-10-17","Statement":[]}`
	d.awsCfg = testIAMConfig(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.FormValue("Action") != "GetRolePolicy" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `<GetRolePolicyResponse>
  <GetRolePolicyResult>
    <RoleName>%s</RoleName>
    <PolicyName>%s</PolicyName>
    <PolicyDocument>%s</PolicyDocument>
  </GetRolePolicyResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</GetRolePolicyResponse>`, r.FormValue("RoleName"), r.FormValue("PolicyName"), url.QueryEscape(policyDoc))
	})

	doc, exists, err := getInlineRolePolicyDocument(context.Background(), "my-role", "inline-policy")
	if err != nil {
		t.Fatalf("getInlineRolePolicyDocument: %v", err)
	}
	if !exists {
		t.Fatal("expected policy to exist")
	}
	if doc != policyDoc {
		t.Fatalf("decoded doc = %q, want %q", doc, policyDoc)
	}
}

func TestPrintPlatformOrgDoctorReportAndSummary(t *testing.T) {
	oldD := d
	t.Cleanup(func() { d = oldD })
	d.ui = nil

	var stdout bytes.Buffer
	out := newWriterOutput(&stdout, io.Discard, nil)
	report := PlatformOrgDoctorReport{
		Sections: []platformOrgDoctorSection{{
			Title: "Backend Contract",
			Checks: []platformOrgDoctorCheck{{
				Status: "ok",
				Title:  "backend points at the current org and env",
				Detail: "bucket=ffreis-tf-state-root",
			}},
		}},
		Summary: platformOrgDoctorSummary{OK: 1, Total: 1},
	}

	printPlatformOrgDoctorReport(out, report)
	printPlatformOrgDoctorSummary(out, report)

	got := stdout.String()
	if !strings.Contains(got, "Backend Contract") {
		t.Fatalf("expected section title in output, got %q", got)
	}
	if !strings.Contains(got, "STATUS") || !strings.Contains(got, "CHECK") || !strings.Contains(got, "HINT") {
		t.Fatalf("expected report headers in output, got %q", got)
	}
	if !strings.Contains(got, "bucket=ffreis-tf-state-root") {
		t.Fatalf("expected detail in output, got %q", got)
	}
	if !strings.Contains(got, "-") {
		t.Fatalf("expected missing hint placeholder in output, got %q", got)
	}
	if !strings.Contains(got, "Integrity Summary: ok=1  warn=0  fail=0  info=0") {
		t.Fatalf("expected summary line in output, got %q", got)
	}
}
