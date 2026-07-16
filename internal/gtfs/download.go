package gtfs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/thesammykins/ptv_cli/internal/atomicfile"
)

const maxGTFSDownloadBytes = 512 << 20

// DownloadResult carries the saved archive and the exact provenance needed by
// DatasetState. ContentLength is the upstream declaration and may be -1;
// ActualBytes is always the number copied and checked locally.
type DownloadResult struct {
	Path          string
	SourceURL     string
	ETag          string
	LastModified  string
	ContentLength int64
	ActualBytes   int64
	DownloadedAt  time.Time
}

// Download fetches the GTFS archive with a bounded, context-aware client.
func Download(ctx context.Context, sourceURL, destDir string) (DownloadResult, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	return DownloadWithClient(ctx, client, sourceURL, destDir)
}

// DownloadWithClient is the injectable form used by contract tests. It writes
// a unique private temporary file and only replaces gtfs.zip after a complete
// bounded download has been synced.
func DownloadWithClient(ctx context.Context, client *http.Client, sourceURL, destDir string) (result DownloadResult, err error) {
	if client == nil {
		return DownloadResult{}, fmt.Errorf("GTFS download HTTP client is nil")
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return DownloadResult{}, fmt.Errorf("creating GTFS download directory: %w", err)
	}
	dest := filepath.Join(destDir, "gtfs.zip")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("creating GTFS download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("downloading GTFS feed: %w", sanitizeSourceError(err, sourceURL))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DownloadResult{}, fmt.Errorf("GTFS download failed: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxGTFSDownloadBytes {
		return DownloadResult{}, fmt.Errorf("GTFS download too large: %d bytes exceeds %d", resp.ContentLength, maxGTFSDownloadBytes)
	}

	tmp, err := os.CreateTemp(destDir, ".gtfs-*.zip.tmp")
	if err != nil {
		return DownloadResult{}, fmt.Errorf("creating GTFS download file: %w", err)
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return DownloadResult{}, fmt.Errorf("securing GTFS download file: %w", err)
	}

	n, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, maxGTFSDownloadBytes+1))
	if copyErr != nil {
		tmp.Close()
		return DownloadResult{}, fmt.Errorf("writing GTFS feed: %w", copyErr)
	}
	if n > maxGTFSDownloadBytes {
		tmp.Close()
		return DownloadResult{}, fmt.Errorf("GTFS download too large: exceeded %d bytes", maxGTFSDownloadBytes)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		tmp.Close()
		return DownloadResult{}, fmt.Errorf("GTFS download length mismatch: received %d bytes, expected %d", n, resp.ContentLength)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return DownloadResult{}, fmt.Errorf("syncing GTFS download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return DownloadResult{}, fmt.Errorf("closing GTFS download: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return DownloadResult{}, err
	}
	if err := atomicfile.Replace(tmpPath, dest); err != nil {
		return DownloadResult{}, fmt.Errorf("publishing GTFS download: %w", err)
	}
	remove = false

	return DownloadResult{
		Path:          dest,
		SourceURL:     sourceURL,
		ETag:          resp.Header.Get("ETag"),
		LastModified:  resp.Header.Get("Last-Modified"),
		ContentLength: resp.ContentLength,
		ActualBytes:   n,
		DownloadedAt:  time.Now().UTC(),
	}, nil
}

// Provenance returns the typed metadata to persist with the generation built
// from this download.
func (r DownloadResult) Provenance() FeedProvenance {
	return FeedProvenance{
		SourceURL:     r.SourceURL,
		ETag:          r.ETag,
		LastModified:  r.LastModified,
		DeclaredBytes: r.ContentLength,
		ActualBytes:   r.ActualBytes,
	}
}
