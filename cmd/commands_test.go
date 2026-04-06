package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	"github.com/ffreis/platform-org/internal/activation"
)

const (
	testDestroyProdConfirmation = "destroy-prod\n"
	testPlatformOrgStateKey     = "platform-org/prod/terraform.tfstate"
)

func TestPlanCommandAllowsDetailedExitCodeTwo(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.creds = rawCreds{Region: testRegion}
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	if err := os.MkdirAll(filepath.Join(stack, terraformDirName), 0o755); err != nil {
		t.Fatalf(errMkdirTerraform, err)
	}
	traceFile := filepath.Join(t.TempDir(), traceFileName)
	t.Setenv("TRACE_FILE", traceFile)
	setupFakeTerraform(t, `printf '%s\n' "$*" > "$TRACE_FILE"; exit 2`)
	withWorkingDir(t, root)
	planCmd.SetContext(context.Background())

	if err := planCmd.RunE(planCmd, nil); err != nil {
		t.Fatalf("planCmd.RunE: %v", err)
	}
	got, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf(errReadTraceFile, err)
	}
	want := "plan -detailed-exitcode -var-file=../envs/prod/terraform.tfvars -var-file=../envs/prod/fetched.auto.tfvars.json\n"
	if string(got) != want {
		t.Fatalf("plan args: want %q got %q", want, string(got))
	}
}

func TestApplyCommandAddsAutoApprove(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.creds = rawCreds{Region: testRegion}
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	if err := os.MkdirAll(filepath.Join(stack, terraformDirName), 0o755); err != nil {
		t.Fatalf(errMkdirTerraform, err)
	}
	traceFile := filepath.Join(t.TempDir(), traceFileName)
	t.Setenv("TRACE_FILE", traceFile)
	setupFakeTerraform(t, `printf '%s\n' "$*" > "$TRACE_FILE"`)
	withWorkingDir(t, root)
	applyCmd.SetContext(context.Background())
	oldApprove := applyAutoApprove
	applyAutoApprove = true
	oldNoActivate := applyNoActivate
	applyNoActivate = true // skip scheduler call in unit tests
	oldDoctor := platformOrgDoctorRunFn
	t.Cleanup(func() {
		applyAutoApprove = oldApprove
		applyNoActivate = oldNoActivate
		platformOrgDoctorRunFn = oldDoctor
	})
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{Summary: platformOrgDoctorSummary{OK: 1, Total: 1}}, nil
	}

	if err := applyCmd.RunE(applyCmd, nil); err != nil {
		t.Fatalf("applyCmd.RunE: %v", err)
	}
	got, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf(errReadTraceFile, err)
	}
	want := "apply -var-file=../envs/prod/terraform.tfvars -var-file=../envs/prod/fetched.auto.tfvars.json -auto-approve\n"
	if string(got) != want {
		t.Fatalf("apply args: want %q got %q", want, string(got))
	}
}

func TestActivateCommandCallsActivateFn(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.accountID = testAccountID

	called := false
	old := activateFn
	activateFn = func(_ context.Context) error {
		called = true
		return nil
	}
	t.Cleanup(func() { activateFn = old })

	activateCmd.SetContext(context.Background())
	if err := activateCmd.RunE(activateCmd, nil); err != nil {
		t.Fatalf("activateCmd.RunE: %v", err)
	}
	if !called {
		t.Fatal("expected activateFn to be called")
	}
}

func TestActivateCommandHandlesNotReadyError(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.accountID = testAccountID

	old := activateFn
	activateFn = func(_ context.Context) error {
		return &activation.ErrNotReady{Missing: []string{"Stack"}}
	}
	t.Cleanup(func() { activateFn = old })

	activateCmd.SetContext(context.Background())
	if err := activateCmd.RunE(activateCmd, nil); err != nil {
		t.Fatalf("activateCmd should exit cleanly on ErrNotReady, got: %v", err)
	}
}

