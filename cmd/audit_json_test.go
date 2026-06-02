package cmd

import (
	"encoding/json"
	"testing"
)

func TestNewAuditReportJSON(t *testing.T) {
	sections := auditSections{
		expected: []auditResource{{status: "OK", resourceType: "s3", name: "b", arn: "arn:aws:s3:::b", issues: []string{"x"}}},
		unowned:  []auditResource{{status: "UNOWNED", resourceType: "iam/role", name: "r"}},
		summary:  auditSectionSummary{expectedOK: 1, unowned: 1},
	}
	integrity := PlatformOrgDoctorReport{Mode: "audit", Summary: platformOrgDoctorSummary{OK: 2, Total: 2}}

	rep := newAuditReportJSON(sections, integrity)
	if len(rep.Expected) != 1 || rep.Expected[0].ResourceType != "s3" || rep.Expected[0].Issues[0] != "x" {
		t.Errorf("expected section = %+v", rep.Expected)
	}
	if len(rep.Unowned) != 1 || rep.Summary.ExpectedOK != 1 || rep.Summary.Unowned != 1 {
		t.Errorf("summary/unowned = %+v / %+v", rep.Summary, rep.Unowned)
	}
	if rep.Integrity.Mode != "audit" {
		t.Errorf("integrity = %+v", rep.Integrity)
	}

	// Round-trips through JSON with the documented keys.
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"expected", "extra_platform_org", "other_managed", "unowned", "summary", "integrity"} {
		if _, ok := back[k]; !ok {
			t.Errorf("missing top-level key %q in JSON", k)
		}
	}
}

func TestAuditHasJSONFlag(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"audit"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Flags().Lookup("json") == nil {
		t.Error("audit missing --json flag")
	}
}
