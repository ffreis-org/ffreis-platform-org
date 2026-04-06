package cmd

import (
	"strings"
	"testing"
)

func TestPlatformOrgDoctorStatusCellPlainMode(t *testing.T) {
	oldUI := d.ui
	d.ui = nil
	t.Cleanup(func() { d.ui = oldUI })

	tests := []struct {
		status string
		want   string
	}{
		{"ok", "OK"},
		{"warn", "WARN"},
		{"fail", "FAIL"},
		{"info", "INFO"},
		{"custom", "CUSTOM"},
	}
	for _, tc := range tests {
		got := platformOrgDoctorStatusCell(tc.status)
		if got != tc.want {
			t.Errorf("platformOrgDoctorStatusCell(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestBlockingFailuresCountsOnlyBlockingFails(t *testing.T) {
	report := PlatformOrgDoctorReport{
		Sections: []platformOrgDoctorSection{
			{
				Checks: []platformOrgDoctorCheck{
					{Status: "fail", Blocking: true},
					{Status: "fail", Blocking: false},
					{Status: "ok", Blocking: true},
				},
			},
		},
	}
	if got := report.BlockingFailures(); got != 1 {
		t.Fatalf("BlockingFailures() = %d, want 1", got)
	}
	if !report.HasFailures() {
		t.Fatal("HasFailures() should be true with one blocking failure")
	}
}

func TestBlockingFailuresZeroWhenNoBlockingFails(t *testing.T) {
	report := PlatformOrgDoctorReport{
		Sections: []platformOrgDoctorSection{
			{
				Checks: []platformOrgDoctorCheck{
					{Status: "fail", Blocking: false},
					{Status: "ok", Blocking: true},
				},
			},
		},
	}
	if got := report.BlockingFailures(); got != 0 {
		t.Fatalf("BlockingFailures() = %d, want 0", got)
	}
	if report.HasFailures() {
		t.Fatal("HasFailures() should be false with no blocking failures")
	}
}

func TestPlatformOrgDoctorStatusCellUnknownStatusReturnsRaw(t *testing.T) {
	oldUI := d.ui
	d.ui = nil
	t.Cleanup(func() { d.ui = oldUI })

	got := platformOrgDoctorStatusCell("SOMETHING")
	if !strings.Contains(got, "SOMETHING") {
		t.Fatalf("expected raw status to be preserved, got %q", got)
	}
}
