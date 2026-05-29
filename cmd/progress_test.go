package cmd

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Len()
}

func TestProgressDisabledDoesNotWrite(t *testing.T) {
	var buf lockedBuffer
	p := &progress{
		w:        &buf,
		messages: []string{"Planning your trip"},
		frames:   []string{"🚆"},
		delay:    0,
		interval: time.Millisecond,
		enabled:  false,
	}

	p.Start()
	p.Stop()

	if got := buf.String(); got != "" {
		t.Fatalf("progress output = %q, want empty", got)
	}
}

func TestProgressWritesAndClears(t *testing.T) {
	var buf lockedBuffer
	p := &progress{
		w:        &buf,
		messages: []string{"Planning your trip"},
		frames:   []string{"🚆"},
		delay:    0,
		interval: time.Millisecond,
		enabled:  true,
	}

	p.Start()
	waitForProgressOutput(t, &buf)
	p.Stop()

	got := buf.String()
	if !strings.Contains(got, "Planning your trip") {
		t.Fatalf("progress output = %q, want message", got)
	}
	if !strings.Contains(got, "\r\033[2K") {
		t.Fatalf("progress output = %q, want line clear", got)
	}
}

func TestProgressStopIsIdempotent(t *testing.T) {
	var buf lockedBuffer
	p := &progress{
		w:        &buf,
		messages: []string{"Planning your trip"},
		frames:   []string{"🚆"},
		delay:    time.Hour,
		interval: time.Millisecond,
		enabled:  true,
	}

	p.Start()
	p.Stop()
	p.Stop()
}

func waitForProgressOutput(t *testing.T, buf *lockedBuffer) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if buf.Len() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("progress did not write before deadline")
}
