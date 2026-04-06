package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/spf13/cobra"
)

var nukeForce = true
var nukeBackupDir string

var nukeCmd = &cobra.Command{
	Use:   "nuke",
	Short: "Destroy all resources in the given environment (IRREVERSIBLE)",
	Long: `nuke runs terraform destroy after an explicit confirmation prompt.

NOTE: The state bucket and lock table in this stack have force_destroy = false.
Empty those S3 buckets before running nuke, or the destroy will fail.

NOTE: Always run 'platform-org nuke' before destroying bootstrap.
Destroying bootstrap first removes the S3 state backend, which prevents clean
terraform teardown. If that happens, nuke will detect the missing backend and
fall back to direct AWS SDK cleanup automatically.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		out := newCommandOutput(cmd, d.ui)

		root, err := repoRoot()
		if err != nil {
			return err
		}
		stack, err := stackDir()
		if err != nil {
			return err
		}

		backendExists, err := checkStateBackendExistsFn(ctx, d.awsCfg, d.org)
		if err != nil {
			d.log.Warn("could not check state backend existence", "err", err)
			backendExists = true // fail-safe: let terraform surface the real error
		}

		backupPlan, err := inspectRuntimeStateStoresForNukeFn(ctx, d.awsCfg, d.org)
		if err != nil {
			return fmt.Errorf("inspect runtime state stores: %w", err)
		}
		backupDir := nukeBackupDir
		if backupDir == "" && backupPlan.hasData() {
			backupDir = defaultRuntimeBackupDirForNukeFn(root, d.env)
		}

		confirm := "destroy-" + d.env
		if backupPlan.hasData() && nukeBackupDir == "" {
			confirm = "backup-destroy-" + d.env
		}
		if d.ui != nil {
			out.ErrLine(d.ui.Header("Platform Org Nuke", envAccountRegionSummary(d.env, d.accountID, d.region)))
			out.ErrStatus("warn", "warn", "this will permanently destroy all resources in env "+strconv.Quote(d.env))
		} else {
			out.ErrLine("WARNING: This will permanently destroy all resources in env " + strconv.Quote(d.env) + ".")
		}
		out.ErrLine("Resources impacted:")
		out.ErrLine("  - Terraform stack:  " + stack)
		out.ErrLine("  - Runtime S3 state bucket")
		out.ErrLine("  - Runtime DynamoDB lock table")
		out.ErrLine("  - Any managed resources referenced by this environment")
		if backupPlan.hasData() {
			out.ErrLine("Detected stateful data that would otherwise be lost:")
			for _, line := range backupPlan.summaryLines() {
				out.ErrLine("  - " + line)
			}
			out.ErrLine("A local backup will be written before destroy:")
			out.ErrLine("  - " + backupDir)
		} else {
			out.ErrLine("No runtime S3 object versions or DynamoDB items were detected.")
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\nType %q to confirm: ", confirm)

		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return fmt.Errorf("no input received")
		}
		if strings.TrimSpace(scanner.Text()) != confirm {
			if d.ui != nil {
				out.ErrStatus("muted", "skip", "operator confirmation did not match")
			} else {
				out.ErrLine("Cancelled.")
			}
			return nil
		}

		if backupPlan.hasData() {
			out.Status("info", "backup", "writing local state backup to "+backupDir)
			if err := backupRuntimeStateStoresForNukeFn(ctx, d.awsCfg, d.org, backupDir, backupPlan); err != nil {
				return fmt.Errorf("backup runtime state stores: %w", err)
			}
			out.Status("ok", "backup", "runtime state backup written")
		}

		out.Status("info", "doctor", "running platform-org preflight checks")
		doctorReport, err := platformOrgDoctorRunFn(ctx, platformOrgDoctorModes.nuke)
		if err != nil {
			return fmt.Errorf("doctor preflight: %w", err)
		}
		printPlatformOrgDoctorSummary(out, doctorReport)
		if doctorReport.HasFailures() {
			out.Blank()
			printPlatformOrgDoctorReport(out, doctorReport)
			return fmt.Errorf("doctor preflight failed with %d blocking integrity issue(s)", doctorReport.BlockingFailures())
		}

		cleanupMessages, err := cleanupInventorySourcesForNuke(ctx)
		if err != nil {
			return fmt.Errorf("cleaning pending runtime resources: %w", err)
		}
		if len(cleanupMessages) == 0 {
			out.Status("muted", "cleanup", "no pending first-class runtime resources found")
		} else {
			for _, msg := range cleanupMessages {
				out.Status("ok", "cleanup", msg)
			}
		}

		if !backendExists {
			out.Status("warn", "backend", fmt.Sprintf("Terraform state backend not found (%s-tf-state-root) — bootstrap was likely destroyed before this stack", d.org))
			out.Status("info", "recover", "skipping terraform; falling back to direct AWS SDK cleanup")
			return fallbackNukeAfterTerraformFailure(ctx, out, root, stack, backupDir,
				fmt.Errorf("state backend missing: %s-tf-state-root S3 bucket not found", d.org))
		}
		if err := ensureInitForNukeFn(ctx, stack, root, d.env, d.creds); err != nil {
			return fallbackNukeAfterTerraformFailure(ctx, out, root, stack, backupDir, fmt.Errorf("terraform init: %w", err))
		}

		d.log.Info("running terraform destroy", "env", d.env, "force", nukeForce)

		args := append([]string{"destroy"}, varFileArgs(stack, root, d.env)...)
		if nukeForce {
			args = append(args, "-auto-approve")
		}
		code, err := runTerraformForNukeFn(ctx, runOptions{
			stackPath: stack,
			args:      args,
			creds:     d.creds,
			stdin:     os.Stdin,
		})
		if err != nil {
			return fallbackNukeAfterTerraformFailure(ctx, out, root, stack, backupDir, fmt.Errorf("terraform destroy: %w", err))
		}
		if code != 0 {
			return fallbackNukeAfterTerraformFailure(ctx, out, root, stack, backupDir, fmt.Errorf("terraform destroy exited with code %d", code))
		}

		remaining, err := platformOrgCleanupTargetsForNukeFn(ctx)
		if err != nil {
			return fallbackNukeAfterTerraformFailure(ctx, out, root, stack, backupDir, fmt.Errorf("verify post-destroy resources: %w", err))
		}
		if len(remaining) > 0 {
			return fallbackNukeAfterTerraformFailure(ctx, out, root, stack, backupDir, fmt.Errorf("terraform destroy completed but %d managed platform-org resource(s) remain", len(remaining)))
		}

		d.log.Info("nuke complete")
		out.Blank()
		out.Status("ok", "ok", "terraform destroy complete")
		return nil
	},
}

func init() {
	nukeCmd.Flags().BoolVar(&nukeForce, "force", true, "Skip Terraform's final approval prompt")
	nukeCmd.Flags().StringVar(&nukeBackupDir, "backup-dir", "", "Directory for local runtime-state backups before destroy (defaults under the repo when data exists)")
	rootCmd.AddCommand(nukeCmd)
}

func deletePendingSchedules(ctx context.Context, org string) ([]string, error) {
	groupName := activationScheduleGroupName(org)
	var removed []string
	var nextToken *string
	for {
		out, err := listSchedulesFn(ctx, &scheduler.ListSchedulesInput{
			GroupName: sdkaws.String(groupName),
			NextToken: nextToken,
		})
		if err != nil {
			if isNotFoundError(err) {
				return removed, nil
			}
			return nil, err
		}
		pageRemoved, err := deleteSchedulePage(ctx, groupName, out.Schedules)
		if err != nil {
			return nil, err
		}
		removed = append(removed, pageRemoved...)
		if sdkaws.ToString(out.NextToken) == "" {
			break
		}
		nextToken = out.NextToken
	}
	return removed, nil
}

func deleteSchedulePage(ctx context.Context, groupName string, schedules []schedulertypes.ScheduleSummary) ([]string, error) {
	removed := make([]string, 0, len(schedules))
	for _, scheduleSummary := range schedules {
		name := sdkaws.ToString(scheduleSummary.Name)
		if name == "" {
			continue
		}
		if err := deletePendingSchedule(ctx, groupName, name); err != nil {
			return nil, err
		}
		removed = append(removed, name)
	}
	return removed, nil
}

func deletePendingSchedule(ctx context.Context, groupName, name string) error {
	_, err := deleteScheduleFn(ctx, &scheduler.DeleteScheduleInput{
		GroupName: sdkaws.String(groupName),
		Name:      sdkaws.String(name),
	})
	if err != nil && !isNotFoundError(err) {
		return err
	}
	return nil
}

func cleanupInventorySourcesForNuke(ctx context.Context) ([]string, error) {
	var messages []string
	for _, source := range inventorySourcesFn() {
		removed, err := source.cleanupNuke(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s cleanup: %w", source.sourceID(), err)
		}
		for _, item := range removed {
			messages = append(messages, fmt.Sprintf("%s: %s", source.sourceID(), item))
		}
	}
	return messages, nil
}
