// Command stratum-worker is the Stratum worker agent binary. It is a stub in
// Phase 0: the worker runtime (registration, job loop, Docker executor) is
// implemented in Phase 3.
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
	slog.Error("stratum-worker is not implemented in Phase 0",
		"version", Version, "commit", Commit)
	os.Exit(1)
}
