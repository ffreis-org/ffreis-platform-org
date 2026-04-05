package cmd

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/ffreis/platform-org/internal/activation"
)

// activateFn is injectable for testing.
var activateFn = func(ctx context.Context) error {
	return activation.Activate(ctx, d.ce)
}

var activateCmd = &cobra.Command{
	Use:   "activate",
	Short: "Activate deferred steps that require account warm-up (e.g. cost allocation tags)",
	Long: `activate performs the steps that were intentionally deferred by 'apply' because
they require AWS to have processed account activity for ~24 hours first.

Current deferred steps:
  - Cost allocation tags (Stack, Project, Layer, Owner, Environment)
    AWS Cost Explorer only discovers tag keys after they have been used on at
    least one resource for ~24 hours. Run this command the day after the first
    'platform-org apply', or wait for the auto-activation Lambda to run.

It is safe to run 'activate' repeatedly — it is idempotent. If AWS is not
ready yet it will print a clear message and exit cleanly.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		out := newCommandOutput(cmd, d.ui)

		out.Header("Platform Org Activate", envAccountRegionSummary(d.env, d.accountID, d.region))
		out.Blank()

		d.log.Info("running activate", "env", d.env)

		if err := activateFn(ctx); err != nil {
			var notReady *activation.ErrNotReady
			if errors.As(err, &notReady) {
				out.Status("warn", "wait", "cost allocation tags are not ready yet")
				out.Status("info", "next", "re-run 'platform-org activate' tomorrow after Cost Explorer discovers the tag keys")
				return nil
			}
			return err
		}

		out.Blank()
		out.Status("ok", "ok", "cost allocation tags are now active in Cost Explorer")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(activateCmd)
}
