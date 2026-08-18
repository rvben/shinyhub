package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestPatchAppSettings_Worker(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "patch-owner", "developer")
	app := mustCreateApp(t, s, "iso-patch", owner.ID)

	if _, _, _, _, _, err := s.PatchAppSettings(db.PatchAppSettingsParams{
		Slug:                app.Slug,
		SetWorkerIsolation:  true,
		WorkerIsolation:     "per_session",
		SetWorkerMaxWorkers: true,
		WorkerMaxWorkers:    12,
		SetWorkerWarmSpares: true,
		WorkerWarmSpares:    2,
	}); err != nil {
		t.Fatalf("PatchAppSettings: %v", err)
	}
	got, err := s.GetAppBySlug(app.Slug)
	if err != nil {
		t.Fatalf("GetAppBySlug: %v", err)
	}
	if got.WorkerIsolation != "per_session" || got.WorkerMaxWorkers != 12 || got.WorkerWarmSpares != 2 {
		t.Fatalf("worker fields not persisted: isolation=%q maxWorkers=%d warmSpares=%d", got.WorkerIsolation, got.WorkerMaxWorkers, got.WorkerWarmSpares)
	}
}

func TestApplyAppManifestSettings_Worker(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "manifest-owner", "developer")
	app := mustCreateApp(t, s, "iso-manifest", owner.ID)

	got0, err := s.GetAppBySlug(app.Slug)
	if err != nil {
		t.Fatalf("GetAppBySlug before: %v", err)
	}

	if _, err := s.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID:                        got0.ID,
		Slug:                         app.Slug,
		SetWorkerIsolation:           true,
		WorkerIsolation:              "grouped",
		SetWorkerGroupedSize:         true,
		WorkerGroupedSize:            4,
		SetWorkerMaxWorkers:          true,
		WorkerMaxWorkers:             8,
		SetWorkerWarmSpares:          true,
		WorkerWarmSpares:             2,
		SetWorkerMaxSessionLifetime:  true,
		WorkerMaxSessionLifetimeSecs: 3600,
	}); err != nil {
		t.Fatalf("ApplyAppManifestSettings: %v", err)
	}

	got, err := s.GetAppBySlug(app.Slug)
	if err != nil {
		t.Fatalf("GetAppBySlug after: %v", err)
	}
	if got.WorkerIsolation != "grouped" {
		t.Errorf("WorkerIsolation = %q, want grouped", got.WorkerIsolation)
	}
	if got.WorkerGroupedSize != 4 {
		t.Errorf("WorkerGroupedSize = %d, want 4", got.WorkerGroupedSize)
	}
	if got.WorkerMaxWorkers != 8 {
		t.Errorf("WorkerMaxWorkers = %d, want 8", got.WorkerMaxWorkers)
	}
	if got.WorkerWarmSpares != 2 {
		t.Errorf("WorkerWarmSpares = %d, want 2", got.WorkerWarmSpares)
	}
	if got.WorkerMaxSessionLifetimeSecs != 3600 {
		t.Errorf("WorkerMaxSessionLifetimeSecs = %d, want 3600", got.WorkerMaxSessionLifetimeSecs)
	}
}
