// Package version provides symbrowse's versionkit handshake metadata.
package version

import "github.com/danieljustus/symaira-corekit/versionkit"

const SchemaVersion = 6

// Info returns the stable version payload consumed by GUI clients.
func Info(version string) versionkit.Info {
	return versionkit.New("symbrowse", version, SchemaVersion)
}
