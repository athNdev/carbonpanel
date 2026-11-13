// SPDX-License-Identifier: MPL-2.0
// Package buildinfo exposes release metadata set by the build pipeline.
package buildinfo

import (
	"fmt"
	"runtime"
)

// These defaults describe an unversioned development build. Release builds set
// all three with go build -ldflags; they never read workstation configuration.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func String(program string) string {
	return fmt.Sprintf("%s %s (commit %s; built %s; %s/%s)",
		program, Version, Commit, Date, runtime.GOOS, runtime.GOARCH)
}
