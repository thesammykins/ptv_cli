// Package config loads PTV credentials and CLI configuration from the
// environment and a local .env file.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thesammykins/ptv_cli/internal/credstore"
)

// CredentialSource identifies where credentials were resolved from.
type CredentialSource string

const (
	SourceEnv     CredentialSource = "environment"
	SourceKeyring CredentialSource = "OS keyring"
	SourceDotEnv  CredentialSource = ".env file"
	SourceNone    CredentialSource = "none"
)

// Config holds resolved runtime configuration for the CLI.
type Config struct {
	// APIKey is the PTV Timetable API key used as the HMAC-SHA1 secret.
	APIKey string
	// DevID is the PTV developer / user id (sent as the devid query param).
	DevID string
	// BaseURL is the PTV Timetable API base URL.
	BaseURL string
	// GTFSURL is the location of the PTV GTFS static feed.
	GTFSURL string
	// DataDir is where the local GTFS SQLite database and caches live.
	DataDir string
	// CredentialSource records where the credentials were loaded from.
	CredentialSource CredentialSource
}

const (
	defaultBaseURL = "https://timetableapi.ptv.vic.gov.au"
	defaultGTFSURL = "https://data.ptv.vic.gov.au/downloads/gtfs.zip"
)

// Load resolves configuration. Credentials are resolved in priority order:
// process environment variables, then the OS keyring, then a .env file in the
// working directory.
func Load() (*Config, error) {
	env := loadDotEnv(".env")

	cfg := &Config{
		BaseURL: firstNonEmpty(os.Getenv("PTV_BASE_URL"), env["PTV_BASE_URL"], defaultBaseURL),
		GTFSURL: firstNonEmpty(os.Getenv("PTV_GTFS_URL"), env["PTV_GTFS_URL"], defaultGTFSURL),
		DataDir: firstNonEmpty(os.Getenv("PTV_DATA_DIR"), env["PTV_DATA_DIR"], defaultDataDir()),
	}

	cfg.resolveCredentials(env)

	if cfg.APIKey == "" || cfg.DevID == "" {
		return nil, fmt.Errorf("missing credentials: run 'ptv auth login' to store them securely, or set PTV_API_KEY and PTV_API_USERID")
	}
	return cfg, nil
}

// resolveCredentials fills in APIKey/DevID using the priority order
// environment > OS keyring > .env, recording the source used.
func (c *Config) resolveCredentials(env map[string]string) {
	if k, d := os.Getenv("PTV_API_KEY"), os.Getenv("PTV_API_USERID"); k != "" && d != "" {
		c.APIKey, c.DevID, c.CredentialSource = k, d, SourceEnv
		return
	}
	if creds, err := credstore.Load(); err == nil {
		c.APIKey, c.DevID, c.CredentialSource = creds.APIKey, creds.DevID, SourceKeyring
		return
	}
	if k, d := env["PTV_API_KEY"], env["PTV_API_USERID"]; k != "" && d != "" {
		c.APIKey, c.DevID, c.CredentialSource = k, d, SourceDotEnv
		return
	}
	c.CredentialSource = SourceNone
}

// DefaultBaseURL resolves the API base URL without requiring credentials,
// honouring PTV_BASE_URL from the environment or a .env file.
func DefaultBaseURL() string {
	env := loadDotEnv(".env")
	return firstNonEmpty(os.Getenv("PTV_BASE_URL"), env["PTV_BASE_URL"], defaultBaseURL)
}

// DBPath returns the path to the local GTFS SQLite database.
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "gtfs.sqlite")
}

// defaultDataDir returns the per-user data directory for cached GTFS data.
func defaultDataDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "ptv-cli")
	}
	return filepath.Join(".", ".ptv-cli")
}

// loadDotEnv parses a simple KEY=VALUE .env file. Missing files yield an
// empty map; malformed lines are skipped.
func loadDotEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		out[key] = val
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
