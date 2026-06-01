package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJSON(&buf, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if got["a"] != 1 {
		t.Errorf("round-trip = %v", got)
	}
	if !strings.Contains(buf.String(), "  ") {
		t.Error("expected indented JSON")
	}
}

func TestRenderCostBreakdownEmpty(t *testing.T) {
	var buf bytes.Buffer
	out := newWriterOutput(&buf, &buf, nil)
	renderCostBreakdown(out, "By Cost Center", map[string]float64{})
	if !strings.Contains(buf.String(), "no data") {
		t.Errorf("expected 'no data', got %q", buf.String())
	}
}

func TestRenderCostBreakdownSortedAndCapped(t *testing.T) {
	var buf bytes.Buffer
	out := newWriterOutput(&buf, &buf, nil)
	data := map[string]float64{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"} {
		data[n] = 1.0
	}
	data["top"] = 99.0
	renderCostBreakdown(out, "By Project", data)
	lines := buf.String()
	// Highest spend must appear before the cap notice.
	topIdx := strings.Index(lines, "top")
	moreIdx := strings.Index(lines, "more")
	if topIdx < 0 || moreIdx < 0 || topIdx > moreIdx {
		t.Errorf("expected 'top' before the cap notice; got:\n%s", lines)
	}
}

func TestJoinSorted(t *testing.T) {
	if got := joinSorted([]string{"Project", "CostCenter", "Layer"}); got != "CostCenter, Layer, Project" {
		t.Errorf("joinSorted = %q", got)
	}
	if got := joinSorted(nil); got != "" {
		t.Errorf("empty joinSorted = %q", got)
	}
}

func TestOrEmpty(t *testing.T) {
	if orEmpty("") != "(unknown)" {
		t.Error("empty should map to (unknown)")
	}
	if orEmpty("o-abc") != "o-abc" {
		t.Error("non-empty should pass through")
	}
}

func TestInventoryCommandsRegistered(t *testing.T) {
	want := map[string]bool{"cost": false, "accounts": false, "resources": false}
	for _, c := range rootCmd.Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("command %q not registered on rootCmd", name)
		}
	}
}

func TestResourcesAndAccountsHaveJSONFlag(t *testing.T) {
	for _, c := range []string{"cost", "accounts", "resources"} {
		cmd, _, err := rootCmd.Find([]string{c})
		if err != nil {
			t.Fatalf("find %s: %v", c, err)
		}
		if cmd.Flags().Lookup("json") == nil {
			t.Errorf("command %q missing --json flag", c)
		}
	}
}
