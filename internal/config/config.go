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
	// GeocoderURL is the configured geocoding search endpoint.
	GeocoderURL string
	// GeocoderProvider is the user-visible identity of the configured provider.
	GeocoderProvider string
	// GeocoderAttribution is emitted when configured-provider data contributes
	// to command output.
	GeocoderAttribution string
	// OpenDataKeyID is the optional Transport Victoria Open Data subscription key used for GTFS Realtime feeds.
	OpenDataKeyID string
	// OpenDataAPIID is the optional Transport Victoria Open Data platform API token.
	OpenDataAPIID string
	// DataDir is where the local GTFS SQLite database and caches live.
	DataDir string
	// CredentialSource records where the credentials were loaded from.
	CredentialSource CredentialSource
}

// RuntimeConfig contains non-secret settings shared by local and networked
// commands. Loading it never consults credential stores or requires credentials.
type RuntimeConfig struct {
	BaseURL             string
	GTFSURL             string
	GeocoderURL         string
	GeocoderProvider    string
	GeocoderAttribution string
	DataDir             string
}

// PTVCredentials are the credentials required to sign Timetable API requests.
type PTVCredentials struct {
	APIKey string
	DevID  string
	Source CredentialSource
}

const (
	defaultBaseURL             = "https://timetableapi.ptv.vic.gov.au"
	defaultGTFSURL             = "https://data.ptv.vic.gov.au/downloads/gtfs.zip"
	defaultGeocoderURL         = "https://nominatim.openstreetmap.org/search"
	defaultGeocoderProvider    = "OpenStreetMap Nominatim"
	defaultGeocoderAttribution = "© OpenStreetMap contributors"
)

// ErrMissingCredentials indicates no credentials were found in any configured
// credential source.
var ErrMissingCredentials = errors.New("missing credentials")

var loadKeyring = credstore.Load

var loadOpenDataKeyring = credstore.LoadOpenData

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

// LoadRuntime resolves non-secret runtime configuration without requiring or
// consulting credentials.
func LoadRuntime() (*RuntimeConfig, error) {
	return LoadRuntimeWithOptions(LoadOptions{})
}

// LoadPTVCredentials resolves only Timetable API credentials.
func LoadPTVCredentials() (PTVCredentials, error) {
	return LoadPTVCredentialsWithOptions(LoadOptions{})
}

// LoadWithOptions resolves configuration with optional explicit dotenv support.
func LoadWithOptions(opts LoadOptions) (*Config, error) {
	runtimeCfg, err := LoadRuntimeWithOptions(opts)
	if err != nil {
		return nil, err
	}
	ptvCreds, err := LoadPTVCredentialsWithOptions(opts)
	if err != nil {
		return nil, err
	}
	openData, err := OpenDataCredentialsWithOptions(opts)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		APIKey:              ptvCreds.APIKey,
		DevID:               ptvCreds.DevID,
		BaseURL:             runtimeCfg.BaseURL,
		GTFSURL:             runtimeCfg.GTFSURL,
		GeocoderURL:         runtimeCfg.GeocoderURL,
		GeocoderProvider:    runtimeCfg.GeocoderProvider,
		GeocoderAttribution: runtimeCfg.GeocoderAttribution,
		OpenDataKeyID:       openData.KeyID,
		OpenDataAPIID:       openData.APIID,
		DataDir:             runtimeCfg.DataDir,
		CredentialSource:    ptvCreds.Source,
	}
	return cfg, nil
}