func TestNukeCommandCancelsOnUnexpectedConfirmation(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.creds = rawCreds{Region: testRegion}
	root := t.TempDir()
	initRepoLayout(t, root, testEnv)
	traceFile := filepath.Join(t.TempDir(), traceFileName)
	t.Setenv("TRACE_FILE", traceFile)
	setupFakeTerraform(t, `printf '%s\n' "$*" > "$TRACE_FILE"`)
	withWorkingDir(t, root)
	setStdinText(t, "nope\n")
	nukeCmd.SetContext(context.Background())
	oldInspect := inspectRuntimeStateStoresForNukeFn
	oldBackup := backupRuntimeStateStoresForNukeFn
	oldScanManaged := scanManagedPlatformOrgResourcesNukeFn
	oldDoctor := platformOrgDoctorRunFn
	t.Cleanup(func() {
		inspectRuntimeStateStoresForNukeFn = oldInspect
		backupRuntimeStateStoresForNukeFn = oldBackup
		scanManagedPlatformOrgResourcesNukeFn = oldScanManaged
		platformOrgDoctorRunFn = oldDoctor
	})
	inspectRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string) (runtimeStateBackupPlan, error) {
		return runtimeStateBackupPlan{}, nil
	}
	backupRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string, string, runtimeStateBackupPlan) error {
		t.Fatal("backup should not run on cancel")
		return nil
	}
	scanManagedPlatformOrgResourcesNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{Summary: platformOrgDoctorSummary{OK: 1, Total: 1}}, nil
	}

	if err := nukeCmd.RunE(nukeCmd, nil); err != nil {
		t.Fatalf("nukeCmd.RunE cancel path: %v", err)
	}
	if _, err := os.Stat(traceFile); !os.IsNotExist(err) {
		t.Fatalf("terraform should not run on cancel, stat err=%v", err)
	}
}

func TestNukeCommandAllowsForceFalse(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.creds = rawCreds{Region: testRegion}
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	if err := os.MkdirAll(filepath.Join(stack, terraformDirName), 0o755); err != nil {
		t.Fatalf(errMkdirTerraform, err)
	}
	traceFile := filepath.Join(t.TempDir(), traceFileName)
	t.Setenv("TRACE_FILE", traceFile)
	setupFakeTerraform(t, `printf '%s\n' "$*" > "$TRACE_FILE"`)
	withWorkingDir(t, root)
	setStdinText(t, testDestroyProdConfirmation)
	nukeCmd.SetContext(context.Background())
	old := nukeForce
	oldList := listSchedulesFn
	oldDelete := deleteScheduleFn
	oldInspect := inspectRuntimeStateStoresForNukeFn
	oldBackup := backupRuntimeStateStoresForNukeFn
	oldScanManaged := scanManagedPlatformOrgResourcesNukeFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	oldDoctor := platformOrgDoctorRunFn
	nukeForce = false
	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return &scheduler.ListSchedulesOutput{}, nil
	}
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return &scheduler.DeleteScheduleOutput{}, nil
	}
	inspectRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string) (runtimeStateBackupPlan, error) {
		return runtimeStateBackupPlan{}, nil
	}
	backupRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string, string, runtimeStateBackupPlan) error {
		t.Fatal("backup should not run when no stateful data is present")
		return nil
	}
	t.Cleanup(func() {
		nukeForce = old
		listSchedulesFn = oldList
		deleteScheduleFn = oldDelete
		inspectRuntimeStateStoresForNukeFn = oldInspect
		backupRuntimeStateStoresForNukeFn = oldBackup
		scanManagedPlatformOrgResourcesNukeFn = oldScanManaged
		platformOrgCleanupTargetsForNukeFn = oldTargets
		platformOrgDoctorRunFn = oldDoctor
	})
	scanManagedPlatformOrgResourcesNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{Summary: platformOrgDoctorSummary{OK: 1, Total: 1}}, nil
	}

	if err := nukeCmd.RunE(nukeCmd, nil); err != nil {
		t.Fatalf("nukeCmd.RunE force=false: %v", err)
	}
	got, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf(errReadTraceFile, err)
	}
	want := "destroy -var-file=../envs/prod/terraform.tfvars -var-file=../envs/prod/fetched.auto.tfvars.json\n"
	if string(got) != want {
		t.Fatalf("destroy args with force=false: want %q got %q", want, string(got))
	}
}

