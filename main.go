// Command ptv is a terminal companion for Victorian public transport.
package main

import (
	_ "embed"
	"strings"

	"github.com/thesammykins/ptv_cli/cmd"
)

// Build metadata, set via -ldflags at release time (GoReleaser).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// sourceVersion is the version reviewed in the pull request. GoReleaser still
// replaces version through ldflags for a tagged release.
//
//go:embed VERSION
var sourceVersion string

func main() {
	cmd.SetBuildInfo(effectiveVersion(version, sourceVersion), commit, date)
	cmd.Execute()
}

func effectiveVersion(buildVersion, plannedVersion string) string {
	if buildVersion != "dev" {
		return buildVersion
	}
	plannedVersion = strings.TrimSpace(plannedVersion)
	if plannedVersion == "" {
		return "dev"
	}
	return plannedVersion + "-dev"
}
