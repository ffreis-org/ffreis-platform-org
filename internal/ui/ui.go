package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

const (
	ModeAuto  = "auto"
	ModePlain = "plain"
	ModeRich  = "rich"
)

type contextKey struct{}

type Presenter struct {
	mode        string
	interactive bool
	header      lipgloss.Style
	subtle      lipgloss.Style
	key         lipgloss.Style
	badges      map[string]lipgloss.Style
}

func New(requested string) (*Presenter, error) {
	mode, interactive, err := ResolveMode(requested, IsTTY(os.Stdout), IsTTY(os.Stderr), noColor())
	if err != nil {
		return nil, err
	}

	p := &Presenter{
		mode:        mode,
		interactive: interactive,
		header:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75")),
		subtle:      lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		key:         lipgloss.NewStyle().Foreground(lipgloss.Color("110")),
		badges: map[string]lipgloss.Style{
			"ok":      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")),
			"running": lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75")),
			"warn":    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")),
			"error":   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203")),
			"muted":   lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
			"info":    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75")),
		},
	}
	if mode == ModePlain {
		p.header = lipgloss.NewStyle()
		p.subtle = lipgloss.NewStyle()
		p.key = lipgloss.NewStyle()
		for k := range p.badges {
			p.badges[k] = lipgloss.NewStyle()
		}
	}

	return p, nil
}

func ResolveMode(requested string, stdoutTTY, stderrTTY, disableColor bool) (string, bool, error) {
	mode := strings.ToLower(strings.TrimSpace(requested))
	if mode == "" {
		mode = ModeAuto
	}

	switch mode {
	case ModeAuto:
		interactive := stdoutTTY || stderrTTY
		if disableColor {
			return ModePlain, interactive, nil
		}
		if interactive {
			return ModeRich, true, nil
		}
		return ModePlain, false, nil
	case ModePlain:
		return ModePlain, true, nil
	case ModeRich:
		if disableColor {
			return ModePlain, true, nil
		}
		return ModeRich, true, nil
	default:
		return "", false, fmt.Errorf("invalid ui mode %q: must be auto, plain, or rich", requested)
	}
}

func IsTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

func WithPresenter(ctx context.Context, presenter *Presenter) context.Context {
	return context.WithValue(ctx, contextKey{}, presenter)
}

func FromContext(ctx context.Context) *Presenter {
	if presenter, ok := ctx.Value(contextKey{}).(*Presenter); ok && presenter != nil {
		return presenter
	}
	presenter, _ := New(ModePlain)
	return presenter
}

func (p *Presenter) Interactive() bool {
	return p != nil && p.interactive
}

func (p *Presenter) Rich() bool {
	return p != nil && p.mode == ModeRich
}

func (p *Presenter) Key(value string) string {
	return p.render(value, p.key)
}

func (p *Presenter) Header(title, subtitle string) string {
	if subtitle == "" {
		return p.render(title, p.header)
	}
	if p.mode == ModeRich {
		return fmt.Sprintf("%s  %s", p.render(title, p.header), p.render(subtitle, p.subtle))
	}
	return fmt.Sprintf("%s\n%s", title, subtitle)
}

func (p *Presenter) Summary(title string, parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 {
		return title
	}
	return fmt.Sprintf("%s: %s", p.render(title, p.key), strings.Join(filtered, "  "))
}

func (p *Presenter) Badge(kind, label string) string {
	label = strings.ToLower(strings.TrimSpace(label))
	if label == "" {
		return ""
	}
	style, ok := p.badges[kind]
	if !ok {
		style = p.badges["info"]
	}
	if p.mode == ModeRich {
		return style.Render(label)
	}
	return "[" + label + "]"
}

func (p *Presenter) Status(kind, label, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return p.Badge(kind, label)
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s", p.Badge(kind, label), p.render(detail, p.subtle)))
}

func (p *Presenter) Duration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Second {
		return d.Round(10 * time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}

func (p *Presenter) render(value string, style lipgloss.Style) string {
	if p == nil || p.mode != ModeRich {
		return value
	}
	return style.Render(value)
}

func noColor() bool {
	value := strings.TrimSpace(os.Getenv("NO_COLOR"))
	return value != "" && value != "0" && strings.ToLower(value) != "false"
}