func TestNukeCommandRunsDestroyAfterConfirmation(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.creds = rawCreds{Region: testRegion}
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	if err := os.MkdirAll(filepath.Join(stack, terraformDirName), 0o755); err != nil {
		t.Fatalf(errMkdirTerraform, err)
	}
	traceFile := filepath.Join(t.TempDir(), traceFileName)
	t.Setenv("TRACE_FILE", traceFile)
	setupFakeTerraform(t, `printf '%s\n' "$*" > "$TRACE_FILE"`)
	withWorkingDir(t, root)
	setStdinText(t, testDestroyProdConfirmation)
	nukeCmd.SetContext(context.Background())
	oldList := listSchedulesFn
	oldDelete := deleteScheduleFn
	oldInspect := inspectRuntimeStateStoresForNukeFn
	oldBackup := backupRuntimeStateStoresForNukeFn
	oldScanManaged := scanManagedPlatformOrgResourcesNukeFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	oldDoctor := platformOrgDoctorRunFn
	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return &scheduler.ListSchedulesOutput{}, nil
	}
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return &scheduler.DeleteScheduleOutput{}, nil
	}
	inspectRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string) (runtimeStateBackupPlan, error) {
		return runtimeStateBackupPlan{}, nil
	}
	backupRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string, string, runtimeStateBackupPlan) error {
		t.Fatal("backup should not run when no stateful data is present")
		return nil
	}
	t.Cleanup(func() {
		listSchedulesFn = oldList
		deleteScheduleFn = oldDelete
		inspectRuntimeStateStoresForNukeFn = oldInspect
		backupRuntimeStateStoresForNukeFn = oldBackup
		scanManagedPlatformOrgResourcesNukeFn = oldScanManaged
		platformOrgCleanupTargetsForNukeFn = oldTargets
		platformOrgDoctorRunFn = oldDoctor
	})
	scanManagedPlatformOrgResourcesNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{Summary: platformOrgDoctorSummary{OK: 1, Total: 1}}, nil
	}

	if err := nukeCmd.RunE(nukeCmd, nil); err != nil {
		t.Fatalf("nukeCmd.RunE: %v", err)
	}
	got, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf(errReadTraceFile, err)
	}
	want := "destroy -var-file=../envs/prod/terraform.tfvars -var-file=../envs/prod/fetched.auto.tfvars.json -auto-approve\n"
	if string(got) != want {
		t.Fatalf("destroy args: want %q got %q", want, string(got))
	}
}

