// Package config loads PTV credentials and CLI configuration from trusted
// process configuration and the OS keyring.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
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
	SourceDotEnv  CredentialSource = "explicit env file"
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

// ErrMissingCredentials indicates no credentials were found in any configured
// credential source.
var ErrMissingCredentials = errors.New("missing credentials")

var loadKeyring = credstore.Load

// LoadOptions controls optional configuration sources.
type LoadOptions struct {
	EnvFile string
}

// Load resolves configuration. Credentials are resolved in priority order:
// process environment variables, then the OS keyring. A dotenv file is read
// only when explicitly supplied through LoadWithOptions.
func Load() (*Config, error) {
	return LoadWithOptions(LoadOptions{})
}

// LoadWithOptions resolves configuration with optional explicit dotenv support.
func LoadWithOptions(opts LoadOptions) (*Config, error) {
	env, err := loadEnvFile(opts.EnvFile)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		BaseURL: firstNonEmpty(os.Getenv("PTV_BASE_URL"), env["PTV_BASE_URL"], defaultBaseURL),
		GTFSURL: firstNonEmpty(os.Getenv("PTV_GTFS_URL"), env["PTV_GTFS_URL"], defaultGTFSURL),
		DataDir: firstNonEmpty(os.Getenv("PTV_DATA_DIR"), env["PTV_DATA_DIR"], defaultDataDir()),
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	cfg.resolveCredentials(env)

	if cfg.APIKey == "" || cfg.DevID == "" {
		return nil, fmt.Errorf("%w: run 'ptv auth login' to store them securely, or set PTV_API_KEY and PTV_API_USERID", ErrMissingCredentials)
	}
	return cfg, nil
}

// resolveCredentials fills in APIKey/DevID using the priority order
// environment > OS keyring > explicit dotenv, recording the source used.
func (c *Config) resolveCredentials(env map[string]string) {
	if k, d := os.Getenv("PTV_API_KEY"), os.Getenv("PTV_API_USERID"); k != "" && d != "" {
		c.APIKey, c.DevID, c.CredentialSource = k, d, SourceEnv
		return
	}
	if creds, err := loadKeyring(); err == nil {
		c.APIKey, c.DevID, c.CredentialSource = creds.APIKey, creds.DevID, SourceKeyring
		return
	}
	if k, d := env["PTV_API_KEY"], env["PTV_API_USERID"]; k != "" && d != "" {
		c.APIKey, c.DevID, c.CredentialSource = k, d, SourceDotEnv
		return
	}
	c.CredentialSource = SourceNone
}

// DefaultBaseURL resolves the API base URL without requiring credentials.
func DefaultBaseURL() string {
	return DefaultBaseURLWithOptions(LoadOptions{})
}

// DefaultBaseURLWithOptions resolves the API base URL with optional explicit
// dotenv support.
func DefaultBaseURLWithOptions(opts LoadOptions) string {
	env, _ := loadEnvFile(opts.EnvFile)
	base := firstNonEmpty(os.Getenv("PTV_BASE_URL"), env["PTV_BASE_URL"], defaultBaseURL)
	if err := validateHTTPSURL("PTV_BASE_URL", base); err != nil {
		return defaultBaseURL
	}
	return base
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

func loadEnvFile(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]string{}, nil
	}
	return loadDotEnv(path)
}

// loadDotEnv parses a simple KEY=VALUE dotenv file. Missing files yield an
// empty map.
func loadDotEnv(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, val, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("%s: malformed dotenv line %d", path, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Config) validate() error {
	if err := validateHTTPSURL("PTV_BASE_URL", c.BaseURL); err != nil {
		return err
	}
	if err := validateHTTPSURL("PTV_GTFS_URL", c.GTFSURL); err != nil {
		return err
	}
	dataDir, err := validateDataDir(c.DataDir)
	if err != nil {
		return err
	}
	c.DataDir = dataDir
	return nil
}

func validateHTTPSURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid %s %q", name, raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLocalhost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%s must use https", name)
}

func isLocalhost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateDataDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("PTV_DATA_DIR is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if clean == string(filepath.Separator) {
		return "", fmt.Errorf("PTV_DATA_DIR must not be filesystem root")
	}
	return clean, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
