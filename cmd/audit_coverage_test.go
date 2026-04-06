package cmd

import (
	"sort"
	"strings"
	"testing"
)

const (
	testAuditType  = "a-type"
	testAuditName  = "a-name"
	testAuditStack = "a-stack"
	testAuditZType = "z-type"
)

// --- naming helpers ---

func TestActivateLambdaName(t *testing.T) {
	got := activateLambdaName("myorg")
	if got != "myorg"+activateCostTagsSuffix {
		t.Fatalf("activateLambdaName: %q", got)
	}
}

func TestActivateLambdaLogGroupName(t *testing.T) {
	got := activateLambdaLogGroupName("myorg")
	if !strings.HasPrefix(got, "/aws/lambda/") || !strings.Contains(got, "myorg") {
		t.Fatalf("activateLambdaLogGroupName: %q", got)
	}
}

func TestSchedulerInvokeRoleName(t *testing.T) {
	got := schedulerInvokeRoleName("myorg")
	if !strings.Contains(got, "myorg") || !strings.Contains(got, "scheduler") {
		t.Fatalf("schedulerInvokeRoleName: %q", got)
	}
}

func TestPlatformAdminBudgetName(t *testing.T) {
	got := platformAdminBudgetName("myorg")
	if !strings.Contains(got, "myorg") || !strings.Contains(got, "budget") {
		t.Fatalf("platformAdminBudgetName: %q", got)
	}
}

func TestBootstrapLayerGroupName(t *testing.T) {
	got := bootstrapLayerGroupName("myorg")
	if !strings.Contains(got, "myorg") || !strings.Contains(got, "bootstrap") {
		t.Fatalf("bootstrapLayerGroupName: %q", got)
	}
}

// --- expectedStatusRank ---

func TestExpectedStatusRankAllBranches(t *testing.T) {
	tests := []struct {
		status string
		want   int
	}{
		{"OK", 0},
		{"SCHEDULED", 1},
		{"WARN", 2},
		{"MISSING", 3},
		{"UNKNOWN", 4},
		{"", 4},
	}
	for _, tc := range tests {
		got := expectedStatusRank(tc.status)
		if got != tc.want {
			t.Errorf("expectedStatusRank(%q) = %d, want %d", tc.status, got, tc.want)
		}
	}
}

// --- auditStatusCell (nil UI — plain mode) ---

func TestAuditStatusCellPlainMode(t *testing.T) {
	oldUI := d.ui
	d.ui = nil
	t.Cleanup(func() { d.ui = oldUI })

	tests := []struct {
		status string
		want   string
	}{
		{"OK", "OK"},
		{"SCHEDULED", "SCHEDULED"},
		{"WARN", "WARN"},
		{"UNOWNED", "UNOWNED"},
		{"MISSING", "MISSING"},
		{"CUSTOM", "CUSTOM"},
	}
	for _, tc := range tests {
		got := auditStatusCell(tc.status)
		if got != tc.want {
			t.Errorf("auditStatusCell(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

// --- sortOtherManagedResources ---

func TestSortOtherManagedResources(t *testing.T) {
	resources := []auditResource{
		{stack: "z-stack", resourceType: testAuditType, name: "b-name"},
		{stack: testAuditStack, resourceType: testAuditZType, name: testAuditName},
		{stack: testAuditStack, resourceType: testAuditType, name: "z-name"},
		{stack: testAuditStack, resourceType: testAuditType, name: testAuditName},
	}
	sortOtherManagedResources(resources)
	// First by stack ascending, then type ascending, then name ascending.
	if resources[0].stack != testAuditStack || resources[0].resourceType != testAuditType || resources[0].name != testAuditName {
		t.Errorf("unexpected first element: %+v", resources[0])
	}
	if resources[len(resources)-1].stack != "z-stack" {
		t.Errorf("unexpected last element: %+v", resources[len(resources)-1])
	}
	if !sort.SliceIsSorted(resources, func(i, j int) bool {
		if resources[i].stack != resources[j].stack {
			return resources[i].stack < resources[j].stack
		}
		if resources[i].resourceType != resources[j].resourceType {
			return resources[i].resourceType < resources[j].resourceType
		}
		return resources[i].name < resources[j].name
	}) {
		t.Errorf("resources not sorted: %+v", resources)
	}
}

// --- sortUnownedResources ---

func TestSortUnownedResources(t *testing.T) {
	resources := []auditResource{
		{resourceType: testAuditZType, name: testAuditName},
		{resourceType: testAuditType, name: "z-name"},
		{resourceType: testAuditType, name: testAuditName},
	}
	sortUnownedResources(resources)
	if resources[0].resourceType != testAuditType || resources[0].name != testAuditName {
		t.Errorf("unexpected first element: %+v", resources[0])
	}
	if resources[len(resources)-1].resourceType != testAuditZType {
		t.Errorf("unexpected last element: %+v", resources[len(resources)-1])
	}
}

// --- equalStringSlices ---

func TestEqualStringSlicesSameLengthDifferentValues(t *testing.T) {
	if equalStringSlices([]string{"a", "b"}, []string{"a", "c"}) {
		t.Fatal("expected slices to be unequal")
	}
}

func TestEqualStringSlicesBothEmpty(t *testing.T) {
	if !equalStringSlices([]string{}, []string{}) {
		t.Fatal("expected empty slices to be equal")
	}
}

func TestEqualStringSlicesDifferentLengths(t *testing.T) {
	if equalStringSlices([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("expected different-length slices to be unequal")
	}
}

// --- hasStringMap ---

func TestHasStringMapWithNil(t *testing.T) {
	if hasStringMap(nil) {
		t.Fatal("nil should not be a string map")
	}
}

func TestHasStringMapWithNonMapType(t *testing.T) {
	if hasStringMap("string-value") {
		t.Fatal("string should not be a string map")
	}
	if hasStringMap(42) {
		t.Fatal("int should not be a string map")
	}
}

func TestHasStringMapWithMapStringAny(t *testing.T) {
	if !hasStringMap(map[string]any{"key": "value"}) {
		t.Fatal("map[string]any should be a string map")
	}
}

func TestHasStringMapWithMapStringString(t *testing.T) {
	if !hasStringMap(map[string]string{"key": "value"}) {
		t.Fatal("map[string]string should be a string map")
	}
}
