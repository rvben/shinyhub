package db_test

import "testing"

func TestRenderSeconds_RoundTrips(t *testing.T) {
	s := mustOpenDB(t)
	owner := mustCreateUser(t, s, "owner", "admin")
	app := mustCreateApp(t, s, "render-app", owner.ID)

	// Default is 0: absent means disabled, never a coerced value.
	got, err := s.GetAppByID(app.ID)
	if err != nil {
		t.Fatalf("GetAppByID: %v", err)
	}
	if got.RenderSeconds != 0 {
		t.Fatalf("default RenderSeconds = %v, want 0", got.RenderSeconds)
	}

	// A set value round-trips through the setter without precision loss.
	if err := s.SetAppRenderSeconds(app.ID, 1.3); err != nil {
		t.Fatalf("SetAppRenderSeconds: %v", err)
	}
	reread, err := s.GetAppByID(app.ID)
	if err != nil {
		t.Fatalf("GetAppByID reread: %v", err)
	}
	if reread.RenderSeconds != 1.3 {
		t.Fatalf("RenderSeconds after set = %v, want 1.3", reread.RenderSeconds)
	}
}
