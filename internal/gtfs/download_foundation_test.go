package gtfs

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDownloadWithClientPersistsActualProvenance(t *testing.T) {
	t.Parallel()
	const body = "bounded feed"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("ETag", `"feed-1"`)
		header.Set("Last-Modified", "Thu, 16 Jul 2026 00:00:00 GMT")
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: -1,
			Header:        header,
			Request:       request,
		}, nil
	})}
	result, err := DownloadWithClient(t.Context(), client, "https://example.test/gtfs.zip", t.TempDir())
	if err != nil {
		t.Fatalf("DownloadWithClient() error = %v", err)
	}
	if result.ActualBytes != int64(len(body)) || result.ContentLength != -1 {
		t.Fatalf("download sizes = actual %d declared %d", result.ActualBytes, result.ContentLength)
	}
	if result.SourceURL != "https://example.test/gtfs.zip" || result.ETag != `"feed-1"` {
		t.Fatalf("download provenance = %+v", result)
	}
	if result.DownloadedAt.IsZero() {
		t.Fatal("DownloadedAt is zero")
	}
	file := mustOpen(t, result.Path)
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body {
		t.Fatalf("download body = %q", data)
	}
	provenance := result.Provenance()
	if provenance.ActualBytes != result.ActualBytes || provenance.DeclaredBytes != result.ContentLength {
		t.Fatalf("Provenance() = %+v", provenance)
	}
}

func TestDownloadWithClientRejectsContentLengthMismatch(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader("short")),
			ContentLength: 100,
			Header:        make(http.Header),
			Request:       request,
		}, nil
	})}
	dir := t.TempDir()
	_, err := DownloadWithClient(t.Context(), client, "https://example.test/gtfs.zip", dir)
	if err == nil {
		t.Fatal("DownloadWithClient() error = nil, want content-length mismatch")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "gtfs.zip")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed download published gtfs.zip: %v", statErr)
	}
}

func TestDownloadWithClientRedactsCredentialBearingSourceFromTransportError(t *testing.T) {
	t.Parallel()
	const sourceURL = "https://source-user:source-pass@example.test/private-path-token/gtfs.zip?token=source-token"
	var requestedURL string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestedURL = request.URL.String()
		return nil, errors.New("dial failed for " + sourceURL)
	})}

	_, err := DownloadWithClient(t.Context(), client, sourceURL, t.TempDir())
	if err == nil {
		t.Fatal("DownloadWithClient() error = nil, want transport error")
	}
	if requestedURL != sourceURL {
		t.Fatalf("request URL = %q, want exact configured URL", requestedURL)
	}
	for _, secret := range []string{"source-user", "source-pass", "source-token", "private-path-token"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("download error leaked %q: %v", secret, err)
		}
	}
}

func mustOpen(t *testing.T, path string) io.ReadCloser {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
