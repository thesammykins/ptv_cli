package gtfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type AutoUpdateConfig struct {
	Enabled       bool
	BlockOnEmpty  bool
	BlockOnGap    bool
	BlockTimeout  time.Duration
	DataDir       string
	SourceURL     string
	RequestedDate *time.Time
}

type AutoUpdateResult struct {
	Triggered  bool   `json:"triggered"`
	Background bool   `json:"background"`
	State      string `json:"state"`
	Message    string `json:"message"`
}

// CheckAndAutoUpdate evaluates the existing freshness state and, when a
// generation is available, starts the current executable as a worker. The
// worker owns AcquireUpdate; no goroutine or second lock protocol is used.
func CheckAndAutoUpdate(ctx context.Context, cfg AutoUpdateConfig) (*Store, AutoUpdateResult, error) {
	if cfg.DataDir == "" {
		return nil, AutoUpdateResult{}, errors.New("GTFS data directory is empty")
	}
	manager, err := NewGenerationManager(cfg.DataDir)
	if err != nil {
		return nil, AutoUpdateResult{}, err
	}
	store, _, openErr := manager.OpenCurrent(ctx)
	if openErr != nil {
		if errors.Is(openErr, ErrNoCurrentGeneration) && cfg.BlockOnEmpty {
			return nil, AutoUpdateResult{State: "failed", Message: "no GTFS data is available; run 'ptv gtfs update'"}, openErr
		}
		return nil, AutoUpdateResult{State: "failed"}, openErr
	}
	state, err := store.DatasetState(ctx)
	if err != nil {
		store.Close()
		return nil, AutoUpdateResult{}, err
	}
	if !cfg.Enabled {
		return store, AutoUpdateResult{State: "no_update_needed"}, nil
	}
	report, checkErr := CheckFreshness(ctx, FreshnessRequest{DataDir: cfg.DataDir, Dataset: state, SourceURL: cfg.SourceURL, AllowNetwork: true, RequestedAt: time.Now().UTC()})
	if checkErr != nil {
		return store, AutoUpdateResult{State: "unknown", Message: "GTFS freshness check unavailable"}, nil
	}
	if report.State != FreshnessChanged && !(report.State == FreshnessStale && report.UpdateAvailable) {
		return store, AutoUpdateResult{State: "current"}, nil
	}
	if err := startUpdateWorker(cfg); err != nil {
		return store, AutoUpdateResult{State: "failed", Message: "GTFS background update could not be started"}, nil
	}
	return store, AutoUpdateResult{Triggered: true, Background: true, State: "updating", Message: "updating GTFS data in background; results may be stale until next invocation"}, nil
}

func startUpdateWorker(cfg AutoUpdateConfig) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, "gtfs", "update", "--background-worker")
	command.Env = append(os.Environ(), "PTV_DATA_DIR="+cfg.DataDir, "PTV_GTFS_URL="+cfg.SourceURL, "PTV_GTFS_BACKGROUND_WORKER=1")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Start()
}

type UpdateProgress struct {
	State        string `json:"state"`
	Percent      int    `json:"percent,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
	FailedAt     string `json:"failed_at,omitempty"`
	GenerationID string `json:"generation_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

func UpdateProgressPath(dataDir string) string {
	return filepath.Join(dataDir, ".update.progress.json")
}
func WriteUpdateProgress(dataDir string, progress UpdateProgress) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	tmp := UpdateProgressPath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, UpdateProgressPath(dataDir)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publishing update progress: %w", err)
	}
	return nil
}
