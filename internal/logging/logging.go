// Package logging initializes symbrowse's process-wide structured logger.
package logging

import (
	"log/slog"

	"github.com/danieljustus/symaira-corekit/logkit"
)

// Init configures slog from SYMBROWSE_LOG_LEVEL and SYMBROWSE_LOG_FORMAT.
// logkit's default handler writes to stderr, keeping stdout available for CLI
// output and future JSON-RPC frames.
func Init() {
	logkit.InitDefault("symbrowse")
	slog.SetDefault(logkit.Default())
}
