package lifecycle_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestRecoverProcesses_DeadPID(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate()

	store.CreateUser(db.CreateUserParams{Username: "u", PasswordHash: "h", Role: "admin"})
	u, _ := store.GetUserByUsername("u")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Set a PID that definitely doesn't exist.
	port, pid := 20001, 99999999
	store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "myapp", Status: "running", Port: &port, PID: &pid})

	mgr := process.NewManager(t.TempDir())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx)

	// App should now be stopped in the DB.
	app, _ := store.GetAppBySlug("myapp")
	if app.Status != "stopped" {
		t.Errorf("expected status=stopped after recovery of dead PID, got %s", app.Status)
	}
}

func TestRecoverProcesses_NoPID(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate()

	store.CreateUser(db.CreateUserParams{Username: "u", PasswordHash: "h", Role: "admin"})
	u, _ := store.GetUserByUsername("u")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Simulate status=running with no PID (corrupted state).
	store.DB().Exec(`UPDATE apps SET status='running' WHERE slug='myapp'`)

	mgr := process.NewManager(t.TempDir())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx) // must not panic

	app, _ := store.GetAppBySlug("myapp")
	if app.Status != "stopped" {
		t.Errorf("expected stopped, got %s", app.Status)
	}
}
