package cmd

import (
	"strings"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print build information",
	Run: func(cmd *cobra.Command, _ []string) {
		out := newCommandOutput(cmd, d.ui)

		v := strings.TrimSpace(version)
		if v == "" {
			v = "dev"
		}

		c := strings.TrimSpace(commit)
		if c == "" {
			c = "unknown"
		}

		t := strings.TrimSpace(buildTime)
		if t == "" {
			t = "unknown"
		}

		out.Line(v + " (commit=" + c + " built=" + t + ")")
	},
}
