package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/spf13/cobra"
)

type platformOrgDoctorCheck struct {
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Detail   string   `json:"detail"`
	Hint     string   `json:"hint,omitempty"`
	Related  []string `json:"related,omitempty"`
	Blocking bool     `json:"blocking"`
}

type platformOrgDoctorSection struct {
	Title  string                   `json:"title"`
	Checks []platformOrgDoctorCheck `json:"checks"`
}

type PlatformOrgDoctorReport struct {
	Mode     string                     `json:"mode"`
	Sections []platformOrgDoctorSection `json:"sections"`
	Summary  platformOrgDoctorSummary   `json:"summary"`
}

type platformOrgDoctorSummary struct {
	OK    int `json:"ok"`
	Warn  int `json:"warn"`
	Fail  int `json:"fail"`
	Info  int `json:"info"`
	Total int `json:"total"`
}

type platformOrgDoctorMode struct {
	Name                string
	IncludeBackend      bool
	IncludeState        bool
	IncludeRuntime      bool
	IncludeInventory    bool
	AllowMissingRuntime bool
	LenientState        bool
}

type terraformStateDocument struct {
	Resources []terraformStateResource `json:"resources"`
}

type terraformStateResource struct {
	Module    string                   `json:"module"`
	Mode      string                   `json:"mode"`
	Type      string                   `json:"type"`
	Name      string                   `json:"name"`
	Instances []terraformStateInstance `json:"instances"`
}

type terraformStateInstance struct {
	IndexKey   any            `json:"index_key"`
	Attributes map[string]any `json:"attributes"`
}

type terraformStateObject struct {
	Address string
	Type    string
	Name    string
	ARN     string
}