func TestNukeCommandBacksUpRuntimeStateBeforeDestroy(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.creds = rawCreds{Region: testRegion}
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	if err := os.MkdirAll(filepath.Join(stack, terraformDirName), 0o755); err != nil {
		t.Fatalf(errMkdirTerraform, err)
	}
	traceFile := filepath.Join(t.TempDir(), traceFileName)
	t.Setenv("TRACE_FILE", traceFile)
	setupFakeTerraform(t, `printf '%s\n' "$*" > "$TRACE_FILE"`)
	withWorkingDir(t, root)
	setStdinText(t, "backup-destroy-prod\n")
	nukeCmd.SetContext(context.Background())

	oldList := listSchedulesFn
	oldDelete := deleteScheduleFn
	oldInspect := inspectRuntimeStateStoresForNukeFn
	oldBackup := backupRuntimeStateStoresForNukeFn
	oldDir := defaultRuntimeBackupDirForNukeFn
	oldFlag := nukeBackupDir
	oldScanManaged := scanManagedPlatformOrgResourcesNukeFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	oldDoctor := platformOrgDoctorRunFn
	var gotBackupDir string
	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return &scheduler.ListSchedulesOutput{}, nil
	}
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return &scheduler.DeleteScheduleOutput{}, nil
	}
	inspectRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string) (runtimeStateBackupPlan, error) {
		return runtimeStateBackupPlan{
			BucketName:         "acme-tf-state-runtime",
			BucketVersionCount: 2,
			TableName:          "acme-tf-locks-runtime",
			TableItemCount:     1,
		}, nil
	}
	backupRuntimeStateStoresForNukeFn = func(_ context.Context, _ sdkaws.Config, _ string, dir string, _ runtimeStateBackupPlan) error {
		gotBackupDir = filepath.Clean(dir)
		return nil
	}
	defaultRuntimeBackupDirForNukeFn = func(string, string) string {
		return filepath.Join(root, ".backups", "expected")
	}
	nukeBackupDir = ""
	t.Cleanup(func() {
		listSchedulesFn = oldList
		deleteScheduleFn = oldDelete
		inspectRuntimeStateStoresForNukeFn = oldInspect
		backupRuntimeStateStoresForNukeFn = oldBackup
		defaultRuntimeBackupDirForNukeFn = oldDir
		nukeBackupDir = oldFlag
		scanManagedPlatformOrgResourcesNukeFn = oldScanManaged
		platformOrgCleanupTargetsForNukeFn = oldTargets
		platformOrgDoctorRunFn = oldDoctor
	})
	scanManagedPlatformOrgResourcesNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{Summary: platformOrgDoctorSummary{OK: 1, Total: 1}}, nil
	}

	if err := nukeCmd.RunE(nukeCmd, nil); err != nil {
		t.Fatalf("nukeCmd.RunE with backup: %v", err)
	}
	if gotBackupDir == "" {
		t.Fatal("expected backupRuntimeStateStoresForNukeFn to be called")
	}
	got, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf(errReadTraceFile, err)
	}
	want := "destroy -var-file=../envs/prod/terraform.tfvars -var-file=../envs/prod/fetched.auto.tfvars.json -auto-approve\n"
	if string(got) != want {
		t.Fatalf("destroy args: want %q got %q", want, string(got))
	}
}

func TestDeletePendingSchedulesRemovesAllSchedulesInGroup(t *testing.T) {
	oldList := listSchedulesFn
	oldDelete := deleteScheduleFn
	defer func() {
		listSchedulesFn = oldList
		deleteScheduleFn = oldDelete
	}()

	var deleted []string
	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return &scheduler.ListSchedulesOutput{
			Schedules: []schedulertypes.ScheduleSummary{
				{Name: sdkaws.String("ffreis-activate-cost-tags")},
				{Name: sdkaws.String("ffreis-extra-job")},
			},
		}, nil
	}
	deleteScheduleFn = func(_ context.Context, input *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		deleted = append(deleted, sdkaws.ToString(input.Name))
		return &scheduler.DeleteScheduleOutput{}, nil
	}

	got, err := deletePendingSchedules(context.Background(), "ffreis")
	if err != nil {
		t.Fatalf("deletePendingSchedules: %v", err)
	}
	if len(got) != 2 || len(deleted) != 2 {
		t.Fatalf("unexpected deleted schedules: got=%v deleted=%v", got, deleted)
	}
}

