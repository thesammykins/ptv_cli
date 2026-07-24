package cmd

import (
	"strings"
	"testing"
)

func TestFormatTrackRealtimeTimeUsesMelbourneZone(t *testing.T) {
	got := formatTrackRealtimeTime(1782877140)
	if !strings.Contains(got, "+10:00") {
		t.Fatalf("formatted time = %q, want Melbourne offset", got)
	}
}
