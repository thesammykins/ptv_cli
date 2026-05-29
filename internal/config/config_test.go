package config

import (
	"os"
	"path/filepath"
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
	if err := os.WriteFile(envFile, []byte("PTV_API_KEY=from-dotenv\nPTV_API_USERID=123\nPTV_DATA_DIR=cache\n"), 0o600); err != nil {
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
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"PTV_API_KEY", "PTV_API_USERID", "PTV_BASE_URL", "PTV_GTFS_URL", "PTV_DATA_DIR"} {
		t.Setenv(key, "")
	}
}

func withoutKeyring(t *testing.T) {
	t.Helper()
	original := loadKeyring
	loadKeyring = func() (credstore.Credentials, error) {
		return credstore.Credentials{}, credstore.ErrNotFound
	}
	t.Cleanup(func() { loadKeyring = original })
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
