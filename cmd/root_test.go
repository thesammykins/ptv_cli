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
