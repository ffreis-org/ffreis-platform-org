package cmd

import (
	"bytes"
	"strings"
	"testing"

	platformui "github.com/ffreis/platform-org/internal/ui"
)

const testOutputFineMessage = "everything is fine"

// newPlainOutput returns a commandOutput with a nil UI (plain / NO_COLOR mode).
func newPlainOutput(t *testing.T) (*commandOutput, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errBuf bytes.Buffer
	return newWriterOutput(&out, &errBuf, nil), &out, &errBuf
}

func TestHeaderPlainWithSubtitle(t *testing.T) {
	o, out, _ := newPlainOutput(t)
	o.Header("TITLE", "sub")
	got := out.String()
	if !strings.Contains(got, "TITLE") || !strings.Contains(got, "sub") {
		t.Fatalf("Header output: %q", got)
	}
}

func TestHeaderPlainNoSubtitle(t *testing.T) {
	o, out, _ := newPlainOutput(t)
	o.Header("ONLY", "")
	got := out.String()
	if !strings.Contains(got, "ONLY") {
		t.Fatalf("Header output: %q", got)
	}
	// subtitle must not appear as a blank line beyond the title line
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one line, got %d: %q", len(lines), got)
	}
}

func TestHeaderRichDelegatesToUI(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	ui, err := platformui.New(platformui.ModeRich)
	if err != nil {
		t.Fatalf(testPlatformUINewErrorf, err)
	}
	var out, errBuf bytes.Buffer
	o := newWriterOutput(&out, &errBuf, ui)
	o.Header("TITLE", "sub")
	if out.String() == "" {
		t.Fatal("expected non-empty header output in rich mode")
	}
}

func TestSummaryPlainWithParts(t *testing.T) {
	o, out, _ := newPlainOutput(t)
	o.Summary("Summary", "ok=1", "warn=2")
	got := out.String()
	if !strings.Contains(got, "Summary") || !strings.Contains(got, "ok=1") {
		t.Fatalf("Summary output: %q", got)
	}
}

func TestSummaryPlainEmptyParts(t *testing.T) {
	o, out, _ := newPlainOutput(t)
	o.Summary("OnlyTitle", "  ", "")
	got := out.String()
	if !strings.Contains(got, "OnlyTitle") {
		t.Fatalf("Summary output: %q", got)
	}
	if strings.Contains(got, ":") {
		t.Fatalf("Summary with empty parts should not include colon: %q", got)
	}
}

func TestSummaryRichDelegatesToUI(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	ui, err := platformui.New(platformui.ModeRich)
	if err != nil {
		t.Fatalf(testPlatformUINewErrorf, err)
	}
	var out, errBuf bytes.Buffer
	o := newWriterOutput(&out, &errBuf, ui)
	o.Summary("Summary", "ok=1")
	if out.String() == "" {
		t.Fatal("expected non-empty summary output in rich mode")
	}
}

func TestStatusPlain(t *testing.T) {
	o, out, _ := newPlainOutput(t)
	o.Status("ok", "ok", testOutputFineMessage)
	got := out.String()
	if !strings.Contains(got, "[ok]") || !strings.Contains(got, testOutputFineMessage) {
		t.Fatalf("Status output: %q", got)
	}
}

func TestStatusRichDelegatesToUI(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	ui, err := platformui.New(platformui.ModeRich)
	if err != nil {
		t.Fatalf(testPlatformUINewErrorf, err)
	}
	var out, errBuf bytes.Buffer
	o := newWriterOutput(&out, &errBuf, ui)
	o.Status("ok", "ok", testOutputFineMessage)
	if out.String() == "" {
		t.Fatal("expected non-empty status output in rich mode")
	}
}

func TestErrStatusPlain(t *testing.T) {
	o, _, errBuf := newPlainOutput(t)
	o.ErrStatus("error", "fail", "something broke")
	got := errBuf.String()
	if !strings.Contains(got, "[fail]") || !strings.Contains(got, "something broke") {
		t.Fatalf("ErrStatus output: %q", got)
	}
}

func TestErrStatusRichWritesToStderr(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	ui, err := platformui.New(platformui.ModeRich)
	if err != nil {
		t.Fatalf(testPlatformUINewErrorf, err)
	}
	var out, errBuf bytes.Buffer
	o := newWriterOutput(&out, &errBuf, ui)
	o.ErrStatus("error", "fail", "broke")
	if errBuf.String() == "" {
		t.Fatal("expected non-empty ErrStatus written to stderr")
	}
	if out.String() != "" {
		t.Fatalf("ErrStatus should not write to stdout, got: %q", out.String())
	}
}

func TestTablePlainStripANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui, err := platformui.New(platformui.ModePlain)
	if err != nil {
		t.Fatalf(testPlatformUINewErrorf, err)
	}
	var buf bytes.Buffer
	out := newWriterOutput(&buf, &buf, ui)
	rows := [][]string{{"\x1b[32mok\x1b[0m", "s3/bucket", "my-bucket"}}
	if err := out.Table([]string{"STATUS", "TYPE", "NAME"}, rows); err != nil {
		t.Fatalf("Table: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("plain table should strip ANSI sequences: %q", got)
	}
	if !strings.Contains(got, "ok") || !strings.Contains(got, "my-bucket") {
		t.Fatalf("plain table missing expected content: %q", got)
	}
}
