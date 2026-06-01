package cmd

import (
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/spf13/cobra"

	"github.com/FelipeFuhr/ffreis-platform-inventory/pkg/org"
)

var accountsJSON bool

var accountsCmd = &cobra.Command{
	Use:     "accounts",
	Aliases: []string{"org"},
	Short:   "Show the AWS Organizations structure (accounts, OUs, SCPs)",
	Long: `Read the AWS Organizations tree — the master account, roots,
organizational units, member accounts, and service control policies — into a
structured view. This surfaces org structure that was previously only visible
buried inside 'audit'. Use --json for the machine-readable contract.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()

		client := organizations.NewFromConfig(d.awsCfg)
		tree, err := org.Describe(ctx, client)
		if err != nil {
			return err
		}

		if accountsJSON {
			return writeJSON(cmd.OutOrStdout(), tree)
		}

		out := newCommandOutput(cmd, d.ui)
		out.Header("AWS Organization", envAccountRegionSummary(d.env, d.accountID, d.region))
		out.Summary("Org", "id="+orEmpty(tree.OrgID), "master="+orEmpty(tree.MasterAccount))

		out.Blank()
		out.Header(fmt.Sprintf("Accounts (%d)", len(tree.Accounts)), "")
		for _, a := range tree.Accounts {
			out.Line(fmt.Sprintf("  %-14s %-24s %s", a.ID, a.Name, a.Status))
		}

		out.Blank()
		out.Header(fmt.Sprintf("Organizational Units (%d)", len(tree.OUs)), "")
		for _, ou := range tree.OUs {
			out.Line(fmt.Sprintf("  %-14s %s", ou.ID, ou.Name))
		}

		out.Blank()
		out.Header(fmt.Sprintf("Service Control Policies (%d)", len(tree.SCPs)), "")
		for _, p := range tree.SCPs {
			marker := "custom"
			if p.AWSManaged {
				marker = "aws-managed"
			}
			out.Line(fmt.Sprintf("  %-26s [%s] %s", p.Name, marker, p.Description))
		}
		return nil
	},
}

func orEmpty(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

func init() {
	accountsCmd.Flags().BoolVar(&accountsJSON, "json", false, "Emit machine-readable JSON")
	rootCmd.AddCommand(accountsCmd)
}
