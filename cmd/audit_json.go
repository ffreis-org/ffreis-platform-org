package cmd

// audit_json.go exposes a machine-readable view of the audit result. The
// internal auditSections/auditResource types are unexported with unexported
// fields, so this builds a stable, JSON-tagged projection — the same contract
// scripts and dashboard Lambdas consume (mirrors the doctor --json shape).

type auditResourceJSON struct {
	Status       string   `json:"status"`
	Source       string   `json:"source,omitempty"`
	Address      string   `json:"address,omitempty"`
	ResourceType string   `json:"resource_type"`
	Name         string   `json:"name"`
	ARN          string   `json:"arn,omitempty"`
	Stack        string   `json:"stack,omitempty"`
	Issues       []string `json:"issues,omitempty"`
}

type auditSummaryJSON struct {
	ExpectedOK        int `json:"expected_ok"`
	ExpectedScheduled int `json:"expected_scheduled"`
	ExpectedWarn      int `json:"expected_warn"`
	ExpectedMissing   int `json:"expected_missing"`
	ExtraPlatformOrg  int `json:"extra_platform_org"`
	OtherManaged      int `json:"other_managed"`
	OtherManagedWarn  int `json:"other_managed_warn"`
	Unowned           int `json:"unowned"`
}

// AuditReportJSON is the top-level audit contract: the four resource sections,
// the section summary, and the embedded integrity (doctor) report.
type AuditReportJSON struct {
	Expected     []auditResourceJSON     `json:"expected"`
	Extra        []auditResourceJSON     `json:"extra_platform_org"`
	OtherManaged []auditResourceJSON     `json:"other_managed"`
	Unowned      []auditResourceJSON     `json:"unowned"`
	Summary      auditSummaryJSON        `json:"summary"`
	Integrity    PlatformOrgDoctorReport `json:"integrity"`
}

func toAuditResourceJSON(r auditResource) auditResourceJSON {
	return auditResourceJSON{
		Status:       r.status,
		Source:       r.source,
		Address:      r.address,
		ResourceType: r.resourceType,
		Name:         r.name,
		ARN:          r.arn,
		Stack:        r.stack,
		Issues:       r.issues,
	}
}

func toAuditResourcesJSON(rs []auditResource) []auditResourceJSON {
	out := make([]auditResourceJSON, 0, len(rs))
	for _, r := range rs {
		out = append(out, toAuditResourceJSON(r))
	}
	return out
}

func newAuditReportJSON(sections auditSections, integrity PlatformOrgDoctorReport) AuditReportJSON {
	return AuditReportJSON{
		Expected:     toAuditResourcesJSON(sections.expected),
		Extra:        toAuditResourcesJSON(sections.extra),
		OtherManaged: toAuditResourcesJSON(sections.otherManaged),
		Unowned:      toAuditResourcesJSON(sections.unowned),
		Summary: auditSummaryJSON{
			ExpectedOK:        sections.summary.expectedOK,
			ExpectedScheduled: sections.summary.expectedScheduled,
			ExpectedWarn:      sections.summary.expectedWarn,
			ExpectedMissing:   sections.summary.expectedMissing,
			ExtraPlatformOrg:  sections.summary.extraPlatformOrg,
			OtherManaged:      sections.summary.otherManaged,
			OtherManagedWarn:  sections.summary.otherManagedWarn,
			Unowned:           sections.summary.unowned,
		},
		Integrity: integrity,
	}
}