func TestNukeCommandFallsBackWhenDestroyLeavesManagedResources(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.creds = rawCreds{Region: testRegion}
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	if err := os.MkdirAll(filepath.Join(stack, terraformDirName), 0o755); err != nil {
		t.Fatalf(errMkdirTerraform, err)
	}
	traceFile := filepath.Join(t.TempDir(), traceFileName)
	t.Setenv("TRACE_FILE", traceFile)
	setupFakeTerraform(t, `printf '%s\n' "$*" > "$TRACE_FILE"`)
	withWorkingDir(t, root)
	setStdinText(t, testDestroyProdConfirmation)
	nukeCmd.SetContext(context.Background())

	oldList := listSchedulesFn
	oldDelete := deleteScheduleFn
	oldInspect := inspectRuntimeStateStoresForNukeFn
	oldBackup := backupRuntimeStateStoresForNukeFn
	oldScanManaged := scanManagedPlatformOrgResourcesNukeFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	oldFallback := runManagedSDKFallbackNukeFn
	oldReset := resetBackendStateForNukeFn
	oldDoctor := platformOrgDoctorRunFn
	defer func() {
		listSchedulesFn = oldList
		deleteScheduleFn = oldDelete
		inspectRuntimeStateStoresForNukeFn = oldInspect
		backupRuntimeStateStoresForNukeFn = oldBackup
		scanManagedPlatformOrgResourcesNukeFn = oldScanManaged
		platformOrgCleanupTargetsForNukeFn = oldTargets
		runManagedSDKFallbackNukeFn = oldFallback
		resetBackendStateForNukeFn = oldReset
		platformOrgDoctorRunFn = oldDoctor
	}()

	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return &scheduler.ListSchedulesOutput{}, nil
	}
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return &scheduler.DeleteScheduleOutput{}, nil
	}
	inspectRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string) (runtimeStateBackupPlan, error) {
		return runtimeStateBackupPlan{}, nil
	}
	backupRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string, string, runtimeStateBackupPlan) error {
		t.Fatal("backup should not run when no runtime data is present")
		return nil
	}

	var scanCalls int
	scanManagedPlatformOrgResourcesNukeFn = func(context.Context) ([]auditResource, error) {
		scanCalls++
		if scanCalls == 1 {
			return []auditResource{{status: "OK", resourceType: "s3", name: "ffreis-tf-state-runtime", stack: testPlatformOrgStack}}, nil
		}
		return nil, nil
	}
	var targetCalls int
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		targetCalls++
		if targetCalls <= 2 {
			return []auditResource{{status: "OK", resourceType: "s3", name: "ffreis-tf-state-runtime", stack: testPlatformOrgStack}}, nil
		}
		return nil, nil
	}
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{Summary: platformOrgDoctorSummary{OK: 1, Total: 1}}, nil
	}

	fallbackCalled := false
	runManagedSDKFallbackNukeFn = func(_ context.Context, _ *commandOutput, resources []auditResource, force bool) (nukeFallbackSummary, error) {
		fallbackCalled = true
		if !force {
			t.Fatal("fallback cleanup should always run with force=true")
		}
		if len(resources) != 1 || resources[0].name != "ffreis-tf-state-runtime" {
			t.Fatalf("unexpected fallback resources: %+v", resources)
		}
		return nukeFallbackSummary{Deleted: 1}, nil
	}

	resetCalled := false
	resetBackendStateForNukeFn = func(_ context.Context, gotRoot, gotStack, gotEnv, gotBackupDir string) (nukeBackendResetSummary, error) {
		resetCalled = true
		if gotRoot != root || gotStack != stack || gotEnv != testEnv {
			t.Fatalf("unexpected reset args: root=%s stack=%s env=%s", gotRoot, gotStack, gotEnv)
		}
		return nukeBackendResetSummary{
			BucketName:            "ffreis-tf-state-root",
			TableName:             "ffreis-tf-locks-root",
			StateKey:              testPlatformOrgStateKey,
			RemovedLocalTerraform: true,
		}, nil
	}

	if err := nukeCmd.RunE(nukeCmd, nil); err != nil {
		t.Fatalf("nukeCmd.RunE fallback path: %v", err)
	}
	if !fallbackCalled {
		t.Fatal("expected AWS fallback cleanup to run")
	}
	if !resetCalled {
		t.Fatal("expected backend reset to run")
	}
}

func TestLoadBackendStateConfigForNuke(t *testing.T) {
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	writeFile(t, filepath.Join(stack, "backend.local.hcl"), "bucket = \"ffreis-tf-state-root\"\ndynamodb_table = \"ffreis-tf-locks-root\"\n")
	writeFile(t, filepath.Join(root, envsDirName, testEnv, "backend.hcl"), "key = \"platform-org/prod/terraform.tfstate\"\n")

	got, err := loadBackendStateConfigForNuke(root, testEnv)
	if err != nil {
		t.Fatalf("loadBackendStateConfigForNuke: %v", err)
	}
	if got.BucketName != "ffreis-tf-state-root" || got.TableName != "ffreis-tf-locks-root" || got.StateKey != testPlatformOrgStateKey {
		t.Fatalf("unexpected backend state config: %+v", got)
	}
}

