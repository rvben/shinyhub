package db_test

import (
	"testing"
)

func TestSetAppPlacement_RoundTrip(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)

	if err := store.SetAppPlacement(app.ID, `{"local":1,"burst":2}`, 3); err != nil {
		t.Fatalf("SetAppPlacement: %v", err)
	}
	got, err := store.GetApp("demo")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.ReplicaPlacement != `{"local":1,"burst":2}` {
		t.Fatalf("ReplicaPlacement = %q", got.ReplicaPlacement)
	}
	if got.Replicas != 3 {
		t.Fatalf("Replicas total = %d, want 3", got.Replicas)
	}
}

func TestSetAppPlacement_DefaultEmptyOnCreate(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	if app.ReplicaPlacement != "" {
		t.Fatalf("new app placement = %q, want empty", app.ReplicaPlacement)
	}
}
