package gtfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thesammykins/ptv_cli/internal/atomicfile"
)

func TestGenerationUpdateLockIsDeterministicAndRecoverable(t *testing.T) {
	t.Parallel()
	manager, err := NewGenerationManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.AcquireUpdate(t.Context())
	if err != nil {
		t.Fatalf("first AcquireUpdate() error = %v", err)
	}
	if _, err := manager.AcquireUpdate(t.Context()); !errors.Is(err, ErrUpdateInProgress) {
		first.Release()
		t.Fatalf("second AcquireUpdate() error = %v, want ErrUpdateInProgress", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	second, err := manager.AcquireUpdate(t.Context())
	if err != nil {
		t.Fatalf("AcquireUpdate() after release error = %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
}

func TestGenerationPublishOpenAndRollback(t *testing.T) {
	t.Parallel()
	manager, err := NewGenerationManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := manager.AcquireUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	first := newPublishableGeneration(t, lock)
	firstID := first.Ref.ID
	if err := lock.Publish(t.Context(), first); err != nil {
		t.Fatalf("Publish(first) error = %v", err)
	}
	store, ref, err := manager.OpenCurrent(t.Context())
	if err != nil {
		t.Fatalf("OpenCurrent(first) error = %v", err)
	}
	if ref.ID != firstID {
		t.Fatalf("current id = %q, want %q", ref.ID, firstID)
	}
	if _, err := store.PersistedCounts(t.Context()); err != nil {
		t.Fatalf("PersistedCounts(first) error = %v", err)
	}
	defer store.Close()

	second := newPublishableGeneration(t, lock)
	secondID := second.Ref.ID
	if err := lock.Publish(t.Context(), second); err != nil {
		t.Fatalf("Publish(second) error = %v", err)
	}
	manifest, err := manager.ReadManifest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Current.ID != secondID || manifest.Previous == nil || manifest.Previous.ID != firstID {
		t.Fatalf("manifest after second publish = %+v", manifest)
	}
	// Readers hold an immutable generation file, not the manifest pointer. A
	// reader opened before publication must remain usable after a newer
	// generation becomes current.
	if _, err := store.PersistedCounts(t.Context()); err != nil {
		t.Fatalf("pre-publication reader after second publish: %v", err)
	}
	if err := lock.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	manifest, err = manager.ReadManifest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Current.ID != firstID || manifest.Previous == nil || manifest.Previous.ID != secondID {
		t.Fatalf("manifest after rollback = %+v", manifest)
	}
}

func TestGenerationPublishCleansCandidateWhenManifestCommitFails(t *testing.T) {
	t.Parallel()
	manager, err := NewGenerationManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := manager.AcquireUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	first := newPublishableGeneration(t, lock)
	if err := lock.Publish(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := newPublishableGeneration(t, lock)
	secondPath := filepath.Join(manager.generationsDir(), second.Ref.Filename)
	injected := errors.New("injected manifest failure before replace")
	manager.manifestWrite = func(string, []byte, os.FileMode) error { return injected }
	if err := lock.Publish(t.Context(), second); !errors.Is(err, injected) {
		t.Fatalf("Publish() error = %v, want injected failure", err)
	}
	manifest, err := manager.ReadManifest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Current.ID != first.Ref.ID {
		t.Fatalf("current generation = %s, want retained %s", manifest.Current.ID, first.Ref.ID)
	}
	if _, err := os.Stat(secondPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced generation remains after failed commit: %v", err)
	}
}

func TestGenerationPublishTreatsPostReplaceErrorAsCommitted(t *testing.T) {
	t.Parallel()
	manager, err := NewGenerationManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := manager.AcquireUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	first := newPublishableGeneration(t, lock)
	if err := lock.Publish(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := newPublishableGeneration(t, lock)
	injected := errors.New("injected error after manifest replace")
	manager.manifestWrite = func(path string, data []byte, mode os.FileMode) error {
		if err := atomicfile.WriteFile(path, data, mode); err != nil {
			return err
		}
		return injected
	}
	if err := lock.Publish(t.Context(), second); err != nil {
		t.Fatalf("Publish() must recognize the committed manifest: %v", err)
	}
	manifest, err := manager.ReadManifest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Current.ID != second.Ref.ID || manifest.Previous == nil || manifest.Previous.ID != first.Ref.ID {
		t.Fatalf("manifest = %+v, want second current and first previous", manifest)
	}
}

func TestGenerationRejectsUnverifiedCandidateWithoutChangingCurrent(t *testing.T) {
	t.Parallel()
	manager, err := NewGenerationManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := manager.AcquireUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	staging, err := lock.NewStaging(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Close()
	if err := lock.Publish(t.Context(), staging); !errors.Is(err, ErrDatasetStateMissing) {
		t.Fatalf("Publish() error = %v, want ErrDatasetStateMissing", err)
	}
	if _, err := manager.ReadManifest(t.Context()); !errors.Is(err, ErrNoCurrentGeneration) {
		t.Fatalf("ReadManifest() error = %v, want ErrNoCurrentGeneration", err)
	}
}

func TestGenerationRejectsUnresolvedCompatibilityRows(t *testing.T) {
	t.Parallel()
	manager, err := NewGenerationManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := manager.AcquireUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	staging := newPublishableGeneration(t, lock)
	defer staging.Close()
	if _, err := staging.Store.db.ExecContext(t.Context(), `UPDATE trips SET source_trip_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if err := lock.Publish(t.Context(), staging); !errors.Is(err, ErrUnresolvedDataset) {
		t.Fatalf("Publish() error = %v, want ErrUnresolvedDataset", err)
	}
	if _, err := manager.ReadManifest(t.Context()); !errors.Is(err, ErrNoCurrentGeneration) {
		t.Fatalf("ReadManifest() error = %v, want ErrNoCurrentGeneration", err)
	}
}

func TestResolvedDatasetValidationRejectsBrokenTransferReferences(t *testing.T) {
	t.Parallel()
	manager, err := NewGenerationManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := manager.AcquireUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	staging := newPublishableGeneration(t, lock)
	defer staging.Close()
	if _, err := staging.Store.db.ExecContext(t.Context(), `INSERT INTO transfers(
		feed_key,from_stop_key,to_stop_key,from_stop_id,to_stop_id,transfer_type,min_transfer_time,
		from_route_key,from_route_id,source
	) VALUES(1,1,2,'1:a','1:b',2,60,999,'1:missing','gtfs')`); err != nil {
		t.Fatal(err)
	}
	if err := staging.Store.ValidateResolvedDataset(t.Context()); !errors.Is(err, ErrUnresolvedDataset) {
		t.Fatalf("ValidateResolvedDataset() error = %v, want ErrUnresolvedDataset", err)
	}
}

func TestGenerationManagerDetectsLegacyLayout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manager, err := NewGenerationManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gtfs.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReadManifest(t.Context()); !errors.Is(err, ErrLegacyDatabase) {
		t.Fatalf("ReadManifest() error = %v, want ErrLegacyDatabase", err)
	}
}

func newPublishableGeneration(t *testing.T, lock *UpdateLock) *StagingGeneration {
	t.Helper()
	staging, err := lock.NewStaging(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	populateResolvedFixture(t, staging.Store)
	counts, err := staging.Store.ComputeDatasetCounts(t.Context())
	if err != nil {
		staging.Close()
		t.Fatal(err)
	}
	state := DatasetState{
		GenerationID: staging.Ref.ID,
		Provenance: FeedProvenance{
			SourceURL:     "https://example.test/gtfs.zip",
			DeclaredBytes: 100,
			ActualBytes:   100,
		},
		IngestedAt: time.Date(2026, 7, 16, 1, 2, 3, 0, time.UTC),
		Coverage: ServiceCoverage{
			Start: "20260716",
			End:   "20260815",
		},
		Counts: counts,
	}
	if err := staging.Store.SaveDatasetState(t.Context(), state); err != nil {
		staging.Close()
		t.Fatal(err)
	}
	return staging
}
