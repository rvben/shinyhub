package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestLatestAutoscaleEvent_NoRows(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)

	_, found, err := store.LatestAutoscaleEvent("demo")
	if err != nil {
		t.Fatalf("LatestAutoscaleEvent: %v", err)
	}
	if found {
		t.Fatal("want found=false for an app with no autoscale events")
	}
}

func TestLatestAutoscaleEvent_ReturnsLatest(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)

	// Seed an older scale_up event, then a newer scale_down event.
	store.LogAuditEvent(db.AuditEventParams{
		Action: "autoscale_scale_up", ResourceType: "app", ResourceID: "demo",
		Detail: `{"from":1,"to":2}`,
	})
	// Small sleep so the two rows have distinct created_at.
	time.Sleep(2 * time.Millisecond)
	store.LogAuditEvent(db.AuditEventParams{
		Action: "autoscale_scale_down", ResourceType: "app", ResourceID: "demo",
		Detail: `{"from":2,"to":1}`,
	})

	event, found, err := store.LatestAutoscaleEvent("demo")
	if err != nil {
		t.Fatalf("LatestAutoscaleEvent: %v", err)
	}
	if !found {
		t.Fatal("want found=true after seeding two events")
	}
	if event.Action != "autoscale_scale_down" {
		t.Fatalf("action = %q, want autoscale_scale_down (the newer event)", event.Action)
	}
}

func TestLatestAutoscaleEvent_IgnoresOtherApps(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "app-a", u.ID)
	mustCreateApp(t, store, "app-b", u.ID)

	// Seed an event only for app-b.
	store.LogAuditEvent(db.AuditEventParams{
		Action: "autoscale_scale_up", ResourceType: "app", ResourceID: "app-b",
		Detail: `{"from":1,"to":2}`,
	})

	_, found, err := store.LatestAutoscaleEvent("app-a")
	if err != nil {
		t.Fatalf("LatestAutoscaleEvent: %v", err)
	}
	if found {
		t.Fatal("want found=false: app-a has no events; app-b's event must not appear")
	}
}

func TestLatestAutoscaleEvent_IgnoresNonAutoscaleActions(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)

	// Seed a deploy event; it is not an autoscale event.
	store.LogAuditEvent(db.AuditEventParams{
		Action: "deploy", ResourceType: "app", ResourceID: "demo",
		Detail: `{}`,
	})

	_, found, err := store.LatestAutoscaleEvent("demo")
	if err != nil {
		t.Fatalf("LatestAutoscaleEvent: %v", err)
	}
	if found {
		t.Fatal("want found=false: deploy event must not be returned as autoscale event")
	}
}
