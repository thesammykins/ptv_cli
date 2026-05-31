package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func executeCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	oldOut := os.Stdout
	oldErr := os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout = outW
	os.Stderr = errW

	rootCmd.SetArgs(args)
	rootCmd.SetOut(outW)
	rootCmd.SetErr(errW)
	flagJSON = false
	flagLimit = 0
	flagEnv = ""

	execErr := rootCmd.Execute()

	outW.Close()
	errW.Close()
	os.Stdout = oldOut
	os.Stderr = oldErr

	var stdout, stderr bytes.Buffer
	_, _ = io.Copy(&stdout, outR)
	_, _ = io.Copy(&stderr, errR)
	_ = outR.Close()
	_ = errR.Close()

	return stdout.String(), stderr.String(), execErr
}

func TestVersionJSON(t *testing.T) {
	stdout, stderr, err := executeCommand(t, "--json", "version")
	if err != nil {
		t.Fatalf("version --json: %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if got["version"] == "" || got["commit"] == "" || got["date"] == "" {
		t.Fatalf("missing build fields: %#v", got)
	}
}

func TestGTFSStatusJSONNotIngested(t *testing.T) {
	t.Setenv("PTV_API_KEY", "test-key")
	t.Setenv("PTV_API_USERID", "123")
	t.Setenv("PTV_DATA_DIR", t.TempDir())

	stdout, stderr, err := executeCommand(t, "--json", "gtfs", "status", "--no-update-check")
	if err != nil {
		t.Fatalf("gtfs status --json: %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	var got struct {
		Database string `json:"database"`
		Ingested bool   `json:"ingested"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if got.Ingested {
		t.Fatalf("ingested = true, want false")
	}
	if !strings.HasSuffix(got.Database, "gtfs.sqlite") {
		t.Fatalf("database = %q, want gtfs.sqlite path", got.Database)
	}
}

func TestGTFSUpdateRejectsUnexpectedArgs(t *testing.T) {
	stdout, stderr, err := executeCommand(t, "gtfs", "update", "unexpected")
	if err == nil {
		t.Fatal("expected unexpected arg error")
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want no direct output from Execute", stdout, stderr)
	}
}

func TestGTFSRealtimeCatalogJSON(t *testing.T) {
	t.Setenv("PTV_API_KEY", "test-key")
	t.Setenv("PTV_API_USERID", "123")
	t.Setenv("PTV_DATA_DIR", t.TempDir())

	stdout, stderr, err := executeCommand(t, "--json", "gtfs", "realtime")
	if err != nil {
		t.Fatalf("gtfs realtime --json: %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	var got struct {
		Feeds []struct {
			ID   string `json:"id"`
			Mode string `json:"mode"`
			Kind string `json:"kind"`
			URL  string `json:"url"`
		} `json:"feeds"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if len(got.Feeds) < 9 {
		t.Fatalf("feeds = %d, want GTFS-R catalog", len(got.Feeds))
	}
	if got.Feeds[0].ID == "" || got.Feeds[0].URL == "" {
		t.Fatalf("first feed missing fields: %+v", got.Feeds[0])
	}
}

func TestAuthStatusPropagatesInvalidConfig(t *testing.T) {
	t.Setenv("PTV_BASE_URL", "http://example.com")
	stdout, stderr, err := executeCommand(t, "auth", "status")
	if err == nil {
		t.Fatal("expected invalid config error")
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want no direct output from Execute", stdout, stderr)
	}
}

func TestUnknownCommandReturnsErrorWithoutUsage(t *testing.T) {
	stdout, stderr, err := executeCommand(t, "definitely-not-a-command")
	if err == nil {
		t.Fatal("expected error")
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want no direct output from Execute", stdout, stderr)
	}
	if strings.Contains(err.Error(), "Usage:") {
		t.Fatalf("error includes usage despite SilenceUsage: %v", err)
	}
}
