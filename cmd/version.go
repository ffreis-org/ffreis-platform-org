package cmd

// Build-time variables injected by the linker via -X flags in the Makefile.
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)
