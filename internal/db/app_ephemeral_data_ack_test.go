package db_test

import "testing"

// ephemeral_data_ack is the escape hatch for the durable-data guard: a new app
// defaults to not-acknowledged, and UpdateAppEphemeralDataAck round-trips.

func TestAppEphemeralDataAck_DefaultsFalse(t *testing.T) {
	store := openTestStore(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "myapp", u.ID)
	if app.EphemeralDataAck {
		t.Fatal("new app: want EphemeralDataAck=false, got true")
	}
}

func TestUpdateAppEphemeralDataAck_RoundTrip(t *testing.T) {
	store := openTestStore(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "myapp", u.ID)

	if err := store.UpdateAppEphemeralDataAck(app.ID, true); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.GetAppByID(app.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.EphemeralDataAck {
		t.Fatal("after set true: want EphemeralDataAck=true, got false")
	}

	if err := store.UpdateAppEphemeralDataAck(app.ID, false); err != nil {
		t.Fatalf("update false: %v", err)
	}
	got, err = store.GetAppByID(app.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.EphemeralDataAck {
		t.Fatal("after set false: want EphemeralDataAck=false, got true")
	}
}
