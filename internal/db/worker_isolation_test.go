package db_test

import "testing"

func TestApp_WorkerIsolationDefaults(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "iso-owner", "developer")
	app := mustCreateApp(t, s, "iso-defaults", owner.ID)
	got, err := s.GetAppBySlug(app.Slug)
	if err != nil {
		t.Fatalf("GetAppBySlug: %v", err)
	}
	if got.WorkerIsolation != "multiplex" {
		t.Errorf("WorkerIsolation = %q, want multiplex", got.WorkerIsolation)
	}
	if got.WorkerGroupedSize != 0 || got.WorkerMaxWorkers != 0 || got.WorkerMaxSessionLifetimeSecs != 0 {
		t.Errorf("worker numeric defaults not zero: %+v", got)
	}
}