func TestNukeCommandFallsBackWhenTerraformInitFails(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.creds = rawCreds{Region: testRegion}
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	withWorkingDir(t, root)
	setStdinText(t, testDestroyProdConfirmation)
	nukeCmd.SetContext(context.Background())

	oldList := listSchedulesFn
	oldDelete := deleteScheduleFn
	oldInspect := inspectRuntimeStateStoresForNukeFn
	oldBackup := backupRuntimeStateStoresForNukeFn
	oldEnsureInit := ensureInitForNukeFn
	oldScanManaged := scanManagedPlatformOrgResourcesNukeFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	oldFallback := runManagedSDKFallbackNukeFn
	oldReset := resetBackendStateForNukeFn
	oldDoctor := platformOrgDoctorRunFn
	defer func() {
		listSchedulesFn = oldList
		deleteScheduleFn = oldDelete
		inspectRuntimeStateStoresForNukeFn = oldInspect
		backupRuntimeStateStoresForNukeFn = oldBackup
		ensureInitForNukeFn = oldEnsureInit
		scanManagedPlatformOrgResourcesNukeFn = oldScanManaged
		platformOrgCleanupTargetsForNukeFn = oldTargets
		runManagedSDKFallbackNukeFn = oldFallback
		resetBackendStateForNukeFn = oldReset
		platformOrgDoctorRunFn = oldDoctor
	}()

	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return &scheduler.ListSchedulesOutput{}, nil
	}
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return &scheduler.DeleteScheduleOutput{}, nil
	}
	inspectRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string) (runtimeStateBackupPlan, error) {
		return runtimeStateBackupPlan{}, nil
	}
	backupRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string, string, runtimeStateBackupPlan) error {
		t.Fatal("backup should not run when no runtime data is present")
		return nil
	}
	ensureInitForNukeFn = func(context.Context, string, string, string, rawCreds) error {
		return os.ErrInvalid
	}
	scanManagedPlatformOrgResourcesNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil
	}
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{Summary: platformOrgDoctorSummary{OK: 1, Total: 1}}, nil
	}
	fallbackCalled := false
	runManagedSDKFallbackNukeFn = func(context.Context, *commandOutput, []auditResource, bool) (nukeFallbackSummary, error) {
		fallbackCalled = true
		return nukeFallbackSummary{}, nil
	}
	resetCalled := false
	resetBackendStateForNukeFn = func(_ context.Context, gotRoot, gotStack, gotEnv, _ string) (nukeBackendResetSummary, error) {
		resetCalled = true
		if gotRoot != root || gotStack != stack || gotEnv != testEnv {
			t.Fatalf("unexpected reset args: root=%s stack=%s env=%s", gotRoot, gotStack, gotEnv)
		}
		return nukeBackendResetSummary{}, nil
	}

	if err := nukeCmd.RunE(nukeCmd, nil); err != nil {
		t.Fatalf("nukeCmd.RunE init fallback path: %v", err)
	}
	if fallbackCalled {
		t.Fatal("expected no SDK cleanup when no managed platform-org resources are discovered")
	}
	if !resetCalled {
		t.Fatal("expected backend reset to run after init failure fallback")
	}
}

