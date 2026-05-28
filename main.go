// Command ptv is a terminal companion for Victorian public transport.
package main

import "github.com/elsammykins/ptv_cli/cmd"

// Build metadata, set via -ldflags at release time (GoReleaser).
var (
version = "dev"
commit  = "none"
date    = "unknown"
)

func main() {
cmd.SetBuildInfo(version, commit, date)
cmd.Execute()
}
