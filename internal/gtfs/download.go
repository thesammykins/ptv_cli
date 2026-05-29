package gtfs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const maxGTFSDownloadBytes = 512 << 20

// DownloadResult carries the saved path plus upstream provenance headers used
// to detect when a newer feed has been published.
type DownloadResult struct {
	Path          string
	ETag          string
	LastModified  string
	ContentLength int64
}

// Download fetches the GTFS zip to destDir and returns the file path plus the
// upstream provenance headers (ETag/Last-Modified/Content-Length). It streams
// to disk to avoid holding the (~200MB) archive in memory.
func Download(ctx context.Context, url, destDir string) (DownloadResult, error) {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return DownloadResult{}, err
	}
	dest := filepath.Join(destDir, "gtfs.zip")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("downloading GTFS feed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DownloadResult{}, fmt.Errorf("GTFS download failed: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxGTFSDownloadBytes {
		return DownloadResult{}, fmt.Errorf("GTFS download too large: %d bytes exceeds %d", resp.ContentLength, maxGTFSDownloadBytes)
	}

	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return DownloadResult{}, err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxGTFSDownloadBytes+1))
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return DownloadResult{}, fmt.Errorf("writing GTFS feed: %w", err)
	}
	if n > maxGTFSDownloadBytes {
		f.Close()
		os.Remove(tmp)
		return DownloadResult{}, fmt.Errorf("GTFS download too large: exceeded %d bytes", maxGTFSDownloadBytes)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return DownloadResult{}, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return DownloadResult{}, err
	}
	return DownloadResult{
		Path:          dest,
		ETag:          resp.Header.Get("ETag"),
		LastModified:  resp.Header.Get("Last-Modified"),
		ContentLength: resp.ContentLength,
	}, nil
}
