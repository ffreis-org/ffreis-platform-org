package cmd

import (
	"encoding/json"
	"io"
)

// writeJSON renders v as indented JSON to w. It is the machine-readable output
// path shared by the read commands (cost, accounts, resources) so scripts and
// dashboard Lambdas consume a stable contract instead of parsing the human
// presenter output.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
