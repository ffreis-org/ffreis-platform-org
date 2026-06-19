package cmd

import (
	"sort"
	"strings"

	"github.com/FelipeFuhr/ffreis-platform-cli/pkg/audit"
	"github.com/FelipeFuhr/ffreis-platform-cli/pkg/inventory"
)

// costAllocationTagKeys are the tags every billable resource must carry so its
// spend is attributable in Cost Explorer and catchable by the per-product
// budgets. CostCenter is the one the budgets filter on (see the budget stacks);
// the rest are the core schema the classification engine already enforces.
// Defined here (not in platform-cli's CoreTagKeys) because CoreTagKeys omits
// CostCenter and we need the cost dimension specifically.
var costAllocationTagKeys = append([]string{"CostCenter"}, inventory.CoreTagKeys...)

// untaggableARNFragments mark resource ARNs that cannot carry arbitrary
// cost-allocation tags, so flagging them as "uncovered" would be a permanent
// false positive. Matched as a substring of the ARN.
var untaggableARNFragments = []string{
	":payments:", // billing payment instruments — not a deployable resource
}

// TagGap is a single resource missing one or more required cost-allocation tags.
type TagGap struct {
	ResourceType string   `json:"resource_type"`
	Name         string   `json:"name"`
	ARN          string   `json:"arn"`
	Missing      []string `json:"missing"`
}

// CoverageSummary tallies a tag-coverage scan.
type CoverageSummary struct {
	Total     int `json:"total"`     // taggable resources scanned
	Covered   int `json:"covered"`   // carry every required tag
	Uncovered int `json:"uncovered"` // missing >= 1 required tag
	Skipped   int `json:"skipped"`   // inherently-untaggable resources excluded
}

// CoverageView is the machine-readable tag-coverage report consumed by --json
// (and, in time, the dashboard Lambdas).
type CoverageView struct {
	Required []string        `json:"required"`
	Gaps     []TagGap        `json:"gaps"`
	Summary  CoverageSummary `json:"summary"`
}

// isUntaggable reports whether a resource cannot meaningfully carry the required
// cost-allocation tags and should be excluded from the coverage tally.
func isUntaggable(r audit.Resource) bool {
	for _, frag := range untaggableARNFragments {
		if strings.Contains(r.ARN, frag) {
			return true
		}
	}
	return false
}

// computeTagCoverage classifies every scanned resource as covered (carries all
// required tags), uncovered (missing >= 1), or skipped (untaggable). It is pure
// so it can be unit-tested without an AWS client.
func computeTagCoverage(rs []audit.Resource, required []string) CoverageView {
	view := CoverageView{Required: required}
	for _, r := range rs {
		if isUntaggable(r) {
			view.Summary.Skipped++
			continue
		}
		view.Summary.Total++
		missing := inventory.MissingTags(r.Tags, required)
		if len(missing) == 0 {
			view.Summary.Covered++
			continue
		}
		view.Summary.Uncovered++
		view.Gaps = append(view.Gaps, TagGap{
			ResourceType: r.ResourceType,
			Name:         r.Name,
			ARN:          r.ARN,
			Missing:      missing,
		})
	}
	sort.Slice(view.Gaps, func(i, j int) bool {
		if view.Gaps[i].ResourceType != view.Gaps[j].ResourceType {
			return view.Gaps[i].ResourceType < view.Gaps[j].ResourceType
		}
		return view.Gaps[i].Name < view.Gaps[j].Name
	})
	return view
}
