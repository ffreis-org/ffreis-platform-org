package cmd

import (
	"bytes"
	"strings"
	"testing"

	platformui "github.com/ffreis/platform-org/internal/ui"
)

func TestTablePreservesANSIInRichMode(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	ui, err := platformui.New(platformui.ModeRich)
	if err != nil {
		t.Fatalf("ui.New: %v", err)
	}

	var buf bytes.Buffer
	out := newWriterOutput(&buf, &buf, ui)
	rows := [][]string{{"\x1b[32mok\x1b[0m", "dynamodb/table", "registry"}}

	if err := out.Table([]string{"STATUS", "TYPE", "NAME"}, rows); err != nil {
		t.Fatalf("Table: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("rich table output did not preserve ANSI sequences: %q", got)
	}
	if strings.Contains(got, "[ok]") {
		t.Fatalf("rich table output fell back to plain rendering: %q", got)
	}
}
