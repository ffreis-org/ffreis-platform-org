package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/spf13/cobra"
)

var (
	applyAutoApprove bool
	applyNoActivate  bool
)

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Provision all infrastructure for the given environment",
	Long: `apply runs terraform apply, creating all managed infrastructure.

By default, after a successful apply a one-time EventBridge Scheduler rule is
created that fires the auto-activation Lambda ~25 hours later. The Lambda
activates cost allocation tags once AWS Cost Explorer has had time to discover
the fresh tag keys.

Use --no-activate to skip scheduling if you want to activate manually later
with 'platform-org activate'.`,
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

		out.Header("Platform Org Apply", envAccountRegionSummary(d.env, d.accountID, d.region))
		out.Summary("Context", "stack="+stack, "auto-approve="+strconv.FormatBool(applyAutoApprove))
		out.Blank()

		out.Status("info", "doctor", "running platform-org preflight checks")
		doctorReport, err := platformOrgDoctorRunFn(ctx, platformOrgDoctorModes.apply)
		if err != nil {
			return fmt.Errorf("doctor preflight: %w", err)
		}
		printPlatformOrgDoctorSummary(out, doctorReport)
		if doctorReport.HasFailures() {
			out.Blank()
			printPlatformOrgDoctorReport(out, doctorReport)
			return fmt.Errorf("doctor preflight failed with %d blocking integrity issue(s)", doctorReport.BlockingFailures())
		}
		out.Blank()

		if err := ensureInit(ctx, stack, root, d.env, d.creds); err != nil {
			return fmt.Errorf("terraform init: %w", err)
		}

		d.log.Info("running terraform apply", "env", d.env, "auto_approve", applyAutoApprove)

		args := append([]string{"apply"}, varFileArgs(stack, root, d.env)...)
		if applyAutoApprove {
			args = append(args, "-auto-approve")
		}

		code, err := runTerraform(ctx, runOptions{
			stackPath: stack,
			args:      args,
			creds:     d.creds,
			stdin:     os.Stdin,
		})
		if err != nil {
			return err
		}
		if code != 0 {
			out.Status("info", "hint", "run 'platform-org doctor' to check for state drift or resource conflicts before retrying")
			return fmt.Errorf("terraform apply exited with code %d", code)
		}

		d.log.Info("apply complete")
		out.Blank()
		out.Status("ok", "ok", "terraform apply complete")

		if applyNoActivate {
			out.Status("info", "next", "run 'platform-org activate' after ~24h to enable cost allocation tags")
			out.Status("muted", "note", "AWS Cost Explorer discovers fresh tag keys after ~24h of resource usage")
			return nil
		}

		out.Status("info", "...", "scheduling auto-activation for ~25h from now")
		activateAt, schedErr := scheduleActivation(ctx, d.awsCfg, d.org, d.accountID, d.region)
		if schedErr != nil {
			out.Status("warn", "warn", "could not schedule auto-activation: "+schedErr.Error())
			out.Status("info", "next", "run 'platform-org activate' manually after ~24h")
		} else {
			out.Status("ok", "sched", fmt.Sprintf("auto-activation scheduled for %s UTC", activateAt.UTC().Format("2006-01-02 15:04")))
			out.Status("muted", "note", "Lambda will activate cost allocation tags and notify via email when done")
		}
		return nil
	},
}

// scheduleActivation creates (or updates) a one-time EventBridge Scheduler rule
// that fires the activate Lambda at now+25h. Returns the scheduled fire time.
func scheduleActivation(ctx context.Context, cfg sdkaws.Config, org, accountID, region string) (time.Time, error) {
	activateAt := time.Now().UTC().Add(25 * time.Hour)
	scheduleName := activationScheduleName(org)
	groupName := activationScheduleGroupName(org)
	lambdaARN := fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", region, accountID, activateLambdaName(org))
	roleARN := fmt.Sprintf("arn:aws:iam::%s:role/%s", accountID, schedulerInvokeRoleName(org))
	expr := "at(" + activateAt.Format("2006-01-02T15:04:05") + ")"

	client := scheduler.NewFromConfig(cfg)

	target := &schedulertypes.Target{
		Arn:     sdkaws.String(lambdaARN),
		RoleArn: sdkaws.String(roleARN),
	}
	window := &schedulertypes.FlexibleTimeWindow{
		Mode: schedulertypes.FlexibleTimeWindowModeOff,
	}

	// Try to update an existing schedule first (idempotent across repeated applies).
	_, err := client.UpdateSchedule(ctx, &scheduler.UpdateScheduleInput{
		Name:                  sdkaws.String(scheduleName),
		GroupName:             sdkaws.String(groupName),
		ScheduleExpression:    sdkaws.String(expr),
		FlexibleTimeWindow:    window,
		ActionAfterCompletion: schedulertypes.ActionAfterCompletionDelete,
		Target:                target,
	})
	if err == nil {
		return activateAt, nil
	}

	// ResourceNotFoundException → schedule doesn't exist yet, create it.
	var notFound *schedulertypes.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return time.Time{}, fmt.Errorf("updating schedule: %w", err)
	}

	_, err = client.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:                  sdkaws.String(scheduleName),
		GroupName:             sdkaws.String(groupName),
		ScheduleExpression:    sdkaws.String(expr),
		FlexibleTimeWindow:    window,
		ActionAfterCompletion: schedulertypes.ActionAfterCompletionDelete,
		Target:                target,
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("creating schedule: %w", err)
	}
	return activateAt, nil
}

func init() {
	applyCmd.Flags().BoolVar(&applyAutoApprove, "auto-approve", false, "Skip interactive approval")
	applyCmd.Flags().BoolVar(&applyNoActivate, "no-activate", false, "Skip scheduling auto-activation after apply")
	rootCmd.AddCommand(applyCmd)
}
