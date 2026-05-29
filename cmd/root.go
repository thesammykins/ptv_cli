// Package cmd defines the ptv CLI commands.
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/config"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
)

var (
	flagJSON  bool
	flagLimit int
	flagEnv   string
)

// Build metadata, overridable via -ldflags at release time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// SetBuildInfo lets the main package inject build metadata from ldflags.
func SetBuildInfo(v, c, d string) {
	if v != "" {
		version = v
	}
	if c != "" {
		commit = c
	}
	if d != "" {
		date = d
	}
	rootCmd.Version = version
}

// rootCmd is the base command.
var rootCmd = &cobra.Command{
	Use:   "ptv",
	Short: "Victorian public transport from your terminal",
	Long: `ptv is a command-line companion for Victorian public transport.

It uses the PTV Timetable API (real-time departures, disruptions, line and
station information) together with the PTV GTFS feed (multi-modal journey
planning) to bring Transit-app style functionality to the terminal.

Credentials are read from PTV_API_KEY and PTV_API_USERID (environment) or the
OS keyring populated by 'ptv auth login'.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       version,
}

// versionCmd prints detailed build information.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version and build information",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagJSON {
			return printJSON(map[string]string{
				"version": version,
				"commit":  commit,
				"date":    date,
			})
		}
		fmt.Printf("ptv %s\n  commit: %s\n  built:  %s\n", render.CleanText(version), render.CleanText(commit), render.CleanText(date))
		return nil
	},
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "output raw JSON")
	rootCmd.PersistentFlags().IntVar(&flagLimit, "limit", 0, "limit number of results (0 = API default)")
	rootCmd.PersistentFlags().StringVar(&flagEnv, "env-file", "", "explicit dotenv file to load (not read by default)")
	rootCmd.SetVersionTemplate("ptv {{.Version}}\n")
	rootCmd.AddCommand(versionCmd)
}

// loadClient resolves config and constructs an API client.
func loadClient() (*ptvapi.Client, *config.Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, nil, err
	}
	return ptvapi.New(cfg.BaseURL, cfg.APIKey, cfg.DevID), cfg, nil
}

func loadConfig() (*config.Config, error) {
	return config.LoadWithOptions(config.LoadOptions{EnvFile: flagEnv})
}

// ctx returns a background context for command execution.
func ctx() context.Context { return context.Background() }

// defaultBaseURL resolves the API base URL without requiring credentials.
func defaultBaseURL() string {
	return config.DefaultBaseURLWithOptions(config.LoadOptions{EnvFile: flagEnv})
}
