// Command stratum-ctl is the Stratum operator CLI. It is a stub in Phase 0;
// operator commands are implemented in later phases.
package main

import (
	"log/slog"
	"os"
)

// Version and Commit are injected at build time via -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
)

func main() {
	slog.Error("stratum-ctl is not implemented in Phase 0",
		"version", Version, "commit", Commit)
	os.Exit(1)
}