// LoadRuntimeWithOptions resolves non-secret runtime configuration. It is the
// correct entry point for local GTFS, status, catalog, and offline plan flows.
func LoadRuntimeWithOptions(opts LoadOptions) (*RuntimeConfig, error) {
	env, err := loadEnvFile(opts.EnvFile)
	if err != nil {
		return nil, err
	}
	geocoderURL := firstNonEmpty(os.Getenv("PTV_GEOCODER_URL"), env["PTV_GEOCODER_URL"], defaultGeocoderURL)
	providerDefault := "Configured geocoder"
	attributionDefault := ""
	if geocoderURL == defaultGeocoderURL {
		providerDefault = defaultGeocoderProvider
		attributionDefault = defaultGeocoderAttribution
	}
	cfg := &RuntimeConfig{
		BaseURL:             firstNonEmpty(os.Getenv("PTV_BASE_URL"), env["PTV_BASE_URL"], defaultBaseURL),
		GTFSURL:             firstNonEmpty(os.Getenv("PTV_GTFS_URL"), env["PTV_GTFS_URL"], defaultGTFSURL),
		GeocoderURL:         geocoderURL,
		GeocoderProvider:    firstNonEmpty(os.Getenv("PTV_GEOCODER_PROVIDER"), env["PTV_GEOCODER_PROVIDER"], providerDefault),
		GeocoderAttribution: firstNonEmpty(os.Getenv("PTV_GEOCODER_ATTRIBUTION"), env["PTV_GEOCODER_ATTRIBUTION"], attributionDefault),
		DataDir:             firstNonEmpty(os.Getenv("PTV_DATA_DIR"), env["PTV_DATA_DIR"], defaultDataDir()),
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadPTVCredentialsWithOptions resolves only Timetable API credentials using
// environment, OS keyring, then an explicitly supplied dotenv file.
func LoadPTVCredentialsWithOptions(opts LoadOptions) (PTVCredentials, error) {
	env, err := loadEnvFile(opts.EnvFile)
	if err != nil {
		return PTVCredentials{}, err
	}
	if key, devID := os.Getenv("PTV_API_KEY"), os.Getenv("PTV_API_USERID"); key != "" && devID != "" {
		return PTVCredentials{APIKey: key, DevID: devID, Source: SourceEnv}, nil
	}
	if creds, err := loadKeyring(); err == nil && creds.APIKey != "" && creds.DevID != "" {
		return PTVCredentials{APIKey: creds.APIKey, DevID: creds.DevID, Source: SourceKeyring}, nil
	}
	if key, devID := env["PTV_API_KEY"], env["PTV_API_USERID"]; key != "" && devID != "" {
		return PTVCredentials{APIKey: key, DevID: devID, Source: SourceDotEnv}, nil
	}
	return PTVCredentials{}, fmt.Errorf("%w: run 'ptv auth login' to store them securely, or set PTV_API_KEY and PTV_API_USERID", ErrMissingCredentials)
}

// OpenDataCredentialsWithOptions resolves optional Transport Victoria Open Data
// credentials without requiring PTV Timetable API credentials.
func OpenDataCredentialsWithOptions(opts LoadOptions) (credstore.OpenDataCredentials, error) {
	env, err := loadEnvFile(opts.EnvFile)
	if err != nil {
		return credstore.OpenDataCredentials{}, err
	}
	if keyID := firstNonEmpty(os.Getenv("PTV_OPENDATA_KEY_ID"), os.Getenv("PTV_OPENDATA_KEYID")); keyID != "" {
		return credstore.OpenDataCredentials{KeyID: keyID, APIID: os.Getenv("PTV_OPENDATA_API_ID")}, nil
	}
	if creds, err := loadOpenDataKeyring(); err == nil {
		return creds, nil
	}
	if keyID := firstNonEmpty(env["PTV_OPENDATA_KEY_ID"], env["PTV_OPENDATA_KEYID"]); keyID != "" {
		return credstore.OpenDataCredentials{KeyID: keyID, APIID: env["PTV_OPENDATA_API_ID"]}, nil
	}
	return credstore.OpenDataCredentials{}, nil
}

// DBPath returns the path to the local GTFS SQLite database.
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "gtfs.sqlite")
}

// DBPath returns the legacy fixed database path. Generation-aware store code
// may resolve a current manifest beneath DataDir instead.
func (c *RuntimeConfig) DBPath() string {
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

func (c *RuntimeConfig) validate() error {
	if err := validateHTTPSURL("PTV_BASE_URL", c.BaseURL); err != nil {
		return err
	}
	if err := validateHTTPSURL("PTV_GTFS_URL", c.GTFSURL); err != nil {
		return err
	}
	if err := validateHTTPSURL("PTV_GEOCODER_URL", c.GeocoderURL); err != nil {
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
		return fmt.Errorf("invalid %s URL", name)
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
	raw := strings.TrimSpace(path)
	if raw == "" {
		return "", fmt.Errorf("PTV_DATA_DIR is empty")
	}
	if isPortableFilesystemRoot(raw) {
		return "", fmt.Errorf("PTV_DATA_DIR must not be filesystem root")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if clean == string(filepath.Separator) || clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return "", fmt.Errorf("PTV_DATA_DIR must not be filesystem root")
	}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		resolved = filepath.Clean(resolved)
		if resolved == string(filepath.Separator) || resolved == filepath.VolumeName(resolved)+string(filepath.Separator) {
			return "", fmt.Errorf("PTV_DATA_DIR must not resolve to filesystem root")
		}
	}
	return clean, nil
}

func isPortableFilesystemRoot(raw string) bool {
	normalized := strings.ReplaceAll(raw, "\\", "/")
	if normalized == "/" {
		return true
	}
	withoutTrailing := strings.TrimRight(normalized, "/")
	if len(withoutTrailing) == 2 && withoutTrailing[1] == ':' && ((withoutTrailing[0] >= 'A' && withoutTrailing[0] <= 'Z') || (withoutTrailing[0] >= 'a' && withoutTrailing[0] <= 'z')) {
		return true
	}
	if strings.HasPrefix(normalized, "//") {
		parts := strings.FieldsFunc(normalized[2:], func(r rune) bool { return r == '/' })
		return len(parts) <= 2 // UNC server/share root or an incomplete UNC path.
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
