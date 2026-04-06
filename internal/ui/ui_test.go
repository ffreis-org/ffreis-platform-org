package ui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func testPlainPresenter() *Presenter {
	return &Presenter{
		mode:        ModePlain,
		interactive: true,
		header:      lipgloss.NewStyle(),
		subtle:      lipgloss.NewStyle(),
		key:         lipgloss.NewStyle(),
		badges: map[string]lipgloss.Style{
			"ok":      lipgloss.NewStyle(),
			"running": lipgloss.NewStyle(),
			"warn":    lipgloss.NewStyle(),
			"error":   lipgloss.NewStyle(),
			"muted":   lipgloss.NewStyle(),
			"info":    lipgloss.NewStyle(),
		},
	}
}

func testRichPresenter() *Presenter {
	return &Presenter{
		mode:        ModeRich,
		interactive: true,
		header:      lipgloss.NewStyle().Bold(true),
		subtle:      lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		key:         lipgloss.NewStyle().Bold(true),
		badges: map[string]lipgloss.Style{
			"ok":      lipgloss.NewStyle().Bold(true),
			"running": lipgloss.NewStyle().Bold(true),
			"warn":    lipgloss.NewStyle().Bold(true),
			"error":   lipgloss.NewStyle().Bold(true),
			"muted":   lipgloss.NewStyle(),
			"info":    lipgloss.NewStyle().Underline(true),
		},
	}
}

func TestNewPlainAndInvalid(t *testing.T) {
	presenter, err := New(ModePlain)
	if err != nil {
		t.Fatalf("New plain: %v", err)
	}
	if presenter == nil || presenter.mode != ModePlain {
		t.Fatalf("unexpected presenter: %#v", presenter)
	}

	_, err = New("invalid")
	if err == nil || !strings.Contains(err.Error(), "invalid ui mode") {
		t.Fatalf("expected invalid mode error, got %v", err)
	}
}

func TestResolveMode(t *testing.T) {
	tests := []struct {
		name         string
		requested    string
		stdoutTTY    bool
		stderrTTY    bool
		disableColor bool
		wantMode     string
		wantInteract bool
		wantErr      string
	}{
		{name: "auto rich", requested: ModeAuto, stdoutTTY: true, wantMode: ModeRich, wantInteract: true},
		{name: "auto plain no tty", requested: ModeAuto, wantMode: ModePlain, wantInteract: false},
		{name: "auto plain no color", requested: ModeAuto, stdoutTTY: true, disableColor: true, wantMode: ModePlain, wantInteract: true},
		{name: "plain forced", requested: ModePlain, wantMode: ModePlain, wantInteract: true},
		{name: "rich downgraded", requested: ModeRich, disableColor: true, wantMode: ModePlain, wantInteract: true},
		{name: "rich allowed", requested: ModeRich, wantMode: ModeRich, wantInteract: true},
		{name: "trim and lowercase", requested: "  RICH  ", wantMode: ModeRich, wantInteract: true},
		{name: "invalid", requested: "bad", wantErr: "invalid ui mode"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotMode, gotInteractive, err := ResolveMode(tc.requested, tc.stdoutTTY, tc.stderrTTY, tc.disableColor)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveMode: %v", err)
			}
			if gotMode != tc.wantMode || gotInteractive != tc.wantInteract {
				t.Fatalf("ResolveMode() = (%q, %v), want (%q, %v)", gotMode, gotInteractive, tc.wantMode, tc.wantInteract)
			}
		})
	}
}

func TestIsTTY(t *testing.T) {
	if IsTTY(nil) {
		t.Fatal("nil file must not be treated as tty")
	}

	file, err := os.CreateTemp(t.TempDir(), "ui-not-tty")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer file.Close()
	if IsTTY(file) {
		t.Fatal("regular file must not be treated as tty")
	}

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("Open(%q): %v", os.DevNull, err)
	}
	defer devNull.Close()
	if !IsTTY(devNull) {
		t.Fatal("expected os.DevNull to be a character device on this platform")
	}
}

func TestWithPresenterAndFromContext(t *testing.T) {
	presenter := testPlainPresenter()
	ctx := WithPresenter(context.Background(), presenter)
	if got := FromContext(ctx); got != presenter {
		t.Fatalf("FromContext returned %#v, want original presenter", got)
	}

	fallback := FromContext(context.Background())
	if fallback == nil || fallback.mode != ModePlain {
		t.Fatalf("unexpected fallback presenter: %#v", fallback)
	}
}

