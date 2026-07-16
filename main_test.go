package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSourceVersionIsSemver(t *testing.T) {
	got := strings.TrimSpace(sourceVersion)
	semver := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	if !semver.MatchString(got) {
		t.Fatalf("VERSION = %q, want X.Y.Z", got)
	}
	if _, err := os.Stat(filepath.Join("docs", "releases", "v"+got+".md")); err != nil {
		t.Fatalf("release notes for VERSION %s: %v", got, err)
	}
}

func TestEffectiveVersion(t *testing.T) {
	tests := []struct {
		name    string
		build   string
		planned string
		want    string
	}{
		{name: "source candidate", build: "dev", planned: "0.5.0\n", want: "0.5.0-dev"},
		{name: "tagged release", build: "0.5.0", planned: "0.5.0\n", want: "0.5.0"},
		{name: "missing planned version", build: "dev", want: "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveVersion(tt.build, tt.planned); got != tt.want {
				t.Fatalf("effectiveVersion(%q, %q) = %q, want %q", tt.build, tt.planned, got, tt.want)
			}
		})
	}
}
