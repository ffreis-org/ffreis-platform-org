package cmd

import (
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/service/budgets"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/spf13/cobra"

	"github.com/FelipeFuhr/ffreis-platform-inventory/pkg/cost"
	"github.com/FelipeFuhr/ffreis-platform-inventory/pkg/responsibility"
)

var (
	costJSON bool
	costDays int
)

// costExplorerRegion is the global endpoint Cost Explorer and Budgets are hosted
// in; clients must target it regardless of the operating region.
const costExplorerRegion = "us-east-1"

var costCmd = &cobra.Command{
	Use:   "cost",
	Short: "Report AWS spend broken down by responsibility (CostCenter, Project, Domain)",
	Long: `Report trailing-window AWS spend grouped by the responsibility tags
defined by modules/tagging (CostCenter, Project, Domain), plus the configured
budgets and which cost-allocation tags are active.

Cost Explorer charges ~$0.01 per call; this command issues a handful of calls
on demand. Use --json for a machine-readable contract (the same shape dashboard
Lambdas consume).`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()

		ce := costexplorer.NewFromConfig(d.awsCfg, func(o *costexplorer.Options) {
			o.Region = costExplorerRegion
		})
		bg := budgets.NewFromConfig(d.awsCfg, func(o *budgets.Options) {
			o.Region = costExplorerRegion
		})

		report, err := cost.Full(ctx, ce, bg, d.accountID, cost.Options{Days: costDays})
		if err != nil {
			return err
		}

		if costJSON {
			return writeJSON(cmd.OutOrStdout(), report)
		}

		out := newCommandOutput(cmd, d.ui)
		out.Header("Platform Cost by Responsibility", envAccountRegionSummary(d.env, d.accountID, d.region))
		out.Summary("Window", report.Start+" → "+report.End, fmt.Sprintf("total=$%.2f", report.TotalUSD))

		renderCostBreakdown(out, "By Cost Center", report.ByTag[responsibility.CostCenter])
		renderCostBreakdown(out, "By Project", report.ByTag[responsibility.Project])
		renderCostBreakdown(out, "By Domain", report.ByTag[responsibility.Domain])
		renderCostBreakdown(out, "Top Services", report.ByService)

		out.Blank()
		out.Header("Budgets & Cost Tags", "")
		if len(report.Budgets) == 0 {
			out.Status("warn", "warn", "no budgets configured")
		}
		for _, b := range report.Budgets {
			out.Status("ok", "ok", fmt.Sprintf("budget %s  $%s/month", b.Name, b.LimitUSD))
		}
		if len(report.ActiveCostTags) == 0 {
			out.Status("warn", "warn", "no active cost-allocation tags; run 'platform-org activate' (breakdowns stay empty until active)")
		} else {
			out.Status("ok", "ok", "active cost tags: "+joinSorted(report.ActiveCostTags))
		}
		return nil
	},
}

// renderCostBreakdown prints a value→USD map sorted by spend descending, capped
// at the highest contributors so the human view stays scannable.
func renderCostBreakdown(out *commandOutput, title string, byValue map[string]float64) {
	out.Blank()
	out.Header(title, "")
	if len(byValue) == 0 {
		out.Status("warn", "warn", "no data")
		return
	}
	type kv struct {
		name string
		usd  float64
	}
	pairs := make([]kv, 0, len(byValue))
	for k, v := range byValue {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].usd != pairs[j].usd {
			return pairs[i].usd > pairs[j].usd
		}
		return pairs[i].name < pairs[j].name
	})
	const maxRows = 10
	for i, p := range pairs {
		if i >= maxRows {
			out.Line(fmt.Sprintf("  … %d more", len(pairs)-maxRows))
			break
		}
		out.Line(fmt.Sprintf("  %-28s $%8.2f", p.name, p.usd))
	}
}

func joinSorted(values []string) string {
	cp := append([]string(nil), values...)
	sort.Strings(cp)
	result := ""
	for i, v := range cp {
		if i > 0 {
			result += ", "
		}
		result += v
	}
	return result
}

func init() {
	costCmd.Flags().BoolVar(&costJSON, "json", false, "Emit machine-readable JSON")
	costCmd.Flags().IntVar(&costDays, "days", 7, "Trailing window length in days")
	rootCmd.AddCommand(costCmd)
}
