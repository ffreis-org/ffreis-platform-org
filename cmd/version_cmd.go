package cmd

import (
	"strings"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print build information",
	// version is a local command — it does not need AWS credentials.
	Annotations: map[string]string{localCommandAnnotation: "true"},
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
