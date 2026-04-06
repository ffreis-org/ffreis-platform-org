package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
)

const (
	testDoctorBucketName         = "my-bucket"
	testDoctorBucketBAddress     = "aws_s3_bucket.b"
	testDoctorExistingBucketName = "existing-bucket"
	testDoctorExistingBucketARN  = "arn:aws:s3:::" + testDoctorExistingBucketName
	testDoctorNotExistsStrict    = "not exists strict"
	testDoctorBlockingErrorf     = "Blocking=%v want %v"
	testDoctorLambdaARN          = "arn:aws:lambda:us-east-1:123:function:myorg-activate-cost-tags"
	testDoctorPlatformEventsARN  = "arn:aws:sns:us-east-1:123:myorg-platform-events"
	testDoctorStatusDetailErrorf = "Status=%q want %q (detail=%s)"
)

// ---------------------------------------------------------------------------
// checkBackendLocalFile
// ---------------------------------------------------------------------------

func TestDoctorCheckBackendLocalFile(t *testing.T) {
	tests := []struct {
		name      string
		local     map[string]string
		localErr  error
		localPath string
		wantStat  string
		blocking  bool
	}{
		{
			name:     "error reading file",
			localErr: errors.New("file not found"),
			wantStat: "fail",
			blocking: true,
		},
		{
			name:     "missing all keys",
			local:    map[string]string{},
			wantStat: "fail",
			blocking: true,
		},
		{
			name:     "missing bucket only",
			local:    map[string]string{"dynamodb_table": "t", "region": "us-east-1"},
			wantStat: "fail",
			blocking: true,
		},
		{
			name:     "whitespace value counts as missing",
			local:    map[string]string{"bucket": "  ", "dynamodb_table": "t", "region": "us-east-1"},
			wantStat: "fail",
			blocking: true,
		},
		{
			name:     "complete config",
			local:    map[string]string{"bucket": "b", "dynamodb_table": "t", "region": "us-east-1"},
			wantStat: "ok",
			blocking: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkBackendLocalFile(tc.local, tc.localErr, "/path/backend.local.hcl")
			if got.Status != tc.wantStat {
				t.Errorf("Status=%q want %q", got.Status, tc.wantStat)
			}
			if got.Blocking != tc.blocking {
				t.Errorf(testDoctorBlockingErrorf, got.Blocking, tc.blocking)
			}
			if tc.localErr != nil && got.Detail != tc.localErr.Error() {
				t.Errorf("Detail=%q want error message", got.Detail)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkBackendEnvFile
// ---------------------------------------------------------------------------

func TestDoctorCheckBackendEnvFile(t *testing.T) {
	tests := []struct {
		name     string
		envCfg   map[string]string
		envErr   error
		env      string
		wantStat string
		blocking bool
	}{
		{
			name:     "error reading file",
			envErr:   errors.New("permission denied"),
			wantStat: "fail",
			blocking: true,
		},
		{
			name:     "missing key",
			envCfg:   map[string]string{},
			wantStat: "fail",
			blocking: true,
		},
		{
			name:     "whitespace key counts as missing",
			envCfg:   map[string]string{"key": "\t"},
			wantStat: "fail",
			blocking: true,
		},
		{
			name:     "complete config",
			envCfg:   map[string]string{"key": "platform-org/staging/terraform.tfstate"},
			wantStat: "ok",
			blocking: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkBackendEnvFile(tc.envCfg, tc.envErr, "/path/backend.hcl", tc.env)
			if got.Status != tc.wantStat {
				t.Errorf("Status=%q want %q", got.Status, tc.wantStat)
			}
			if got.Blocking != tc.blocking {
				t.Errorf(testDoctorBlockingErrorf, got.Blocking, tc.blocking)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkBackendStateObject
// ---------------------------------------------------------------------------

func TestDoctorCheckBackendStateObject(t *testing.T) {
	cfg := nukeBackendStateConfig{BucketName: "my-bucket", TableName: "my-table", StateKey: "platform-org/prod/terraform.tfstate"}

	tests := []struct {
		name     string
		count    int
		stateErr error
		wantStat string
		blocking bool
	}{
		{"error", 0, errors.New("s3 error"), "fail", true},
		{"zero versions", 0, nil, "info", false},
		{"one version", 1, nil, "ok", false},
		{"many versions", 5, nil, "ok", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkBackendStateObject(tc.count, tc.stateErr, cfg)
			if got.Status != tc.wantStat {
				t.Errorf("Status=%q want %q", got.Status, tc.wantStat)
			}
			if got.Blocking != tc.blocking {
				t.Errorf(testDoctorBlockingErrorf, got.Blocking, tc.blocking)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkBackendLockRows
// ---------------------------------------------------------------------------

func TestDoctorCheckBackendLockRows(t *testing.T) {
	cfg := nukeBackendStateConfig{BucketName: "b", TableName: "t", StateKey: "k"}

	tests := []struct {
		name       string
		lockCount  int
		stateCount int
		lockErr    error
		wantStat   string
		blocking   bool
	}{
		{"error", 0, 0, errors.New("dynamo error"), "fail", true},
		{"orphaned lock (lock > 0, state == 0)", 1, 0, nil, "fail", true},
		{"state present but no lock", 0, 1, nil, "warn", false},
		{"both present", 1, 1, nil, "warn", false},
		{"both zero", 0, 0, nil, "ok", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkBackendLockRows(tc.lockCount, tc.stateCount, tc.lockErr, cfg)
			if got.Status != tc.wantStat {
				t.Errorf("Status=%q want %q", got.Status, tc.wantStat)
			}
			if got.Blocking != tc.blocking {
				t.Errorf(testDoctorBlockingErrorf, got.Blocking, tc.blocking)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkRuntimeActivationSchedule
// ---------------------------------------------------------------------------

func perfectSchedule(org, lambdaARN, roleARN string) *activationScheduleDetails {
	return &activationScheduleDetails{
		GroupName:             activationScheduleGroupName(org),
		TargetARN:             lambdaARN,
		TargetRoleARN:         roleARN,
		State:                 schedulertypes.ScheduleStateEnabled,
		ActionAfterCompletion: schedulertypes.ActionAfterCompletionDelete,
	}
}

func TestDoctorCheckRuntimeActivationSchedule(t *testing.T) {
	org := "myorg"
	lambdaARN := testDoctorLambdaARN
	roleARN := "arn:aws:iam::123:role/myorg-scheduler-invoke-activate"

	tests := []struct {
		name     string
		schedule *activationScheduleDetails
		wantStat string
		blocking bool
	}{
		{"nil schedule", nil, "info", false},
		{"perfect schedule", perfectSchedule(org, lambdaARN, roleARN), "ok", false},
		{
			"wrong group",
			func() *activationScheduleDetails {
				s := perfectSchedule(org, lambdaARN, roleARN)
				s.GroupName = "wrong-group"
				return s
			}(),
			"fail", true,
		},
		{
			"wrong target ARN",
			func() *activationScheduleDetails {
				s := perfectSchedule(org, lambdaARN, roleARN)
				s.TargetARN = "arn:aws:lambda:us-east-1:123:function:other"
				return s
			}(),
			"fail", true,
		},
		{
			"wrong role ARN",
			func() *activationScheduleDetails {
				s := perfectSchedule(org, lambdaARN, roleARN)
				s.TargetRoleARN = "arn:aws:iam::123:role/wrong-role"
				return s
			}(),
			"fail", true,
		},
		{
			"disabled state",
			func() *activationScheduleDetails {
				s := perfectSchedule(org, lambdaARN, roleARN)
				s.State = schedulertypes.ScheduleStateDisabled
				return s
			}(),
			"fail", true,
		},
		{
			"wrong ActionAfterCompletion",
			func() *activationScheduleDetails {
				s := perfectSchedule(org, lambdaARN, roleARN)
				s.ActionAfterCompletion = schedulertypes.ActionAfterCompletionNone
				return s
			}(),
			"fail", true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkRuntimeActivationSchedule(tc.schedule, org, lambdaARN, roleARN)
			if got.Status != tc.wantStat {
				t.Errorf(testDoctorStatusDetailErrorf, got.Status, tc.wantStat, got.Detail)
			}
			if got.Blocking != tc.blocking {
				t.Errorf(testDoctorBlockingErrorf, got.Blocking, tc.blocking)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkRuntimeSchedulerRolePolicy
// ---------------------------------------------------------------------------

func TestDoctorCheckRuntimeSchedulerRolePolicy(t *testing.T) {
	lambdaARN := testDoctorLambdaARN
	modeAllowMissing := platformOrgDoctorMode{AllowMissingRuntime: true}
	modeStrict := platformOrgDoctorMode{AllowMissingRuntime: false}

	tests := []struct {
		name     string
		doc      string
		exists   bool
		err      error
		mode     platformOrgDoctorMode
		wantStat string
		blocking bool
	}{
		{"error", "", false, errors.New("iam error"), modeAllowMissing, "fail", true},
		{"not exists allow missing", "", false, nil, modeAllowMissing, "info", false},
		{testDoctorNotExistsStrict, "", false, nil, modeStrict, "fail", true},
		{"exists contains ARN", `{"Resource":"` + lambdaARN + `"}`, true, nil, modeAllowMissing, "ok", false},
		{"exists missing ARN", `{"Resource":"arn:aws:lambda:us-east-1:123:function:other"}`, true, nil, modeAllowMissing, "fail", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkRuntimeSchedulerRolePolicy(tc.doc, tc.exists, tc.err, tc.mode, "myorg-scheduler-invoke-activate", lambdaARN)
			if got.Status != tc.wantStat {
				t.Errorf(testDoctorStatusDetailErrorf, got.Status, tc.wantStat, got.Detail)
			}
			if got.Blocking != tc.blocking {
				t.Errorf(testDoctorBlockingErrorf, got.Blocking, tc.blocking)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkRuntimeLambdaRolePolicy
// ---------------------------------------------------------------------------

func TestDoctorCheckRuntimeLambdaRolePolicy(t *testing.T) {
	topicARN := testDoctorPlatformEventsARN
	logPattern := "arn:aws:logs:us-east-1:123:log-group:/aws/lambda/myorg-activate-cost-tags:*"
	modeAllowMissing := platformOrgDoctorMode{AllowMissingRuntime: true}
	modeStrict := platformOrgDoctorMode{AllowMissingRuntime: false}

	docBoth := fmt.Sprintf(`{"Resource":["%s","%s"]}`, topicARN, logPattern)
	docMissingLog := fmt.Sprintf(`{"Resource":"%s"}`, topicARN)
	docMissingTopic := fmt.Sprintf(`{"Resource":"%s"}`, logPattern)

	tests := []struct {
		name     string
		doc      string
		exists   bool
		err      error
		mode     platformOrgDoctorMode
		wantStat string
		blocking bool
	}{
		{"error", "", false, errors.New("iam error"), modeAllowMissing, "fail", true},
		{"not exists allow missing", "", false, nil, modeAllowMissing, "info", false},
		{testDoctorNotExistsStrict, "", false, nil, modeStrict, "fail", true},
		{"exists with both", docBoth, true, nil, modeAllowMissing, "ok", false},
		{"exists missing log pattern", docMissingLog, true, nil, modeAllowMissing, "fail", true},
		{"exists missing topic ARN", docMissingTopic, true, nil, modeAllowMissing, "fail", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkRuntimeLambdaRolePolicy(tc.doc, tc.exists, tc.err, tc.mode, "myorg-activate-cost-tags", topicARN, logPattern)
			if got.Status != tc.wantStat {
				t.Errorf(testDoctorStatusDetailErrorf, got.Status, tc.wantStat, got.Detail)
			}
			if got.Blocking != tc.blocking {
				t.Errorf(testDoctorBlockingErrorf, got.Blocking, tc.blocking)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkRuntimeLambdaEnvironment
// ---------------------------------------------------------------------------

func lambdaOutputWithEnv(vars map[string]string) *lambda.GetFunctionOutput {
	return &lambda.GetFunctionOutput{
		Configuration: &lambdatypes.FunctionConfiguration{
			Environment: &lambdatypes.EnvironmentResponse{
				Variables: vars,
			},
		},
	}
}

func TestDoctorCheckRuntimeLambdaEnvironment(t *testing.T) {
	topicARN := testDoctorPlatformEventsARN
	lambdaName := "myorg-activate-cost-tags"

	tests := []struct {
		name     string
		out      *lambda.GetFunctionOutput
		err      error
		wantStat string
		blocking bool
	}{
		{
			"not found error",
			nil,
			errors.New("function not found"),
			"info", false,
		},
		{
			"other error",
			nil,
			errors.New("access denied"),
			"fail", true,
		},
		{
			"correct topic ARN",
			lambdaOutputWithEnv(map[string]string{"PLATFORM_EVENTS_TOPIC_ARN": topicARN}),
			nil,
			"ok", false,
		},
		{
			"wrong topic ARN",
			lambdaOutputWithEnv(map[string]string{"PLATFORM_EVENTS_TOPIC_ARN": "arn:aws:sns:us-east-1:123:wrong"}),
			nil,
			"fail", true,
		},
		{
			"nil Configuration",
			&lambda.GetFunctionOutput{Configuration: nil},
			nil,
			"fail", true,
		},
		{
			"nil Environment",
			&lambda.GetFunctionOutput{
				Configuration: &lambdatypes.FunctionConfiguration{Environment: nil},
			},
			nil,
			"fail", true,
		},
		{
			"nil Variables",
			&lambda.GetFunctionOutput{
				Configuration: &lambdatypes.FunctionConfiguration{
					Environment: &lambdatypes.EnvironmentResponse{Variables: nil},
				},
			},
			nil,
			"fail", true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkRuntimeLambdaEnvironment(tc.out, tc.err, lambdaName, topicARN)
			if got.Status != tc.wantStat {
				t.Errorf(testDoctorStatusDetailErrorf, got.Status, tc.wantStat, got.Detail)
			}
			if got.Blocking != tc.blocking {
				t.Errorf(testDoctorBlockingErrorf, got.Blocking, tc.blocking)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkRuntimeLambdaLogGroup
// ---------------------------------------------------------------------------

func TestDoctorCheckRuntimeLambdaLogGroup(t *testing.T) {
	logGroup := "/aws/lambda/myorg-activate-cost-tags"
	modeAllowMissing := platformOrgDoctorMode{AllowMissingRuntime: true}
	modeStrict := platformOrgDoctorMode{AllowMissingRuntime: false}

	tests := []struct {
		name     string
		exists   bool
		err      error
		mode     platformOrgDoctorMode
		wantStat string
		blocking bool
	}{
		{"error", false, errors.New("logs error"), modeAllowMissing, "fail", true},
		{"exists", true, nil, modeAllowMissing, "ok", false},
		{"not exists allow missing", false, nil, modeAllowMissing, "info", false},
		{testDoctorNotExistsStrict, false, nil, modeStrict, "fail", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkRuntimeLambdaLogGroup(tc.exists, tc.err, tc.mode, logGroup)
			if got.Status != tc.wantStat {
				t.Errorf("Status=%q want %q", got.Status, tc.wantStat)
			}
			if got.Blocking != tc.blocking {
				t.Errorf(testDoctorBlockingErrorf, got.Blocking, tc.blocking)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// existsStatus
// ---------------------------------------------------------------------------

func TestDoctorExistsStatus(t *testing.T) {
	tests := []struct {
		name   string
		exists bool
		err    error
		want   string
	}{
		{"error", false, errors.New("err"), "fail"},
		{"not exists", false, nil, "fail"},
		{"exists", true, nil, "ok"},
		{"exists with error still fails", true, errors.New("err"), "fail"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := existsStatus(tc.exists, tc.err)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// existsDetail
// ---------------------------------------------------------------------------

func TestDoctorExistsDetail(t *testing.T) {
	tests := []struct {
		name    string
		exists  bool
		err     error
		label   string
		resname string
		wantSub string
	}{
		{"error returns error message", false, errors.New("access denied"), "bucket", testDoctorBucketName, "access denied"},
		{"not exists returns missing", false, nil, "bucket", testDoctorBucketName, "is missing"},
		{"exists returns present", true, nil, "bucket", testDoctorBucketName, "is present"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := existsDetail(tc.exists, tc.err, tc.label, tc.resname)
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("got %q, want substring %q", got, tc.wantSub)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// failOrWarn
// ---------------------------------------------------------------------------

func TestDoctorFailOrWarn(t *testing.T) {
	if got := failOrWarn(true); got != "warn" {
		t.Errorf("failOrWarn(true)=%q want warn", got)
	}
	if got := failOrWarn(false); got != "fail" {
		t.Errorf("failOrWarn(false)=%q want fail", got)
	}
}

// ---------------------------------------------------------------------------
// missingBackendMapKeys
// ---------------------------------------------------------------------------

func TestDoctorMissingBackendMapKeys(t *testing.T) {
	tests := []struct {
		name     string
		values   map[string]string
		required []string
		wantLen  int
	}{
		{"all present", map[string]string{"a": "x", "b": "y"}, []string{"a", "b"}, 0},
		{"some missing", map[string]string{"a": "x"}, []string{"a", "b"}, 1},
		{"all missing", map[string]string{}, []string{"a", "b"}, 2},
		{"whitespace counts as missing", map[string]string{"a": "  ", "b": "y"}, []string{"a", "b"}, 1},
		{"empty string missing", map[string]string{"a": "", "b": "y"}, []string{"a", "b"}, 1},
		{"no required keys", map[string]string{"a": "x"}, []string{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := missingBackendMapKeys(tc.values, tc.required...)
			if len(got) != tc.wantLen {
				t.Errorf("len(missing)=%d want %d (got %v)", len(got), tc.wantLen, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// countByStack
// ---------------------------------------------------------------------------

func TestDoctorCountByStack(t *testing.T) {
	resources := []auditResource{
		{stack: "bootstrap"},
		{stack: "bootstrap"},
		{stack: "platform-org"},
		{stack: ""},
	}

	tests := []struct {
		stack string
		want  int
	}{
		{"bootstrap", 2},
		{"platform-org", 1},
		{"", 1},
		{"other", 0},
	}
	for _, tc := range tests {
		t.Run(tc.stack, func(t *testing.T) {
			got := countByStack(resources, tc.stack)
			if got != tc.want {
				t.Errorf("countByStack(%q)=%d want %d", tc.stack, got, tc.want)
			}
		})
	}

	if got := countByStack(nil, "bootstrap"); got != 0 {
		t.Errorf("countByStack(nil)=%d want 0", got)
	}
}

// ---------------------------------------------------------------------------
// summarizePlatformOrgDoctor
// ---------------------------------------------------------------------------

func TestDoctorSummarizePlatformOrgDoctor(t *testing.T) {
	t.Run("empty sections", func(t *testing.T) {
		got := summarizePlatformOrgDoctor(nil)
		if got.Total != 0 || got.OK != 0 || got.Warn != 0 || got.Fail != 0 || got.Info != 0 {
			t.Errorf("expected zero summary, got %+v", got)
		}
	})

	t.Run("mixed statuses", func(t *testing.T) {
		sections := []platformOrgDoctorSection{
			{
				Checks: []platformOrgDoctorCheck{
					{Status: "ok"},
					{Status: "ok"},
					{Status: "warn"},
					{Status: "fail"},
					{Status: "info"},
					{Status: "unknown"}, // doesn't match any bucket
				},
			},
			{
				Checks: []platformOrgDoctorCheck{
					{Status: "ok"},
					{Status: "fail"},
				},
			},
		}
		got := summarizePlatformOrgDoctor(sections)
		if got.Total != 8 {
			t.Errorf("Total=%d want 8", got.Total)
		}
		if got.OK != 3 {
			t.Errorf("OK=%d want 3", got.OK)
		}
		if got.Warn != 1 {
			t.Errorf("Warn=%d want 1", got.Warn)
		}
		if got.Fail != 2 {
			t.Errorf("Fail=%d want 2", got.Fail)
		}
		if got.Info != 1 {
			t.Errorf("Info=%d want 1", got.Info)
		}
	})
}

// ---------------------------------------------------------------------------
// buildStateLiveIndex
// ---------------------------------------------------------------------------

func TestDoctorBuildStateLiveIndex(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		byARN, byName := buildStateLiveIndex(nil)
		if len(byARN) != 0 || len(byName) != 0 {
			t.Errorf("expected empty maps")
		}
	})

	t.Run("arn only", func(t *testing.T) {
		resources := []auditResource{{arn: "arn:aws:s3:::my-bucket"}}
		byARN, byName := buildStateLiveIndex(resources)
		if _, ok := byARN["arn:aws:s3:::my-bucket"]; !ok {
			t.Errorf("expected arn index entry")
		}
		if len(byName) != 0 {
			t.Errorf("expected empty name index")
		}
	})

	t.Run("name only", func(t *testing.T) {
		resources := []auditResource{{name: "My-Bucket"}}
		byARN, byName := buildStateLiveIndex(resources)
		if len(byARN) != 0 {
			t.Errorf("expected empty arn index")
		}
		if _, ok := byName["my-bucket"]; !ok {
			t.Errorf("expected lowercased name index entry")
		}
	})

	t.Run("both arn and name", func(t *testing.T) {
		resources := []auditResource{{arn: "arn:aws:s3:::bkt", name: "bkt"}}
		byARN, byName := buildStateLiveIndex(resources)
		if _, ok := byARN["arn:aws:s3:::bkt"]; !ok {
			t.Errorf("expected arn index entry")
		}
		if _, ok := byName["bkt"]; !ok {
			t.Errorf("expected name index entry")
		}
	})

	t.Run("multiple resources with same name", func(t *testing.T) {
		resources := []auditResource{
			{name: "shared"},
			{name: "shared"},
		}
		_, byName := buildStateLiveIndex(resources)
		if len(byName["shared"]) != 2 {
			t.Errorf("expected 2 entries for same name, got %d", len(byName["shared"]))
		}
	})
}

// ---------------------------------------------------------------------------
// stateDoctorDriftChecks
// ---------------------------------------------------------------------------

type doctorStateDoctorDriftCheckCase struct {
	name            string
	mode            platformOrgDoctorMode
	expected        []expectedAuditResource
	stateByAddr     map[string]terraformStateObject
	liveByARN       map[string]auditResource
	liveByName      map[string][]auditResource
	wantCheckCount  int
	wantFirstKey    string
	wantFirstStatus string
	wantAddresses   map[string]bool
}

func assertDoctorStateDoctorDriftChecksCase(t *testing.T, tc doctorStateDoctorDriftCheckCase) {
	t.Helper()

	addrs, checks := stateDoctorDriftChecks(tc.mode, tc.expected, tc.stateByAddr, tc.liveByARN, tc.liveByName)
	if len(checks) != tc.wantCheckCount {
		t.Fatalf("expected %d check(s), got %d: %v", tc.wantCheckCount, len(checks), checks)
	}
	if tc.wantFirstKey != "" && checks[0].Key != tc.wantFirstKey {
		t.Errorf("unexpected key: %s", checks[0].Key)
	}
	if tc.wantFirstStatus != "" && checks[0].Status != tc.wantFirstStatus {
		t.Errorf("Status=%q want %s", checks[0].Status, tc.wantFirstStatus)
	}
	for addr, want := range tc.wantAddresses {
		if addrs[addr] != want {
			t.Errorf("address %q presence=%v want %v", addr, addrs[addr], want)
		}
	}
}

func TestDoctorStateDoctorDriftChecks(t *testing.T) {
	mode := platformOrgDoctorMode{LenientState: false}
	lenientMode := platformOrgDoctorMode{LenientState: true}

	liveByARN := map[string]auditResource{
		testDoctorExistingBucketARN: {arn: testDoctorExistingBucketARN, name: testDoctorExistingBucketName},
	}
	liveByName := map[string][]auditResource{
		testDoctorExistingBucketName: {{arn: testDoctorExistingBucketARN, name: testDoctorExistingBucketName}},
	}
	emptyARN := map[string]auditResource{}
	emptyName := map[string][]auditResource{}
	liveByNameLocal := map[string][]auditResource{
		"new-bucket": {{name: "new-bucket"}},
	}
	newARN := "arn:aws:s3:::new-bucket"
	liveByARNLocal := map[string]auditResource{
		newARN: {arn: newARN, name: "new-bucket"},
	}

	testCases := []doctorStateDoctorDriftCheckCase{
		{
			name:           "both missing - no check",
			mode:           mode,
			expected:       []expectedAuditResource{{address: "aws_s3_bucket.missing", status: "OK", name: "missing-bucket"}},
			stateByAddr:    map[string]terraformStateObject{},
			liveByARN:      emptyARN,
			liveByName:     emptyName,
			wantCheckCount: 0,
		},
		{
			name: "not in state, live present, status MISSING -> outside-state check",
			mode: mode,
			expected: []expectedAuditResource{{
				address: "aws_s3_bucket.mybucket", status: "MISSING",
				name: testDoctorExistingBucketName, arn: testDoctorExistingBucketARN,
			}},
			stateByAddr:     map[string]terraformStateObject{},
			liveByARN:       liveByARN,
			liveByName:      liveByName,
			wantCheckCount:  1,
			wantFirstKey:    "state.outside-state.aws_s3_bucket.mybucket",
			wantFirstStatus: "fail",
		},
		{
			name: "not in state, live present, status MISSING -> warn in lenient mode",
			mode: lenientMode,
			expected: []expectedAuditResource{{
				address: "aws_s3_bucket.mybucket", status: "MISSING",
				name: testDoctorExistingBucketName, arn: testDoctorExistingBucketARN,
			}},
			stateByAddr:     map[string]terraformStateObject{},
			liveByARN:       liveByARN,
			liveByName:      liveByName,
			wantCheckCount:  1,
			wantFirstStatus: "warn",
		},
		{
			name: "not in state, live present, not MISSING -> missing-address check",
			mode: mode,
			expected: []expectedAuditResource{{
				address: "aws_s3_bucket.mybucket", status: "OK",
				name: testDoctorExistingBucketName, arn: testDoctorExistingBucketARN,
			}},
			stateByAddr:    map[string]terraformStateObject{},
			liveByARN:      liveByARN,
			liveByName:     liveByName,
			wantCheckCount: 1,
			wantFirstKey:   "state.missing-address.aws_s3_bucket.mybucket",
		},
		{
			name:           "in state, not live, status OK -> deleted-live check",
			mode:           mode,
			expected:       []expectedAuditResource{{address: "aws_s3_bucket.gone", status: "OK", name: "gone-bucket"}},
			stateByAddr:    map[string]terraformStateObject{"aws_s3_bucket.gone": {Address: "aws_s3_bucket.gone", Name: "gone-bucket"}},
			liveByARN:      emptyARN,
			liveByName:     emptyName,
			wantCheckCount: 1,
			wantFirstKey:   "state.deleted-live.aws_s3_bucket.gone",
		},
		{
			name:           "stale name check",
			mode:           mode,
			expected:       []expectedAuditResource{{address: testDoctorBucketBAddress, status: "OK", name: "new-bucket"}},
			stateByAddr:    map[string]terraformStateObject{testDoctorBucketBAddress: {Address: testDoctorBucketBAddress, Name: "old-bucket"}},
			liveByARN:      emptyARN,
			liveByName:     liveByNameLocal,
			wantCheckCount: 1,
			wantFirstKey:   "state.stale-name.aws_s3_bucket.b",
		},
		{
			name:           "stale ARN check",
			mode:           mode,
			expected:       []expectedAuditResource{{address: testDoctorBucketBAddress, status: "OK", arn: newARN}},
			stateByAddr:    map[string]terraformStateObject{testDoctorBucketBAddress: {Address: testDoctorBucketBAddress, ARN: "arn:aws:s3:::old-bucket"}},
			liveByARN:      liveByARNLocal,
			liveByName:     emptyName,
			wantCheckCount: 1,
			wantFirstKey:   "state.stale-arn.aws_s3_bucket.b",
		},
		{
			name:        "returns expectedAddresses map",
			mode:        mode,
			expected:    []expectedAuditResource{{address: "aws_s3_bucket.a", status: "OK"}, {address: testDoctorBucketBAddress, status: "OK"}},
			stateByAddr: map[string]terraformStateObject{},
			liveByARN:   emptyARN,
			liveByName:  emptyName,
			wantAddresses: map[string]bool{
				"aws_s3_bucket.a":        true,
				testDoctorBucketBAddress: true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assertDoctorStateDoctorDriftChecksCase(t, tc)
		})
	}
}

// ---------------------------------------------------------------------------
// stateDoctorStaleAddressCheck
// ---------------------------------------------------------------------------

func TestDoctorStateDoctorStaleAddressCheck(t *testing.T) {
	t.Run("no stale addresses", func(t *testing.T) {
		stateByAddr := map[string]terraformStateObject{
			"aws_s3_bucket.a": {Address: "aws_s3_bucket.a", Type: "aws_s3_bucket"},
		}
		expected := map[string]bool{"aws_s3_bucket.a": true}
		got := stateDoctorStaleAddressCheck(stateByAddr, expected)
		if got.Status != "ok" {
			t.Errorf("Status=%q want ok", got.Status)
		}
	})

	t.Run("some stale addresses", func(t *testing.T) {
		stateByAddr := map[string]terraformStateObject{
			"aws_s3_bucket.a":        {Address: "aws_s3_bucket.a", Type: "aws_s3_bucket"},
			testDoctorBucketBAddress: {Address: testDoctorBucketBAddress, Type: "aws_s3_bucket"},
		}
		expected := map[string]bool{"aws_s3_bucket.a": true}
		got := stateDoctorStaleAddressCheck(stateByAddr, expected)
		if got.Status != "warn" {
			t.Errorf("Status=%q want warn", got.Status)
		}
		if got.Blocking {
			t.Errorf("expected non-blocking")
		}
	})

	t.Run("excluded type is ignored", func(t *testing.T) {
		// aws_s3_bucket_versioning is excluded by excludeTerraformAuditResource
		stateByAddr := map[string]terraformStateObject{
			"aws_s3_bucket_versioning.v": {Address: "aws_s3_bucket_versioning.v", Type: "aws_s3_bucket_versioning"},
		}
		expected := map[string]bool{}
		got := stateDoctorStaleAddressCheck(stateByAddr, expected)
		if got.Status != "ok" {
			t.Errorf("Status=%q want ok (excluded type should be ignored)", got.Status)
		}
	})
}

// ---------------------------------------------------------------------------
// terraformStateInstanceAddress
// ---------------------------------------------------------------------------

func TestDoctorTerraformStateInstanceAddress(t *testing.T) {
	tests := []struct {
		name     string
		resource terraformStateResource
		index    int
		instance terraformStateInstance
		want     string
	}{
		{
			"no module, no index_key",
			terraformStateResource{Type: "aws_s3_bucket", Name: "main"},
			0,
			terraformStateInstance{IndexKey: nil},
			"aws_s3_bucket.main",
		},
		{
			"with module",
			terraformStateResource{Module: "module.mymod", Type: "aws_s3_bucket", Name: "main"},
			0,
			terraformStateInstance{IndexKey: nil},
			"module.mymod.aws_s3_bucket.main",
		},
		{
			"string index_key",
			terraformStateResource{Type: "aws_s3_bucket", Name: "main"},
			0,
			terraformStateInstance{IndexKey: "mykey"},
			`aws_s3_bucket.main["mykey"]`,
		},
		{
			"float64 index_key",
			terraformStateResource{Type: "aws_s3_bucket", Name: "main"},
			2,
			terraformStateInstance{IndexKey: float64(3)},
			"aws_s3_bucket.main[3]",
		},
		{
			"other index_key falls back to index param",
			terraformStateResource{Type: "aws_s3_bucket", Name: "main"},
			7,
			terraformStateInstance{IndexKey: true},
			"aws_s3_bucket.main[7]",
		},
		{
			"string index with special chars",
			terraformStateResource{Type: "aws_iam_role", Name: "r"},
			0,
			terraformStateInstance{IndexKey: `has "quotes"`},
			`aws_iam_role.r["has \"quotes\""]`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := terraformStateInstanceAddress(tc.resource, tc.index, tc.instance)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// strconvQuoteString
// ---------------------------------------------------------------------------

func TestDoctorStrconvQuoteString(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", `"simple"`},
		{`has "quotes"`, `"has \"quotes\""`},
		{"with\nnewline", `"with\nnewline"`},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := strconvQuoteString(tc.input)
			if got != tc.want {
				t.Errorf("strconvQuoteString(%q)=%q want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkRuntimeLambdaEnvironment — edge: PLATFORM_EVENTS_TOPIC_ARN absent
// ---------------------------------------------------------------------------

func TestDoctorCheckRuntimeLambdaEnvironmentMissingKey(t *testing.T) {
	topicARN := testDoctorPlatformEventsARN
	// Variables map exists but key is absent → actualTopicARN will be ""
	out := lambdaOutputWithEnv(map[string]string{"OTHER_KEY": "val"})
	got := checkRuntimeLambdaEnvironment(out, nil, "fn-name", topicARN)
	if got.Status != "fail" {
		t.Errorf("Status=%q want fail", got.Status)
	}
}

// ---------------------------------------------------------------------------
// checkRuntimeActivationSchedule — detail contains "schedule present"
// ---------------------------------------------------------------------------

func TestDoctorCheckRuntimeActivationScheduleDetailPrefix(t *testing.T) {
	org := "myorg"
	lambdaARN := testDoctorLambdaARN
	roleARN := "arn:aws:iam::123:role/myorg-scheduler-invoke-activate"
	s := perfectSchedule(org, lambdaARN, roleARN)
	got := checkRuntimeActivationSchedule(s, org, lambdaARN, roleARN)
	if !strings.Contains(got.Detail, "schedule present") {
		t.Errorf("Detail should start with 'schedule present', got: %s", got.Detail)
	}
}

// ---------------------------------------------------------------------------
// checkRuntimeLambdaEnvironment — exact ARN mismatch detail
// ---------------------------------------------------------------------------

func TestDoctorCheckRuntimeLambdaEnvironmentMismatchDetail(t *testing.T) {
	topicARN := testDoctorPlatformEventsARN
	wrong := "arn:aws:sns:us-east-1:123:wrong-topic"
	out := lambdaOutputWithEnv(map[string]string{"PLATFORM_EVENTS_TOPIC_ARN": wrong})
	got := checkRuntimeLambdaEnvironment(out, nil, "fn", topicARN)
	if !strings.Contains(got.Detail, wrong) || !strings.Contains(got.Detail, topicARN) {
		t.Errorf("detail should mention both ARNs, got: %s", got.Detail)
	}
}

// ---------------------------------------------------------------------------
// buildStateLiveIndex — name is case-insensitive
// ---------------------------------------------------------------------------

func TestDoctorBuildStateLiveIndexCaseInsensitive(t *testing.T) {
	resources := []auditResource{
		{name: "MyBucket"},
		{name: "mybucket"},
	}
	_, byName := buildStateLiveIndex(resources)
	if len(byName["mybucket"]) != 2 {
		t.Errorf("expected 2 entries under 'mybucket', got %d", len(byName["mybucket"]))
	}
}

// ---------------------------------------------------------------------------
// summarizePlatformOrgDoctor — total is always sum of all statuses
// ---------------------------------------------------------------------------

func TestDoctorSummarizeTotalEquality(t *testing.T) {
	sections := []platformOrgDoctorSection{
		{
			Checks: []platformOrgDoctorCheck{
				{Status: "ok"}, {Status: "warn"}, {Status: "fail"}, {Status: "info"},
			},
		},
	}
	got := summarizePlatformOrgDoctor(sections)
	counted := got.OK + got.Warn + got.Fail + got.Info
	if got.Total != 4 {
		t.Errorf("Total=%d want 4", got.Total)
	}
	// "unknown" status contributes to Total but not to any named counter
	if counted != 4 {
		t.Errorf("sum of named counters=%d want 4", counted)
	}
}

// ---------------------------------------------------------------------------
// missingBackendMapKeys — tab counts as missing
// ---------------------------------------------------------------------------

func TestDoctorMissingBackendMapKeysTab(t *testing.T) {
	m := map[string]string{"key": "\t\t"}
	missing := missingBackendMapKeys(m, "key")
	if len(missing) != 1 {
		t.Errorf("expected tab value to be missing, got %v", missing)
	}
}

// ---------------------------------------------------------------------------
// checkBackendStateObject — related slice contains bucket/key path
// ---------------------------------------------------------------------------

func TestDoctorCheckBackendStateObjectRelated(t *testing.T) {
	cfg := nukeBackendStateConfig{BucketName: "mybucket", TableName: "mytable", StateKey: "mykey"}
	got := checkBackendStateObject(0, nil, cfg)
	found := false
	for _, r := range got.Related {
		if r == "mybucket/mykey" {
			found = true
		}
	}
	if !found {
		t.Errorf("related should contain 'mybucket/mykey', got %v", got.Related)
	}
}

// ---------------------------------------------------------------------------
// checkBackendLocalFile — related always contains localPath
// ---------------------------------------------------------------------------

func TestDoctorCheckBackendLocalFileRelatedPath(t *testing.T) {
	path := "/repo/terraform/stack/backend.local.hcl"
	got := checkBackendLocalFile(
		map[string]string{"bucket": "b", "dynamodb_table": "t", "region": "r"},
		nil, path,
	)
	if len(got.Related) == 0 || got.Related[0] != path {
		t.Errorf("related should contain path, got %v", got.Related)
	}
}

// ---------------------------------------------------------------------------
// checkRuntimeSchedulerRolePolicy — key and title constants
// ---------------------------------------------------------------------------

func TestDoctorCheckRuntimeSchedulerRolePolicyKey(t *testing.T) {
	mode := platformOrgDoctorMode{AllowMissingRuntime: true}
	got := checkRuntimeSchedulerRolePolicy("", false, nil, mode, "role", "arn")
	if got.Key != runtimeSchedulerRolePolicyKey {
		t.Errorf("Key=%q want %q", got.Key, runtimeSchedulerRolePolicyKey)
	}
	if got.Title != runtimeSchedulerRolePolicyTitle {
		t.Errorf("Title=%q want %q", got.Title, runtimeSchedulerRolePolicyTitle)
	}
}
