package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestApp_AutoscaleDefaultsOffOnCreate(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)

	if app.AutoscaleEnabled {
		t.Fatalf("new app AutoscaleEnabled = true, want false")
	}
	if app.AutoscaleMinReplicas != 0 || app.AutoscaleMaxReplicas != 0 {
		t.Fatalf("new app autoscale min/max = %d/%d, want 0/0",
			app.AutoscaleMinReplicas, app.AutoscaleMaxReplicas)
	}
	if app.AutoscaleTarget != 0 {
		t.Fatalf("new app AutoscaleTarget = %v, want 0", app.AutoscaleTarget)
	}
}

func TestSetAppAutoscale_RoundTrip(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)

	if err := store.SetAppAutoscale(db.SetAppAutoscaleParams{
		AppID:       app.ID,
		Enabled:     true,
		MinReplicas: 2,
		MaxReplicas: 8,
		Target:      0.75,
	}); err != nil {
		t.Fatalf("SetAppAutoscale: %v", err)
	}

	got, err := store.GetApp("demo")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if !got.AutoscaleEnabled {
		t.Fatalf("AutoscaleEnabled = false, want true")
	}
	if got.AutoscaleMinReplicas != 2 || got.AutoscaleMaxReplicas != 8 {
		t.Fatalf("autoscale min/max = %d/%d, want 2/8",
			got.AutoscaleMinReplicas, got.AutoscaleMaxReplicas)
	}
	if got.AutoscaleTarget != 0.75 {
		t.Fatalf("AutoscaleTarget = %v, want 0.75", got.AutoscaleTarget)
	}
}

func TestListAutoscaleApps_ReturnsEnabledRunningOrDegradedOnly(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")

	cases := []struct {
		slug, status string
		enabled      bool
		want         bool
	}{
		{"on-running", "running", true, true},
		{"on-degraded", "degraded", true, true},
		{"on-stopped", "stopped", true, false},   // enabled but not actionable
		{"off-running", "running", false, false}, // running but not opted in
		{"on-hibernated", "hibernated", true, false},
	}
	for _, tc := range cases {
		app := mustCreateApp(t, store, tc.slug, u.ID)
		if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: tc.slug, Status: tc.status}); err != nil {
			t.Fatalf("status %q: %v", tc.slug, err)
		}
		if tc.enabled {
			if err := store.SetAppAutoscale(db.SetAppAutoscaleParams{
				AppID: app.ID, Enabled: true, MinReplicas: 1, MaxReplicas: 4, Target: 0.8,
			}); err != nil {
				t.Fatalf("enable %q: %v", tc.slug, err)
			}
		}
	}

	apps, err := store.ListAutoscaleApps()
	if err != nil {
		t.Fatalf("ListAutoscaleApps: %v", err)
	}
	got := make(map[string]bool, len(apps))
	for _, a := range apps {
		got[a.Slug] = true
		if !a.AutoscaleEnabled {
			t.Errorf("slug %q returned with AutoscaleEnabled=false", a.Slug)
		}
	}
	for _, tc := range cases {
		if got[tc.slug] != tc.want {
			t.Errorf("slug %q (status %q enabled %v): present=%v, want %v",
				tc.slug, tc.status, tc.enabled, got[tc.slug], tc.want)
		}
	}
}

func TestSetAppAutoscale_DisableClears(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)

	if err := store.SetAppAutoscale(db.SetAppAutoscaleParams{
		AppID: app.ID, Enabled: true, MinReplicas: 2, MaxReplicas: 8, Target: 0.5,
	}); err != nil {
		t.Fatalf("SetAppAutoscale enable: %v", err)
	}
	if err := store.SetAppAutoscale(db.SetAppAutoscaleParams{
		AppID: app.ID, Enabled: false, MinReplicas: 2, MaxReplicas: 8, Target: 0.5,
	}); err != nil {
		t.Fatalf("SetAppAutoscale disable: %v", err)
	}
	got, err := store.GetApp("demo")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.AutoscaleEnabled {
		t.Fatalf("AutoscaleEnabled = true after disable, want false")
	}
}
