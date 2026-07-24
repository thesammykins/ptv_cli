package gtfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteUpdateProgressPublishesOneAtomicRecord(t *testing.T) {
	dataDir := t.TempDir()
	want := UpdateProgress{State: "downloading", Percent: 42, StartedAt: "2026-07-24T01:02:03Z"}
	if err := WriteUpdateProgress(dataDir, want); err != nil {
		t.Fatalf("WriteUpdateProgress() error = %v", err)
	}
	contents, err := os.ReadFile(UpdateProgressPath(dataDir))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var got UpdateProgress
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatalf("progress JSON = %q: %v", contents, err)
	}
	if got != want {
		t.Fatalf("progress = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".update.progress.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary progress file still exists: %v", err)
	}
}