func TestInteractiveAndRich(t *testing.T) {
	if (&Presenter{interactive: true}).Interactive() != true {
		t.Fatal("Interactive should reflect presenter state")
	}
	if (*Presenter)(nil).Interactive() {
		t.Fatal("nil presenter must not be interactive")
	}
	if (&Presenter{mode: ModeRich}).Rich() != true {
		t.Fatal("Rich should be true in rich mode")
	}
	if (*Presenter)(nil).Rich() {
		t.Fatal("nil presenter must not be rich")
	}
}

func assertKeyHeaderSummaryBadgeStatus(t *testing.T, plain, rich *Presenter) {
	t.Helper()

	if got := plain.Key("name"); got != "name" {
		t.Fatalf("Key plain = %q", got)
	}
	if got := rich.Key("name"); got != rich.render("name", rich.key) || !strings.Contains(got, "name") {
		t.Fatalf("Key rich = %q", got)
	}

	if got := plain.Header("Title", "Subtitle"); got != "Title\nSubtitle" {
		t.Fatalf("Header plain = %q", got)
	}
	if got := plain.Header("Title", ""); got != "Title" {
		t.Fatalf("Header without subtitle = %q", got)
	}
	if got := rich.Header("Title", "Subtitle"); !strings.Contains(got, "Title") || !strings.Contains(got, "Subtitle") || !strings.Contains(got, "  ") {
		t.Fatalf("Header rich = %q", got)
	}

	if got := plain.Summary("Result", "", "a", "  ", "b"); got != "Result: a  b" {
		t.Fatalf("Summary = %q", got)
	}
	if got := plain.Summary("Result"); got != "Result" {
		t.Fatalf("Summary without parts = %q", got)
	}

	if got := plain.Badge("warn", " WARN "); got != "[warn]" {
		t.Fatalf("Badge plain = %q", got)
	}
	if got := plain.Badge("warn", " "); got != "" {
		t.Fatalf("Badge empty = %q", got)
	}
	if got := rich.Badge("unknown", "hello"); !strings.Contains(got, "hello") {
		t.Fatalf("Badge fallback = %q", got)
	}

	if got := plain.Status("ok", "done", " "); got != "[done]" {
		t.Fatalf("Status without detail = %q", got)
	}
	if got := plain.Status("ok", "done", "finished"); got != "[done] finished" {
		t.Fatalf("Status with detail = %q", got)
	}
	if got := rich.Status("ok", "done", "finished"); !strings.Contains(got, "finished") || !strings.Contains(got, "done") {
		t.Fatalf("Status rich = %q", got)
	}
}

func assertDurationAndRender(t *testing.T, plain, rich *Presenter) {
	t.Helper()

	if got := plain.Duration(0); got != "0s" {
		t.Fatalf("Duration zero = %q", got)
	}
	if got := plain.Duration(155 * time.Millisecond); got != "160ms" {
		t.Fatalf("Duration sub-second = %q", got)
	}
	if got := plain.Duration(1549 * time.Millisecond); got != "1.5s" {
		t.Fatalf("Duration rounded seconds = %q", got)
	}

	if got := (*Presenter)(nil).render("value", lipgloss.NewStyle().Bold(true)); got != "value" {
		t.Fatalf("nil render = %q", got)
	}
	if got := plain.render("value", lipgloss.NewStyle().Bold(true)); got != "value" {
		t.Fatalf("plain render = %q", got)
	}
	if got := rich.render("value", lipgloss.NewStyle().Bold(true)); !strings.Contains(got, "value") {
		t.Fatalf("rich render = %q", got)
	}
}

func TestKeyHeaderSummaryBadgeStatus(t *testing.T) {
	plain := testPlainPresenter()
	rich := testRichPresenter()
	assertKeyHeaderSummaryBadgeStatus(t, plain, rich)
}

func TestDurationAndRender(t *testing.T) {
	plain := testPlainPresenter()
	rich := testRichPresenter()
	assertDurationAndRender(t, plain, rich)
}

func TestNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if noColor() {
		t.Fatal("empty NO_COLOR should be false")
	}

	t.Setenv("NO_COLOR", "0")
	if noColor() {
		t.Fatal("NO_COLOR=0 should be false")
	}

	t.Setenv("NO_COLOR", "false")
	if noColor() {
		t.Fatal("NO_COLOR=false should be false")
	}

	t.Setenv("NO_COLOR", "1")
	if !noColor() {
		t.Fatal("NO_COLOR=1 should be true")
	}
}
