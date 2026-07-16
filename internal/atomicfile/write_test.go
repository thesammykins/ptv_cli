package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileCreatesAndReplacesDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state ?# file.json")
	if err := WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("first WriteFile: %v", err)
	}
	if err := WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("replacement WriteFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("destination = %q, want second", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("destination mode = %o, want private", info.Mode().Perm())
	}
}

func TestReplaceRejectsCrossDirectoryMove(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	source := filepath.Join(first, "source")
	if err := os.WriteFile(source, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Replace(source, filepath.Join(second, "destination")); err == nil {
		t.Fatal("Replace accepted a cross-directory move")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source changed after rejected replace: %v", err)
	}
}
