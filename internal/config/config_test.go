package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thesammykins/ptv_cli/internal/credstore"
)

func TestLoadDoesNotReadWorkingDirectoryDotEnv(t *testing.T) {
	clearConfigEnv(t)
	withoutKeyring(t)

	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PTV_API_KEY=from-dotenv\nPTV_API_USERID=123\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		return
	}
	if cfg.CredentialSource == SourceDotEnv || cfg.APIKey == "from-dotenv" {
		t.Fatalf("Load used cwd .env credentials: %#v", cfg)
	}
}

func TestLoadWithOptionsReadsExplicitEnvFile(t *testing.T) {
	clearConfigEnv(t)
	withoutKeyring(t)

	envFile := filepath.Join(t.TempDir(), "ptv.env")
	if err := os.WriteFile(envFile, []byte("PTV_API_KEY=from-dotenv\nPTV_API_USERID=123\nPTV_DATA_DIR=cache\nPTV_OPENDATA_KEY_ID=subscription\nPTV_OPENDATA_API_ID=platform-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadWithOptions(LoadOptions{EnvFile: envFile})
	if err != nil {
		t.Fatalf("LoadWithOptions: %v", err)
	}
	if cfg.CredentialSource == SourceDotEnv && (cfg.APIKey != "from-dotenv" || cfg.DevID != "123") {
		t.Fatalf("explicit env file credentials not loaded: %#v", cfg)
	}
	if cfg.CredentialSource != SourceDotEnv && cfg.APIKey == "from-dotenv" {
		t.Fatalf("explicit env file secret loaded with wrong source: %#v", cfg)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("DataDir = %q, want absolute path", cfg.DataDir)
	}
	if cfg.OpenDataKeyID != "subscription" {
		t.Fatalf("OpenDataKeyID = %q, want explicit env file value", cfg.OpenDataKeyID)
	}
	if cfg.OpenDataAPIID != "platform-token" {
		t.Fatalf("OpenDataAPIID = %q, want explicit env file value", cfg.OpenDataAPIID)
	}
}

func TestLoadRuntimeDoesNotRequireOrConsultCredentials(t *testing.T) {
	clearConfigEnv(t)
	originalKeyring := loadKeyring
	originalOpenData := loadOpenDataKeyring
	ptvCalls := 0
	openDataCalls := 0
	loadKeyring = func() (credstore.Credentials, error) {
		ptvCalls++
		return credstore.Credentials{}, credstore.ErrNotFound
	}
	loadOpenDataKeyring = func() (credstore.OpenDataCredentials, error) {
		openDataCalls++
		return credstore.OpenDataCredentials{}, credstore.ErrNotFound
	}
	t.Cleanup(func() {
		loadKeyring = originalKeyring
		loadOpenDataKeyring = originalOpenData
	})

	cfg, err := LoadRuntime()
	if err != nil {
		t.Fatalf("LoadRuntime: %v", err)
	}
	if cfg.BaseURL == "" || cfg.GTFSURL == "" || cfg.GeocoderURL == "" || cfg.GeocoderProvider == "" || cfg.DataDir == "" {
		t.Fatalf("LoadRuntime returned incomplete config: %#v", cfg)
	}
	if ptvCalls != 0 || openDataCalls != 0 {
		t.Fatalf("LoadRuntime consulted credential stores: ptv=%d openData=%d", ptvCalls, openDataCalls)
	}
}

func TestLoadRuntimeReadsOnlyExplicitDotEnv(t *testing.T) {
	clearConfigEnv(t)
	envFile := filepath.Join(t.TempDir(), "runtime.env")
	if err := os.WriteFile(envFile, []byte("PTV_GTFS_URL=http://localhost:8080/gtfs.zip\nPTV_GEOCODER_URL=http://127.0.0.1:8081/search\nPTV_GEOCODER_PROVIDER=Local geocoder\nPTV_GEOCODER_ATTRIBUTION=Local data\nPTV_DATA_DIR=cache\nPTV_API_KEY=ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRuntimeWithOptions(LoadOptions{EnvFile: envFile})
	if err != nil {
		t.Fatalf("LoadRuntimeWithOptions: %v", err)
	}
	if cfg.GTFSURL != "http://localhost:8080/gtfs.zip" {
		t.Fatalf("GTFSURL = %q", cfg.GTFSURL)
	}
	if cfg.GeocoderURL != "http://127.0.0.1:8081/search" {
		t.Fatalf("GeocoderURL = %q", cfg.GeocoderURL)
	}
	if cfg.GeocoderProvider != "Local geocoder" || cfg.GeocoderAttribution != "Local data" {
		t.Fatalf("geocoder identity = %q / %q", cfg.GeocoderProvider, cfg.GeocoderAttribution)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("DataDir = %q, want absolute", cfg.DataDir)
	}
}

func TestLoadPTVCredentialsMissingDoesNotAffectRuntime(t *testing.T) {
	clearConfigEnv(t)
	withoutKeyring(t)
	if _, err := LoadPTVCredentials(); !errors.Is(err, ErrMissingCredentials) {
		t.Fatalf("LoadPTVCredentials error = %v, want ErrMissingCredentials", err)
	}
	if _, err := LoadRuntime(); err != nil {
		t.Fatalf("LoadRuntime with missing credentials: %v", err)
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"PTV_API_KEY", "PTV_API_USERID", "PTV_BASE_URL", "PTV_GTFS_URL", "PTV_GEOCODER_URL", "PTV_GEOCODER_PROVIDER", "PTV_GEOCODER_ATTRIBUTION", "PTV_DATA_DIR", "PTV_OPENDATA_KEY_ID", "PTV_OPENDATA_KEYID", "PTV_OPENDATA_API_ID"} {
		t.Setenv(key, "")
	}
}

func withoutKeyring(t *testing.T) {
	t.Helper()
	original := loadKeyring
	originalOpenData := loadOpenDataKeyring
	loadKeyring = func() (credstore.Credentials, error) {
		return credstore.Credentials{}, credstore.ErrNotFound
	}
	loadOpenDataKeyring = func() (credstore.OpenDataCredentials, error) {
		return credstore.OpenDataCredentials{}, credstore.ErrNotFound
	}
	t.Cleanup(func() {
		loadKeyring = original
		loadOpenDataKeyring = originalOpenData
	})
}

func TestLoadReadsOpenDataCredentialsFromKeyring(t *testing.T) {
	clearConfigEnv(t)
	originalKeyring := loadKeyring
	originalOpenData := loadOpenDataKeyring
	loadKeyring = func() (credstore.Credentials, error) {
		return credstore.Credentials{APIKey: "api-key", DevID: "123"}, nil
	}
	loadOpenDataKeyring = func() (credstore.OpenDataCredentials, error) {
		return credstore.OpenDataCredentials{KeyID: "subscription", APIID: "platform-token"}, nil
	}
	t.Cleanup(func() {
		loadKeyring = originalKeyring
		loadOpenDataKeyring = originalOpenData
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenDataKeyID != "subscription" {
		t.Fatalf("OpenDataKeyID = %q, want keyring value", cfg.OpenDataKeyID)
	}
	if cfg.OpenDataAPIID != "platform-token" {
		t.Fatalf("OpenDataAPIID = %q, want keyring value", cfg.OpenDataAPIID)
	}
}

func TestOpenDataEnvironmentOverridesKeyring(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PTV_OPENDATA_KEY_ID", "env-subscription")
	t.Setenv("PTV_OPENDATA_API_ID", "env-platform-token")
	originalKeyring := loadKeyring
	originalOpenData := loadOpenDataKeyring
	loadKeyring = func() (credstore.Credentials, error) {
		return credstore.Credentials{APIKey: "api-key", DevID: "123"}, nil
	}
	loadOpenDataKeyring = func() (credstore.OpenDataCredentials, error) {
		return credstore.OpenDataCredentials{KeyID: "keyring-subscription", APIID: "keyring-platform-token"}, nil
	}
	t.Cleanup(func() {
		loadKeyring = originalKeyring
		loadOpenDataKeyring = originalOpenData
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenDataKeyID != "env-subscription" {
		t.Fatalf("OpenDataKeyID = %q, want environment value", cfg.OpenDataKeyID)
	}
	if cfg.OpenDataAPIID != "env-platform-token" {
		t.Fatalf("OpenDataAPIID = %q, want environment value", cfg.OpenDataAPIID)
	}
}

func TestLoadRejectsNonHTTPSRemoteURLs(t *testing.T) {
	t.Setenv("PTV_API_KEY", "key")
	t.Setenv("PTV_API_USERID", "123")
	t.Setenv("PTV_BASE_URL", "http://example.com")

	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with non-HTTPS remote URL")
	}
}

func TestLoadAllowsHTTPForLocalhost(t *testing.T) {
	t.Setenv("PTV_API_KEY", "key")
	t.Setenv("PTV_API_USERID", "123")
	t.Setenv("PTV_BASE_URL", "http://127.0.0.1:8080")

	if _, err := Load(); err != nil {
		t.Fatalf("Load rejected localhost HTTP URL: %v", err)
	}
}

func TestURLValidationErrorDoesNotEchoCredentialBearingInput(t *testing.T) {
	const raw = "://source-user:source-pass@example.test/feed?token=source-token"
	err := validateHTTPSURL("PTV_GTFS_URL", raw)
	if err == nil {
		t.Fatal("validateHTTPSURL() error = nil, want invalid URL")
	}
	for _, secret := range []string{"source-user", "source-pass", "source-token"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("validation error leaked %q: %v", secret, err)
		}
	}
}

func TestValidateDataDirRejectsPortableRoots(t *testing.T) {
	for _, value := range []string{"/", `C:\\`, "D:/", `\\\\server\\share`, "//server/share/"} {
		t.Run(value, func(t *testing.T) {
			if _, err := validateDataDir(value); err == nil {
				t.Fatalf("validateDataDir(%q) succeeded", value)
			}
		})
	}
}

func TestValidateDataDirAllowsPortableSubdirectories(t *testing.T) {
	for _, value := range []string{`C:\\ptv`, "D:/ptv", `\\\\server\\share\\ptv`} {
		t.Run(value, func(t *testing.T) {
			if _, err := validateDataDir(value); err != nil {
				t.Fatalf("validateDataDir(%q): %v", value, err)
			}
		})
	}
}

func TestValidateDataDirRejectsSymlinkToRoot(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "root-link")
	if err := os.Symlink(string(filepath.Separator), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := validateDataDir(link); err == nil {
		t.Fatalf("validateDataDir accepted symlink to root")
	}
}
