package lifecycle_test

import (
	"os"
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
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	if err := store.CreateUser(db.CreateUserParams{Username: "u", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("u")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}

	// Set a PID that definitely doesn't exist.
	port, pid := 20001, 99999999
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "myapp", Status: "running", Port: &port, PID: &pid}); err != nil {
		t.Fatal(err)
	}

	mgr := process.NewManager(t.TempDir())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx)

	// App should now be stopped in the DB.
	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
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
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	if err := store.CreateUser(db.CreateUserParams{Username: "u", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("u")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}

	// Simulate status=running with no PID (corrupted state).
	store.DB().Exec(`UPDATE apps SET status='running' WHERE slug='myapp'`)

	mgr := process.NewManager(t.TempDir())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx) // must not panic

	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "stopped" {
		t.Errorf("expected stopped, got %s", app.Status)
	}
}

func TestRecoverProcesses_AlivePID(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	if err := store.CreateUser(db.CreateUserParams{Username: "u", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("u")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}

	port, pid := 20002, os.Getpid() // current test process is guaranteed alive
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "myapp", Status: "running", Port: &port, PID: &pid}); err != nil {
		t.Fatal(err)
	}

	mgr := process.NewManager(t.TempDir())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx)

	// App should still be running in the DB.
	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "running" {
		t.Errorf("expected status=running for alive PID, got %s", app.Status)
	}

	// Manager should have the entry.
	info, ok := mgr.Get("myapp")
	if !ok {
		t.Error("expected manager to have myapp after recovery")
	} else if info.PID != pid {
		t.Errorf("expected PID %d in manager, got %d", pid, info.PID)
	}
}