var (
	platformOrgDoctorRunFn = runPlatformOrgDoctor
	platformOrgDoctorModes = struct {
		command platformOrgDoctorMode
		audit   platformOrgDoctorMode
		apply   platformOrgDoctorMode
		nuke    platformOrgDoctorMode
	}{
		command: platformOrgDoctorMode{
			Name:                "doctor",
			IncludeBackend:      true,
			IncludeState:        true,
			IncludeRuntime:      true,
			IncludeInventory:    true,
			AllowMissingRuntime: true,
		},
		audit: platformOrgDoctorMode{
			Name:                "audit",
			IncludeBackend:      true,
			IncludeState:        true,
			IncludeRuntime:      true,
			IncludeInventory:    true,
			AllowMissingRuntime: true,
		},
		apply: platformOrgDoctorMode{
			Name:                "apply-preflight",
			IncludeBackend:      true,
			IncludeState:        true,
			IncludeRuntime:      true,
			AllowMissingRuntime: true,
		},
		nuke: platformOrgDoctorMode{
			Name:                "nuke-preflight",
			IncludeBackend:      true,
			IncludeState:        true,
			IncludeRuntime:      true,
			AllowMissingRuntime: true,
			LenientState:        true,
		},
	}
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Validate backend, state, runtime wiring, and ownership integrity",
	Long: `doctor runs read-only integrity checks across the platform-org backend,
Terraform state, runtime wiring, and ownership contracts.

This command does not create, modify, or delete any AWS resources.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		jsonOut, _ := cmd.Flags().GetBool("json")
		out := newCommandOutput(cmd, d.ui)

		report, err := platformOrgDoctorRunFn(ctx, platformOrgDoctorModes.command)
		if err != nil {
			return err
		}

		if jsonOut {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(report)
		}

		out.Header("Platform Org Doctor", envAccountRegionSummary(d.env, d.accountID, d.region))
		out.Summary("Context", "org="+d.org, "mode="+report.Mode)
		out.Blank()
		printPlatformOrgDoctorReport(out, report)
		out.Blank()
		printPlatformOrgDoctorSummary(out, report)
		if report.HasFailures() {
			return fmt.Errorf("doctor found %d blocking integrity issue(s)", report.Summary.Fail)
		}
		return nil
	},
}

func init() {
	doctorCmd.Flags().Bool("json", false, "output the doctor report as JSON")
	rootCmd.AddCommand(doctorCmd)
}

func runPlatformOrgDoctor(ctx context.Context, mode platformOrgDoctorMode) (PlatformOrgDoctorReport, error) {
	report := PlatformOrgDoctorReport{Mode: mode.Name}
	if mode.IncludeBackend {
		section, err := platformOrgBackendDoctorSection(ctx)
		if err != nil {
			return PlatformOrgDoctorReport{}, err
		}
		report.Sections = append(report.Sections, section)
	}
	if mode.IncludeState {
		section, err := platformOrgStateDoctorSection(ctx, mode)
		if err != nil {
			return PlatformOrgDoctorReport{}, err
		}
		report.Sections = append(report.Sections, section)
	}
	if mode.IncludeRuntime {
		section, err := platformOrgRuntimeDoctorSection(ctx, mode)
		if err != nil {
			return PlatformOrgDoctorReport{}, err
		}
		report.Sections = append(report.Sections, section)
	}
	if mode.IncludeInventory {
		section, err := platformOrgInventoryDoctorSection(ctx)
		if err != nil {
			return PlatformOrgDoctorReport{}, err
		}
		report.Sections = append(report.Sections, section)
	}
	report.Summary = summarizePlatformOrgDoctor(report.Sections)
	return report, nil
}

func summarizePlatformOrgDoctor(sections []platformOrgDoctorSection) platformOrgDoctorSummary {
	var summary platformOrgDoctorSummary
	for _, section := range sections {
		for _, check := range section.Checks {
			summary.Total++
			switch check.Status {
			case "ok":
				summary.OK++
			case "warn":
				summary.Warn++
			case "fail":
				summary.Fail++
			case "info":
				summary.Info++
			}
		}
	}
	return summary
}

func (r PlatformOrgDoctorReport) HasFailures() bool {
	return r.BlockingFailures() > 0
}

func (r PlatformOrgDoctorReport) BlockingFailures() int {
	count := 0
	for _, section := range r.Sections {
		for _, check := range section.Checks {
			if check.Status == "fail" && check.Blocking {
				count++
			}
		}
	}
	return count
}

func platformOrgBackendDoctorSection(ctx context.Context) (platformOrgDoctorSection, error) {
	root, err := repoRoot()
	if err != nil {
		return platformOrgDoctorSection{}, err
	}
	stack, err := stackDir()
	if err != nil {
		return platformOrgDoctorSection{}, err
	}

	checks := []platformOrgDoctorCheck{}
	localPath := filepath.Join(stack, "backend.local.hcl")
	envPath := filepath.Join(root, envsDirName, d.env, "backend.hcl")
	local, localErr := parseBackendConfigFile(localPath)
	envCfg, envErr := parseBackendConfigFile(envPath)

	if localErr != nil {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "backend.local-file",
			Title:    "backend.local.hcl is readable",
			Status:   "fail",
			Detail:   localErr.Error(),
			Hint:     "run platform-bootstrap fetch to regenerate terraform/stack/backend.local.hcl",
			Related:  []string{localPath},
			Blocking: true,
		})
	} else {
		missing := missingBackendMapKeys(local, "bucket", "dynamodb_table", "region")
		status := "ok"
		detail := "backend.local.hcl contains bucket, dynamodb_table, and region"
		blocking := false
		if len(missing) > 0 {
			status = "fail"
			detail = "backend.local.hcl is missing keys: " + strings.Join(missing, ", ")
			blocking = true
		}
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "backend.local-file",
			Title:    "backend.local.hcl is complete",
			Status:   status,
			Detail:   detail,
			Hint:     "regenerate backend.local.hcl from bootstrap if any key is missing",
			Related:  []string{localPath},
			Blocking: blocking,
		})
	}

	if envErr != nil {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "backend.env-file",
			Title:    "env backend.hcl is readable",
			Status:   "fail",
			Detail:   envErr.Error(),
			Hint:     "restore terraform/envs/" + d.env + "/backend.hcl",
			Related:  []string{envPath},
			Blocking: true,
		})
	} else {
		missing := missingBackendMapKeys(envCfg, "key")
		status := "ok"
		detail := "env backend.hcl contains the committed state key"
		blocking := false
		if len(missing) > 0 {
			status = "fail"
			detail = "env backend.hcl is missing keys: " + strings.Join(missing, ", ")
			blocking = true
		}
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "backend.env-file",
			Title:    "env backend.hcl is complete",
			Status:   status,
			Detail:   detail,
			Hint:     "restore terraform/envs/" + d.env + "/backend.hcl with the committed key",
			Related:  []string{envPath},
			Blocking: blocking,
		})
	}

	cfg, err := loadBackendStateConfigForNukeFn(root, d.env)
	if err != nil {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "backend.contract",
			Title:    "backend contract resolves cleanly",
			Status:   "fail",
			Detail:   err.Error(),
			Hint:     "repair backend.local.hcl and backend.hcl so the root backend can be derived unambiguously",
			Blocking: true,
		})
		return platformOrgDoctorSection{Title: "Backend Contract", Checks: checks}, nil
	}

	expectedBucket := d.org + "-tf-state-root"
	expectedTable := d.org + "-tf-locks-root"
	expectedKey := fmt.Sprintf("platform-org/%s/terraform.tfstate", d.env)
	status := "ok"
	detail := fmt.Sprintf("bucket=%s  table=%s  key=%s", cfg.BucketName, cfg.TableName, cfg.StateKey)
	if cfg.BucketName != expectedBucket || cfg.TableName != expectedTable || cfg.StateKey != expectedKey {
		status = "fail"
		detail = fmt.Sprintf("resolved backend points to bucket=%s table=%s key=%s, expected bucket=%s table=%s key=%s", cfg.BucketName, cfg.TableName, cfg.StateKey, expectedBucket, expectedTable, expectedKey)
	}
	checks = append(checks, platformOrgDoctorCheck{
		Key:      "backend.identity",
		Title:    "backend points at the current org and env",
		Status:   status,
		Detail:   detail,
		Hint:     "regenerate backend files if they point at another org, env, or root backend",
		Related:  []string{expectedBucket, expectedTable, expectedKey},
		Blocking: status == "fail",
	})

	bucketExists, bucketErr := s3BucketExists(ctx, cfg.BucketName)
	checks = append(checks, platformOrgDoctorCheck{
		Key:      "backend.bucket",
		Title:    "root backend bucket exists",
		Status:   existsStatus(bucketExists, bucketErr),
		Detail:   existsDetail(bucketExists, bucketErr, "root backend bucket", cfg.BucketName),
		Hint:     "recreate the bootstrap root tfstate bucket before running platform-org again",
		Related:  []string{cfg.BucketName},
		Blocking: bucketErr != nil || !bucketExists,
	})

	tableExists, tableErr := dynamoTableExists(ctx, cfg.TableName)
	checks = append(checks, platformOrgDoctorCheck{
		Key:      "backend.lock-table",
		Title:    "root backend lock table exists",
		Status:   existsStatus(tableExists, tableErr),
		Detail:   existsDetail(tableExists, tableErr, "root backend lock table", cfg.TableName),
		Hint:     "recreate the bootstrap root lock table before running platform-org again",
		Related:  []string{cfg.TableName},
		Blocking: tableErr != nil || !tableExists,
	})

	s3Client := newNukeBackendResetS3ClientFn(d.awsCfg)
	dynamoClient := newNukeBackendResetDynamoClientFn(d.awsCfg)
	stateVersions, _, stateErr := listStateObjectVersions(ctx, s3Client, cfg.BucketName, cfg.StateKey)
	lockItems, lockErr := findMatchingLockItems(ctx, dynamoClient, cfg.TableName, cfg.BucketName, cfg.StateKey)
	if stateErr != nil {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "backend.state-object",
			Title:    "backend state object is readable",
			Status:   "fail",
			Detail:   stateErr.Error(),
			Hint:     "repair the root backend bucket or permissions so terraform state can be read directly",
			Related:  []string{cfg.BucketName + "/" + cfg.StateKey},
			Blocking: true,
		})
	} else if len(stateVersions) == 0 {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "backend.state-object",
			Title:    "backend state object is readable",
			Status:   "info",
			Detail:   "no current state object exists for " + cfg.StateKey,
			Hint:     "this is expected before the first apply or after a full nuke",
			Related:  []string{cfg.BucketName + "/" + cfg.StateKey},
			Blocking: false,
		})
	} else {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "backend.state-object",
			Title:    "backend state object is readable",
			Status:   "ok",
			Detail:   fmt.Sprintf("%d state object version(s) found for %s", len(stateVersions), cfg.StateKey),
			Hint:     "inspect the root backend bucket if this stops matching the current state key",
			Related:  []string{cfg.BucketName + "/" + cfg.StateKey},
			Blocking: false,
		})
	}

	if lockErr != nil {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "backend.lock-rows",
			Title:    "backend lock rows are coherent with the state key",
			Status:   "fail",
			Detail:   lockErr.Error(),
			Hint:     "repair the root lock table or permissions so terraform lock rows can be inspected",
			Related:  []string{cfg.TableName},
			Blocking: true,
		})
	} else {
		switch {
		case len(lockItems) > 0 && len(stateVersions) == 0:
			checks = append(checks, platformOrgDoctorCheck{
				Key:      "backend.lock-rows",
				Title:    "backend lock rows are coherent with the state key",
				Status:   "fail",
				Detail:   fmt.Sprintf("%d lock row(s) exist for %s but no current state object exists", len(lockItems), cfg.StateKey),
				Hint:     "clear orphaned terraform lock rows before trusting the root backend",
				Related:  []string{cfg.TableName, cfg.StateKey},
				Blocking: true,
			})
		case len(lockItems) == 0 && len(stateVersions) > 0:
			checks = append(checks, platformOrgDoctorCheck{
				Key:      "backend.lock-rows",
				Title:    "backend lock rows are coherent with the state key",
				Status:   "warn",
				Detail:   "state object exists but no matching lock rows are present",
				Hint:     "this is usually fine when terraform is idle, but verify the backend if concurrent operations were expected",
				Related:  []string{cfg.TableName, cfg.StateKey},
				Blocking: false,
			})
		case len(lockItems) > 0:
			checks = append(checks, platformOrgDoctorCheck{
				Key:      "backend.lock-rows",
				Title:    "backend lock rows are coherent with the state key",
				Status:   "warn",
				Detail:   fmt.Sprintf("%d matching lock row(s) exist for %s", len(lockItems), cfg.StateKey),
				Hint:     "ensure no terraform apply/destroy is still in flight before proceeding",
				Related:  []string{cfg.TableName, cfg.StateKey},
				Blocking: false,
			})
		default:
			checks = append(checks, platformOrgDoctorCheck{
				Key:      "backend.lock-rows",
				Title:    "backend lock rows are coherent with the state key",
				Status:   "ok",
				Detail:   "no orphaned lock rows found for the current state key",
				Hint:     "lock rows should remain empty when terraform is idle",
				Related:  []string{cfg.TableName, cfg.StateKey},
				Blocking: false,
			})
		}
	}

	return platformOrgDoctorSection{Title: "Backend Contract", Checks: checks}, nil
}

func platformOrgStateDoctorSection(ctx context.Context, mode platformOrgDoctorMode) (platformOrgDoctorSection, error) {
	root, err := repoRoot()
	if err != nil {
		return platformOrgDoctorSection{}, err
	}
	cfg, err := loadBackendStateConfigForNukeFn(root, d.env)
	if err != nil {
		return platformOrgDoctorSection{
			Title: "Terraform State Integrity",
			Checks: []platformOrgDoctorCheck{{
				Key:      "state.backend-config",
				Title:    "backend state can be inspected",
				Status:   "fail",
				Detail:   err.Error(),
				Hint:     "repair backend.local.hcl and backend.hcl before inspecting terraform state",
				Blocking: true,
			}},
		}, nil
	}

	expectedData, err := terraformPlanJSONFn(ctx)
	if err != nil {
		return platformOrgDoctorSection{}, fmt.Errorf("terraform plan inventory: %w", err)
	}
	expected, err := parseExpectedPlatformOrgResources(expectedData)
	if err != nil {
		return platformOrgDoctorSection{}, err
	}

	stateResources, stateErr := loadPlatformOrgStateObjects(ctx, cfg)
	liveResources, liveErr := platformOrgCleanupTargetsForNukeFn(ctx)
	checks := []platformOrgDoctorCheck{}

	if stateErr != nil {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "state.read",
			Title:    "terraform state is readable directly from S3",
			Status:   "fail",
			Detail:   stateErr.Error(),
			Hint:     "repair the root backend or clear broken state before trusting terraform metadata",
			Related:  []string{cfg.BucketName + "/" + cfg.StateKey},
			Blocking: !mode.LenientState,
		})
		return platformOrgDoctorSection{Title: "Terraform State Integrity", Checks: checks}, nil
	}

	if liveErr != nil {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "state.live-scan",
			Title:    "live platform-org resources can be scanned",
			Status:   "fail",
			Detail:   liveErr.Error(),
			Hint:     "repair tagging or explicit live-resource discovery before relying on state integrity checks",
			Blocking: !mode.LenientState,
		})
		return platformOrgDoctorSection{Title: "Terraform State Integrity", Checks: checks}, nil
	}

	stateByAddress := make(map[string]terraformStateObject, len(stateResources))
	for _, resource := range stateResources {
		stateByAddress[resource.Address] = resource
	}
	liveByARN := make(map[string]auditResource, len(liveResources))
	liveByName := make(map[string][]auditResource, len(liveResources))
	for _, resource := range liveResources {
		if resource.arn != "" {
			liveByARN[resource.arn] = resource
		}
		if resource.name != "" {
			liveByName[strings.ToLower(resource.name)] = append(liveByName[strings.ToLower(resource.name)], resource)
		}
	}

	checks = append(checks, platformOrgDoctorCheck{
		Key:      "state.summary",
		Title:    "terraform state and plan inventory were loaded",
		Status:   "ok",
		Detail:   fmt.Sprintf("%d expected terraform resource(s), %d state object(s), %d live managed resource(s)", len(expected), len(stateResources), len(liveResources)),
		Hint:     "use these counts to spot obvious drift before mutating the stack",
		Blocking: false,
	})

	expectedAddresses := make(map[string]bool, len(expected))
	for _, def := range expected {
		expectedAddresses[def.address] = true
		stateResource, statePresent := stateByAddress[def.address]
		liveResource, livePresent := matchExpectedAuditResource(def, liveByARN, liveByName)

		switch {
		case !statePresent && livePresent && def.status == "MISSING":
			checks = append(checks, platformOrgDoctorCheck{
				Key:      "state.outside-state." + def.address,
				Title:    def.address + " is not being recreated on top of an existing object",
				Status:   failOrWarn(mode.LenientState),
				Detail:   fmt.Sprintf("plan expects create, but live object %s already exists outside terraform state", liveResource.name),
				Hint:     "import the existing object or remove it before applying again",
				Related:  []string{def.address, liveResource.name},
				Blocking: !mode.LenientState,
			})
		case !statePresent && livePresent:
			checks = append(checks, platformOrgDoctorCheck{
				Key:      "state.missing-address." + def.address,
				Title:    def.address + " is tracked in state",
				Status:   failOrWarn(mode.LenientState),
				Detail:   fmt.Sprintf("live managed object %s exists, but terraform state has no address entry", liveResource.name),
				Hint:     "import the live object or run nuke fallback before applying again",
				Related:  []string{def.address, liveResource.name},
				Blocking: !mode.LenientState,
			})
		case statePresent && !livePresent && def.status == "OK":
			checks = append(checks, platformOrgDoctorCheck{
				Key:      "state.deleted-live." + def.address,
				Title:    def.address + " still points to a live object",
				Status:   failOrWarn(mode.LenientState),
				Detail:   fmt.Sprintf("terraform state references %s, but no matching live AWS object was found", stateResource.Name),
				Hint:     "remove or repair the stale state entry before trusting terraform",
				Related:  []string{def.address, stateResource.Name},
				Blocking: !mode.LenientState,
			})
		case statePresent && livePresent:
			if def.name != "" && stateResource.Name != "" && def.name != stateResource.Name && liveResource.name == def.name {
				checks = append(checks, platformOrgDoctorCheck{
					Key:      "state.stale-name." + def.address,
					Title:    def.address + " points at the current physical object",
					Status:   failOrWarn(mode.LenientState),
					Detail:   fmt.Sprintf("state points to %s, but the current planned object is %s", stateResource.Name, def.name),
					Hint:     "refresh or import the current object before applying again",
					Related:  []string{def.address, stateResource.Name, def.name},
					Blocking: !mode.LenientState,
				})
			} else if def.arn != "" && stateResource.ARN != "" && def.arn != stateResource.ARN && liveResource.arn == def.arn {
				checks = append(checks, platformOrgDoctorCheck{
					Key:      "state.stale-arn." + def.address,
					Title:    def.address + " points at the current physical ARN",
					Status:   failOrWarn(mode.LenientState),
					Detail:   fmt.Sprintf("state points to %s, but the current planned ARN is %s", stateResource.ARN, def.arn),
					Hint:     "refresh or import the current object before applying again",
					Related:  []string{def.address, stateResource.ARN, def.arn},
					Blocking: !mode.LenientState,
				})
			}
		}
	}

	var staleAddresses []string
	for address, resource := range stateByAddress {
		if expectedAddresses[address] {
			continue
		}
		if excludeTerraformAuditResource(resource.Type) {
			continue
		}
		staleAddresses = append(staleAddresses, address)
	}
	sort.Strings(staleAddresses)
	if len(staleAddresses) == 0 {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "state.extra-addresses",
			Title:    "terraform state contains only current plan addresses",
			Status:   "ok",
			Detail:   "no extra managed state addresses remain outside the current plan inventory",
			Hint:     "remove state entries that no longer belong to the current plan if they appear",
			Blocking: false,
		})
	} else {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "state.extra-addresses",
			Title:    "terraform state contains only current plan addresses",
			Status:   "warn",
			Detail:   "state contains addresses no longer present in the current plan: " + strings.Join(staleAddresses, ", "),
			Hint:     "review stale state addresses before relying on terraform destroy/apply decisions",
			Related:  staleAddresses,
			Blocking: false,
		})
	}

	return platformOrgDoctorSection{Title: "Terraform State Integrity", Checks: checks}, nil
}

func platformOrgRuntimeDoctorSection(ctx context.Context, mode platformOrgDoctorMode) (platformOrgDoctorSection, error) {
	checks := []platformOrgDoctorCheck{}

	schedule, err := activationSchedule(ctx, d.org)
	if err != nil {
		return platformOrgDoctorSection{}, fmt.Errorf("inspect activation schedule: %w", err)
	}

	expectedLambdaName := activateLambdaName(d.org)
	expectedLambdaARN := fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", d.region, d.accountID, expectedLambdaName)
	expectedScheduleRole := schedulerInvokeRoleName(d.org)
	expectedScheduleRoleARN := fmt.Sprintf("arn:aws:iam::%s:role/%s", d.accountID, expectedScheduleRole)
	expectedTopicARN := fmt.Sprintf("arn:aws:sns:%s:%s:%s", d.region, d.accountID, d.org+"-platform-events")
	expectedLogGroup := activateLambdaLogGroupName(d.org)
	expectedLogPattern := fmt.Sprintf("arn:aws:logs:%s:%s:log-group:%s:*", d.region, d.accountID, expectedLogGroup)

	if schedule == nil {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "runtime.schedule",
			Title:    "activation schedule points to the current Lambda and role",
			Status:   "info",
			Detail:   "no one-time activation schedule currently exists",
			Hint:     "this is expected before apply or after the schedule has already fired",
			Blocking: false,
		})
	} else {
		status := "ok"
		detailParts := []string{"schedule present"}
		if schedule.GroupName != activationScheduleGroupName(d.org) {
			status = "fail"
			detailParts = append(detailParts, "wrong group="+schedule.GroupName)
		}
		if schedule.TargetARN != expectedLambdaARN {
			status = "fail"
			detailParts = append(detailParts, "target="+schedule.TargetARN)
		}
		if schedule.TargetRoleARN != expectedScheduleRoleARN {
			status = "fail"
			detailParts = append(detailParts, "role="+schedule.TargetRoleARN)
		}
		if schedule.State != schedulertypes.ScheduleStateEnabled {
			status = "fail"
			detailParts = append(detailParts, "state="+string(schedule.State))
		}
		if schedule.ActionAfterCompletion != schedulertypes.ActionAfterCompletionDelete {
			status = "fail"
			detailParts = append(detailParts, "after="+string(schedule.ActionAfterCompletion))
		}
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "runtime.schedule",
			Title:    "activation schedule points to the current Lambda and role",
			Status:   status,
			Detail:   strings.Join(detailParts, "  "),
			Hint:     "re-run platform-org apply to rewrite the activation schedule against the current Lambda and scheduler role",
			Related:  []string{activationScheduleName(d.org), expectedLambdaName, expectedScheduleRole},
			Blocking: status == "fail",
		})
	}

	rolePolicyDoc, rolePolicyExists, err := getInlineRolePolicyDocument(ctx, expectedScheduleRole, "invoke-activate-lambda")
	if err != nil {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "runtime.scheduler-role-policy",
			Title:    "scheduler invoke role policy targets the current Lambda",
			Status:   "fail",
			Detail:   err.Error(),
			Hint:     "repair the scheduler invoke role inline policy",
			Related:  []string{expectedScheduleRole},
			Blocking: true,
		})
	} else if !rolePolicyExists {
		status := "info"
		blocking := false
		detail := "scheduler invoke role policy is not present"
		if !mode.AllowMissingRuntime {
			status = "fail"
			blocking = true
		}
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "runtime.scheduler-role-policy",
			Title:    "scheduler invoke role policy targets the current Lambda",
			Status:   status,
			Detail:   detail,
			Hint:     "apply the platform-org stack to create the scheduler invoke role policy",
			Related:  []string{expectedScheduleRole},
			Blocking: blocking,
		})
	} else {
		status := "ok"
		detail := "scheduler invoke role policy references the current Lambda ARN"
		if !strings.Contains(rolePolicyDoc, expectedLambdaARN) {
			status = "fail"
			detail = "scheduler invoke role policy does not reference " + expectedLambdaARN
		}
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "runtime.scheduler-role-policy",
			Title:    "scheduler invoke role policy targets the current Lambda",
			Status:   status,
			Detail:   detail,
			Hint:     "repair the scheduler invoke role policy so it references the current Lambda ARN",
			Related:  []string{expectedScheduleRole, expectedLambdaARN},
			Blocking: status == "fail",
		})
	}

	lambdaRolePolicyDoc, lambdaRolePolicyExists, err := getInlineRolePolicyDocument(ctx, expectedLambdaName, "activate-cost-tags")
	if err != nil {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "runtime.lambda-role-policy",
			Title:    "Lambda role policy references the current SNS topic and log group",
			Status:   "fail",
			Detail:   err.Error(),
			Hint:     "repair the Lambda execution role policy",
			Related:  []string{expectedLambdaName},
			Blocking: true,
		})
	} else if !lambdaRolePolicyExists {
		status := "info"
		blocking := false
		if !mode.AllowMissingRuntime {
			status = "fail"
			blocking = true
		}
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "runtime.lambda-role-policy",
			Title:    "Lambda role policy references the current SNS topic and log group",
			Status:   status,
			Detail:   "Lambda execution role policy is not present",
			Hint:     "apply the platform-org stack to create the Lambda execution role policy",
			Related:  []string{expectedLambdaName},
			Blocking: blocking,
		})
	} else {
		status := "ok"
		detail := "Lambda execution role policy references the current SNS topic and log group pattern"
		if !strings.Contains(lambdaRolePolicyDoc, expectedTopicARN) || !strings.Contains(lambdaRolePolicyDoc, expectedLogPattern) {
			status = "fail"
			detail = "Lambda execution role policy does not reference the current SNS topic ARN and log group ARN pattern"
		}
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "runtime.lambda-role-policy",
			Title:    "Lambda role policy references the current SNS topic and log group",
			Status:   status,
			Detail:   detail,
			Hint:     "repair the Lambda execution role policy so it references the current topic and log group pattern",
			Related:  []string{expectedLambdaName, expectedTopicARN, expectedLogPattern},
			Blocking: status == "fail",
		})
	}

	lambdaOut, err := lambda.NewFromConfig(d.awsCfg).GetFunction(ctx, &lambda.GetFunctionInput{
		FunctionName: sdkaws.String(expectedLambdaName),
	})
	if err != nil {
		if isNotFoundError(err) {
			checks = append(checks, platformOrgDoctorCheck{
				Key:      "runtime.lambda-environment",
				Title:    "activation Lambda points to the current events topic",
				Status:   "info",
				Detail:   "activation Lambda is not present",
				Hint:     "apply the platform-org stack to create the activation Lambda",
				Related:  []string{expectedLambdaName},
				Blocking: false,
			})
		} else {
			checks = append(checks, platformOrgDoctorCheck{
				Key:      "runtime.lambda-environment",
				Title:    "activation Lambda points to the current events topic",
				Status:   "fail",
				Detail:   err.Error(),
				Hint:     "repair Lambda access so the function configuration can be inspected",
				Related:  []string{expectedLambdaName},
				Blocking: true,
			})
		}
	} else {
		actualTopicARN := ""
		if lambdaOut.Configuration != nil && lambdaOut.Configuration.Environment != nil && lambdaOut.Configuration.Environment.Variables != nil {
			actualTopicARN = lambdaOut.Configuration.Environment.Variables["PLATFORM_EVENTS_TOPIC_ARN"]
		}
		status := "ok"
		detail := "Lambda environment points to the current platform events topic"
		if actualTopicARN != expectedTopicARN {
			status = "fail"
			detail = fmt.Sprintf("PLATFORM_EVENTS_TOPIC_ARN=%s, expected %s", actualTopicARN, expectedTopicARN)
		}
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "runtime.lambda-environment",
			Title:    "activation Lambda points to the current events topic",
			Status:   status,
			Detail:   detail,
			Hint:     "repair the Lambda environment so it points to the current platform events topic ARN",
			Related:  []string{expectedLambdaName, expectedTopicARN},
			Blocking: status == "fail",
		})
	}

	logExists, logErr := logGroupExists(ctx, expectedLogGroup)
	var logStatus string
	var logBlocking bool
	switch {
	case logErr != nil:
		logStatus = "fail"
		logBlocking = true
	case logExists:
		logStatus = "ok"
	case mode.AllowMissingRuntime:
		logStatus = "info"
	default:
		logStatus = "fail"
		logBlocking = true
	}
	checks = append(checks, platformOrgDoctorCheck{
		Key:      "runtime.lambda-log-group",
		Title:    "activation Lambda log group matches the current function name",
		Status:   logStatus,
		Detail:   existsDetail(logExists, logErr, "Lambda log group", expectedLogGroup),
		Hint:     "ensure the current Lambda log group exists at " + expectedLogGroup,
		Related:  []string{expectedLogGroup},
		Blocking: logBlocking,
	})

	return platformOrgDoctorSection{Title: "Runtime Wiring", Checks: checks}, nil
}

func platformOrgInventoryDoctorSection(ctx context.Context) (platformOrgDoctorSection, error) {
	discovered, err := scanResourcesFn(ctx)
	if err != nil {
		return platformOrgDoctorSection{}, err
	}
	sections, err := buildAuditSections(ctx, discovered)
	if err != nil {
		return platformOrgDoctorSection{}, err
	}

	checks := []platformOrgDoctorCheck{}
	var driftedExpected []string
	for _, resource := range sections.expected {
		if resource.source == "terraform" && resource.status == "WARN" {
			driftedExpected = append(driftedExpected, resource.address)
		}
	}
	sort.Strings(driftedExpected)
	if len(driftedExpected) == 0 {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "inventory.expected-managed",
			Title:    "expected platform-org resources still match managed ownership tags",
			Status:   "ok",
			Detail:   "no expected platform-org resource is present with broken managed ownership tags",
			Hint:     "repair ownership tags if an expected managed resource becomes warn-level drift",
			Blocking: false,
		})
	} else {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "inventory.expected-managed",
			Title:    "expected platform-org resources still match managed ownership tags",
			Status:   "fail",
			Detail:   "expected platform-org resources have managed ownership drift: " + strings.Join(driftedExpected, ", "),
			Hint:     "repair ownership tags on expected platform-org resources before relying on automated cleanup",
			Related:  driftedExpected,
			Blocking: true,
		})
	}

	if len(sections.extra) == 0 {
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "inventory.extra-owned",
			Title:    "no unexpected platform-org-owned resources remain",
			Status:   "ok",
			Detail:   "no extra Stack=platform-org resources were found outside the current plan/runtime inventory",
			Hint:     "unexpected owned resources should be cleaned up or adopted into the current inventory",
			Blocking: false,
		})
	} else {
		names := make([]string, 0, len(sections.extra))
		for _, resource := range sections.extra {
			names = append(names, resource.resourceType+" "+resource.name)
		}
		sort.Strings(names)
		checks = append(checks, platformOrgDoctorCheck{
			Key:      "inventory.extra-owned",
			Title:    "no unexpected platform-org-owned resources remain",
			Status:   "warn",
			Detail:   "unexpected platform-org-owned drift exists: " + strings.Join(names, ", "),
			Hint:     "review extra platform-org resources before assuming the stack is clean",
			Related:  names,
			Blocking: false,
		})
	}

	bootstrapCount := 0
	for _, resource := range sections.otherManaged {
		if resource.stack == "bootstrap" {
			bootstrapCount++
		}
	}
	status := "info"
	detail := "no bootstrap-managed external dependencies were discovered"
	if bootstrapCount > 0 {
		status = "ok"
		detail = fmt.Sprintf("%d bootstrap-managed dependency resource(s) are present and kept separate from platform-org drift", bootstrapCount)
	}
	checks = append(checks, platformOrgDoctorCheck{
		Key:      "inventory.bootstrap-deps",
		Title:    "bootstrap-managed dependencies stay external to platform-org drift",
		Status:   status,
		Detail:   detail,
		Hint:     "bootstrap-owned resources should remain in Other Managed Resources, not in platform-org drift",
		Blocking: false,
	})

	return platformOrgDoctorSection{Title: "Inventory and Ownership", Checks: checks}, nil
}

func existsStatus(exists bool, err error) string {
	if err != nil || !exists {
		return "fail"
	}
	return "ok"
}

func existsDetail(exists bool, err error, label, name string) string {
	if err != nil {
		return err.Error()
	}
	if !exists {
		return label + " " + name + " is missing"
	}
	return label + " " + name + " is present"
}

func failOrWarn(lenient bool) string {
	if lenient {
		return "warn"
	}
	return "fail"
}

func missingBackendMapKeys(values map[string]string, required ...string) []string {
	var missing []string
	for _, key := range required {
		if strings.TrimSpace(values[key]) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func loadPlatformOrgStateObjects(ctx context.Context, cfg nukeBackendStateConfig) ([]terraformStateObject, error) {
	client := newNukeBackendResetS3ClientFn(d.awsCfg)
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: sdkaws.String(cfg.BucketName),
		Key:    sdkaws.String(cfg.StateKey),
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read terraform state s3://%s/%s: %w", cfg.BucketName, cfg.StateKey, err)
	}
	defer func() { _ = out.Body.Close() }()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("read terraform state body: %w", err)
	}

	var state terraformStateDocument
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse terraform state json: %w", err)
	}

	var objects []terraformStateObject
	for _, resource := range state.Resources {
		if resource.Mode != "managed" {
			continue
		}
		for idx, instance := range resource.Instances {
			address := terraformStateInstanceAddress(resource, idx, instance)
			objects = append(objects, terraformStateObject{
				Address: address,
				Type:    resource.Type,
				Name:    firstStringValue(instance.Attributes, "name", "bucket", "function_name", "url", "id"),
				ARN:     firstStringValue(instance.Attributes, "arn"),
			})
		}
	}
	return objects, nil
}

func terraformStateInstanceAddress(resource terraformStateResource, index int, instance terraformStateInstance) string {
	base := resource.Type + "." + resource.Name
	if resource.Module != "" {
		base = resource.Module + "." + base
	}
	if instance.IndexKey == nil {
		return base
	}
	switch v := instance.IndexKey.(type) {
	case string:
		return base + "[" + strconvQuoteString(v) + "]"
	case float64:
		return base + "[" + strconv.Itoa(int(v)) + "]"
	default:
		return fmt.Sprintf("%s[%d]", base, index)
	}
}

func strconvQuoteString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func getInlineRolePolicyDocument(ctx context.Context, roleName, policyName string) (string, bool, error) {
	client := iam.NewFromConfig(d.awsCfg)
	out, err := client.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
		RoleName:   sdkaws.String(roleName),
		PolicyName: sdkaws.String(policyName),
	})
	if err != nil {
		if isNotFoundError(err) {
			return "", false, nil
		}
		return "", false, err
	}
	decoded, err := url.QueryUnescape(sdkaws.ToString(out.PolicyDocument))
	if err != nil {
		return "", false, fmt.Errorf("decode policy %s/%s: %w", roleName, policyName, err)
	}
	return decoded, true, nil
}

func printPlatformOrgDoctorReport(out *commandOutput, report PlatformOrgDoctorReport) {
	for idx, section := range report.Sections {
		if idx > 0 {
			out.Blank()
		}
		out.Header(section.Title, "")
		rows := make([][]string, 0, len(section.Checks))
		for _, check := range section.Checks {
			hint := check.Hint
			if strings.TrimSpace(hint) == "" {
				hint = "-"
			}
			rows = append(rows, []string{
				platformOrgDoctorStatusCell(check.Status),
				check.Title,
				check.Detail,
				hint,
			})
		}
		_ = out.Table([]string{"STATUS", "CHECK", "DETAIL", "HINT"}, rows)
	}
}

func printPlatformOrgDoctorSummary(out *commandOutput, report PlatformOrgDoctorReport) {
	out.Summary("Integrity Summary",
		countPart("ok", report.Summary.OK),
		countPart("warn", report.Summary.Warn),
		countPart("fail", report.Summary.Fail),
		countPart("info", report.Summary.Info),
	)
}

func platformOrgDoctorStatusCell(status string) string {
	if d.ui == nil {
		return strings.ToUpper(status)
	}
	switch status {
	case "ok":
		return d.ui.Badge("ok", "ok")
	case "warn":
		return d.ui.Badge("warn", "warn")
	case "fail":
		return d.ui.Badge("error", "fail")
	case "info":
		return d.ui.Badge("info", "info")
	default:
		return status
	}
}
