package cmd

import (
	"io"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	platformui "github.com/ffreis/platform-org/internal/ui"
)

type commandOutput struct {
	out io.Writer
	err io.Writer
	ui  *platformui.Presenter
}

func newCommandOutput(cmd *cobra.Command, presenter *platformui.Presenter) *commandOutput {
	return newWriterOutput(cmd.OutOrStdout(), cmd.ErrOrStderr(), presenter)
}

func newWriterOutput(out, err io.Writer, presenter *platformui.Presenter) *commandOutput {
	return &commandOutput{out: out, err: err, ui: presenter}
}

func (o *commandOutput) Line(text string) {
	writeLine(o.out, text)
}

func (o *commandOutput) ErrLine(text string) {
	writeLine(o.err, text)
}

func (o *commandOutput) Blank() {
	writeLine(o.out, "")
}

func (o *commandOutput) Header(title, subtitle string) {
	if o.ui != nil {
		o.Line(o.ui.Header(title, subtitle))
		return
	}
	o.Line(title)
	if subtitle != "" {
		o.Line(subtitle)
	}
}

func (o *commandOutput) Summary(title string, parts ...string) {
	if o.ui != nil {
		o.Line(o.ui.Summary(title, parts...))
		return
	}
	filtered := filterParts(parts)
	if len(filtered) == 0 {
		o.Line(title)
		return
	}
	o.Line(title + ": " + strings.Join(filtered, "  "))
}

func (o *commandOutput) Status(kind, label, detail string) {
	if o.ui != nil {
		o.Line(o.ui.Status(kind, label, detail))
		return
	}
	o.Line("[" + label + "] " + detail)
}

func (o *commandOutput) ErrStatus(kind, label, detail string) {
	if o.ui != nil {
		o.ErrLine(o.ui.Status(kind, label, detail))
		return
	}
	o.ErrLine("[" + label + "] " + detail)
}

func (o *commandOutput) Table(headers []string, rows [][]string) error {
	if o.ui != nil && o.ui.Rich() {
		return o.writeRichTable(headers, rows)
	}

	w := tabwriter.NewWriter(o.out, 0, 0, 2, ' ', 0)
	stripped := make([]string, len(headers))
	for i, h := range headers {
		stripped[i] = stripANSI(h)
	}
	_, _ = io.WriteString(w, strings.Join(stripped, "\t")+"\n")
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = stripANSI(cell)
		}
		_, _ = io.WriteString(w, strings.Join(cells, "\t")+"\n")
	}
	return w.Flush()
}

func (o *commandOutput) writeRichTable(headers []string, rows [][]string) error {
	styledHeaders := make([]string, len(headers))
	copy(styledHeaders, headers)
	for i, h := range styledHeaders {
		styledHeaders[i] = o.ui.Key(h)
	}

	colWidths := make([]int, len(headers))
	for i, h := range styledHeaders {
		colWidths[i] = lipgloss.Width(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(colWidths) {
				break
			}
			if width := lipgloss.Width(cell); width > colWidths[i] {
				colWidths[i] = width
			}
		}
	}

	writeRow := func(row []string) {
		for i, cell := range row {
			if i >= len(colWidths) {
				break
			}
			if i > 0 {
				_, _ = io.WriteString(o.out, "  ")
			}
			_, _ = io.WriteString(o.out, cell)
			padding := colWidths[i] - lipgloss.Width(cell)
			if padding > 0 {
				_, _ = io.WriteString(o.out, strings.Repeat(" ", padding))
			}
		}
		_, _ = io.WriteString(o.out, "\n")
	}

	writeRow(styledHeaders)
	for _, row := range rows {
		writeRow(row)
	}
	return nil
}

func writeLine(w io.Writer, text string) {
	_, _ = io.WriteString(w, text+"\n")
}

func filterParts(parts []string) []string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	return filtered
}

func countPart(label string, value int) string {
	return label + "=" + strconv.Itoa(value)
}

func envAccountRegionSummary(env, accountID, region string) string {
	return "env " + env + "  account " + accountID + "  region " + region
}

var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiEscapeRE.ReplaceAllString(s, "")
}