func TestNukeCommandSkipsTerraformWhenBackendMissing(t *testing.T) {
	d.log = newLogger("error")
	d.env = testEnv
	d.org = "ffreis"
	d.creds = rawCreds{Region: testRegion}
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	withWorkingDir(t, root)
	setStdinText(t, testDestroyProdConfirmation)
	nukeCmd.SetContext(context.Background())

	oldList := listSchedulesFn
	oldDelete := deleteScheduleFn
	oldInspect := inspectRuntimeStateStoresForNukeFn
	oldBackup := backupRuntimeStateStoresForNukeFn
	oldCheckBackend := checkStateBackendExistsFn
	oldEnsureInit := ensureInitForNukeFn
	oldRunTf := runTerraformForNukeFn
	oldTargets := platformOrgCleanupTargetsForNukeFn
	oldFallback := runManagedSDKFallbackNukeFn
	oldReset := resetBackendStateForNukeFn
	oldDoctor := platformOrgDoctorRunFn
	defer func() {
		listSchedulesFn = oldList
		deleteScheduleFn = oldDelete
		inspectRuntimeStateStoresForNukeFn = oldInspect
		backupRuntimeStateStoresForNukeFn = oldBackup
		checkStateBackendExistsFn = oldCheckBackend
		ensureInitForNukeFn = oldEnsureInit
		runTerraformForNukeFn = oldRunTf
		platformOrgCleanupTargetsForNukeFn = oldTargets
		runManagedSDKFallbackNukeFn = oldFallback
		resetBackendStateForNukeFn = oldReset
		platformOrgDoctorRunFn = oldDoctor
	}()

	listSchedulesFn = func(context.Context, *scheduler.ListSchedulesInput) (*scheduler.ListSchedulesOutput, error) {
		return &scheduler.ListSchedulesOutput{}, nil
	}
	deleteScheduleFn = func(context.Context, *scheduler.DeleteScheduleInput) (*scheduler.DeleteScheduleOutput, error) {
		return &scheduler.DeleteScheduleOutput{}, nil
	}
	inspectRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string) (runtimeStateBackupPlan, error) {
		return runtimeStateBackupPlan{}, nil
	}
	backupRuntimeStateStoresForNukeFn = func(context.Context, sdkaws.Config, string, string, runtimeStateBackupPlan) error {
		return nil
	}
	checkStateBackendExistsFn = func(context.Context, sdkaws.Config, string) (bool, error) {
		return false, nil // backend is gone
	}
	ensureInitForNukeFn = func(context.Context, string, string, string, rawCreds) error {
		t.Fatal("terraform init must not be called when backend is missing")
		return nil
	}
	runTerraformForNukeFn = func(context.Context, runOptions) (int, error) {
		t.Fatal("terraform destroy must not be called when backend is missing")
		return 0, nil
	}
	platformOrgCleanupTargetsForNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, nil // no remaining resources
	}
	platformOrgDoctorRunFn = func(context.Context, platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
		return PlatformOrgDoctorReport{Summary: platformOrgDoctorSummary{OK: 1, Total: 1}}, nil
	}
	runManagedSDKFallbackNukeFn = func(context.Context, *commandOutput, []auditResource, bool) (nukeFallbackSummary, error) {
		return nukeFallbackSummary{}, nil
	}
	resetBackendStateForNukeFn = func(_ context.Context, gotRoot, gotStack, gotEnv, _ string) (nukeBackendResetSummary, error) {
		if gotRoot != root || gotStack != stack || gotEnv != testEnv {
			t.Fatalf("unexpected reset args: root=%s stack=%s env=%s", gotRoot, gotStack, gotEnv)
		}
		return nukeBackendResetSummary{}, nil
	}

	if err := nukeCmd.RunE(nukeCmd, nil); err != nil {
		t.Fatalf("nukeCmd.RunE missing-backend path: %v", err)
	}
}

func TestNukeCleanupTargetsToleratesScanError(t *testing.T) {
	d.log = newLogger("error")

	oldScan := scanManagedPlatformOrgResourcesNukeFn
	oldExplicit := explicitPlatformOrgCleanupTargetsFn
	defer func() {
		scanManagedPlatformOrgResourcesNukeFn = oldScan
		explicitPlatformOrgCleanupTargetsFn = oldExplicit
	}()

	scanManagedPlatformOrgResourcesNukeFn = func(context.Context) ([]auditResource, error) {
		return nil, os.ErrInvalid // simulate tagging API failure
	}
	explicitPlatformOrgCleanupTargetsFn = func(context.Context) ([]auditResource, error) {
		return []auditResource{
			{status: "OK", resourceType: "organizations/organization", name: "organization", stack: testPlatformOrgStack},
		}, nil
	}

	got, err := platformOrgCleanupTargetsForNuke(context.Background())
	if err != nil {
		t.Fatalf("expected no error when scan fails: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected explicit targets to be returned even when scan fails")
	}
}
