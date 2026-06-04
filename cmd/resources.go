package cmd

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/spf13/cobra"

	"github.com/FelipeFuhr/ffreis-platform-inventory/pkg/resources"
	"github.com/FelipeFuhr/ffreis-platform-inventory/pkg/responsibility"
)

var (
	resourcesJSON        bool
	resourcesGroupBy     string
	resourcesCostCenter  string
	resourcesLayer       string
	resourcesDomain      string
	resourcesProject     string
	resourcesEnvironment string
)

var resourcesCmd = &cobra.Command{
	Use:     "resources",
	Aliases: []string{"inventory"},
	Short:   "List tagged resources grouped by responsibility (CostCenter, Layer, Domain, …)",
	Long: `Enumerate tag-bearing AWS resources in the current region and group them
by a responsibility dimension. Filter to a single responsibility with the
--cost-center/--layer/--domain/--project/--environment flags. This is the
"objects that are tagged and under certain responsibilities" view. Use --json
for the machine-readable contract dashboard Lambdas consume.

Note: the AWS tagging API is regional — this lists resources in --region only.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()

		// Adapter: wrap the tagging client as the audit.PageFetcher the library
		// expects (ports-and-adapters — the library owns no AWS client).
		fetch := func(c context.Context, in *resourcegroupstaggingapi.GetResourcesInput) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
			return d.tagging.GetResources(c, in)
		}

		view, err := resources.Build(ctx, fetch, resources.Options{
			Env:     d.env,
			GroupBy: responsibility.Dimension(resourcesGroupBy),
			Filter: responsibility.Filter{
				CostCenter:  resourcesCostCenter,
				Layer:       resourcesLayer,
				Domain:      resourcesDomain,
				Project:     resourcesProject,
				Environment: resourcesEnvironment,
			},
		})
		if err != nil {
			return err
		}

		if resourcesJSON {
			return writeJSON(cmd.OutOrStdout(), view)
		}

		out := newCommandOutput(cmd, d.ui)
		out.Header("Tagged Resources by Responsibility", envAccountRegionSummary(d.env, d.accountID, d.region))
		out.Summary("Scope",
			"group-by="+string(view.GroupBy),
			fmt.Sprintf("total=%d", view.Summary.Total),
			fmt.Sprintf("owned=%d", view.Summary.Owned),
			fmt.Sprintf("unowned=%d", view.Summary.Unowned),
		)

		for _, key := range view.Responsibilities() {
			group := view.ByResponsibility[key]
			out.Blank()
			out.Header(fmt.Sprintf("%s (%d)", key, len(group)), "")
			const maxRows = 25
			for i, r := range group {
				if i >= maxRows {
					out.Line(fmt.Sprintf("  … %d more", len(group)-maxRows))
					break
				}
				out.Line(fmt.Sprintf("  %-26s %s", r.ResourceType, r.Name))
			}
		}
		return nil
	},
}

func init() {
	f := resourcesCmd.Flags()
	f.BoolVar(&resourcesJSON, "json", false, "Emit machine-readable JSON")
	f.StringVar(&resourcesGroupBy, "group-by", string(responsibility.CostCenter),
		"Responsibility dimension to group by: CostCenter, Layer, Domain, Project, Environment, Stack")
	f.StringVar(&resourcesCostCenter, "cost-center", "", "Filter to this CostCenter")
	f.StringVar(&resourcesLayer, "layer", "", "Filter to this Layer")
	f.StringVar(&resourcesDomain, "domain", "", "Filter to this Domain")
	f.StringVar(&resourcesProject, "project", "", "Filter to this Project")
	f.StringVar(&resourcesEnvironment, "environment", "", "Filter to this Environment")
	rootCmd.AddCommand(resourcesCmd)
}
