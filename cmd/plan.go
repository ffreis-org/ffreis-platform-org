package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Run terraform plan for the given environment",
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

		out.Header("Platform Org Plan", envAccountRegionSummary(d.env, d.accountID, d.region))
		out.Summary("Context", "stack="+stack)
		out.Blank()

		if err := ensureInit(ctx, stack, root, d.env, d.creds); err != nil {
			return fmt.Errorf("terraform init: %w", err)
		}

		d.log.Info("running terraform plan", "env", d.env, "stack", stack)

		args := append([]string{"plan", "-detailed-exitcode"}, varFileArgs(stack, root, d.env)...)
		code, err := runTerraform(ctx, runOptions{
			stackPath: stack,
			args:      args,
			creds:     d.creds,
			stdin:     os.Stdin,
		})
		if err != nil {
			return err
		}

		switch code {
		case 0:
			d.log.Info("plan complete: no changes")
			out.Blank()
			out.Status("ok", "ok", "terraform plan complete; no changes detected")
		case 2:
			d.log.Info("plan complete: changes detected")
			out.Blank()
			out.Status("warn", "plan", "terraform plan complete; changes detected")
		default:
			return fmt.Errorf("terraform plan exited with code %d", code)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(planCmd)
}
