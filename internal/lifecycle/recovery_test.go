package lifecycle_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// fakeContainerLister implements lifecycle.ContainerLister for tests.
type fakeContainerLister struct {
	containers []process.ContainerInfo
	pids       map[string]int // containerID → host PID
}

func (f *fakeContainerLister) ListByLabel(_ string) ([]process.ContainerInfo, error) {
	return f.containers, nil
}

func (f *fakeContainerLister) InspectPID(id string) (int, error) {
	if pid, ok := f.pids[id]; ok {
		return pid, nil
	}
	return 0, fmt.Errorf("container %s not found", id)
}

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

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, nil)

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

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, nil) // must not panic

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

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, nil)

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

func TestRecoverDockerProcesses(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	prx := proxy.New()

	if err := store.CreateUser(db.CreateUserParams{
		Username: "u", PasswordHash: "x", Role: "developer",
	}); err != nil {
		t.Fatal(err)
	}
	user, _ := store.GetUserByUsername("u")
	if err := store.CreateApp(db.CreateAppParams{
		Slug: "docker-app", Name: "Docker App", OwnerID: user.ID,
	}); err != nil {
		t.Fatal(err)
	}
	port := 20500
	pid := 99001
	store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug: "docker-app", Status: "running", Port: &port, PID: &pid,
	})

	lister := &fakeContainerLister{
		containers: []process.ContainerInfo{
			{ID: "cont-abc", Labels: map[string]string{"shinyhub.slug": "docker-app"}},
		},
		pids: map[string]int{"cont-abc": 99001},
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())

	lifecycle.RecoverProcesses(store, mgr, prx, lister)

	info, ok := mgr.Get("docker-app")
	if !ok {
		t.Fatal("expected docker-app to be adopted after recovery")
	}
	if info.Port != port {
		t.Errorf("expected port %d, got %d", port, info.Port)
	}
	if info.PID != pid {
		t.Errorf("expected pid %d, got %d", pid, info.PID)
	}
}

func TestRecoverDockerProcesses_OrphanMarkedStopped(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	prx := proxy.New()

	if err := store.CreateUser(db.CreateUserParams{
		Username: "u2", PasswordHash: "x", Role: "developer",
	}); err != nil {
		t.Fatal(err)
	}
	user, _ := store.GetUserByUsername("u2")

	// Create two apps both marked as running in the DB.
	for _, slug := range []string{"alive-app", "orphan-app"} {
		if err := store.CreateApp(db.CreateAppParams{
			Slug: slug, Name: slug, OwnerID: user.ID,
		}); err != nil {
			t.Fatal(err)
		}
		port := 20600
		pid := 99002
		store.UpdateAppStatus(db.UpdateAppStatusParams{
			Slug: slug, Status: "running", Port: &port, PID: &pid,
		})
	}

	// Only "alive-app" has a running container.
	lister := &fakeContainerLister{
		containers: []process.ContainerInfo{
			{ID: "cont-alive", Labels: map[string]string{"shinyhub.slug": "alive-app"}},
		},
		pids: map[string]int{"cont-alive": 99002},
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())

	lifecycle.RecoverProcesses(store, mgr, prx, lister)

	// "alive-app" should be adopted.
	if _, ok := mgr.Get("alive-app"); !ok {
		t.Error("expected alive-app to be adopted")
	}

	// "orphan-app" should NOT be in the manager.
	if _, ok := mgr.Get("orphan-app"); ok {
		t.Error("expected orphan-app to not be adopted (no container found)")
	}

	// "orphan-app" should be marked stopped in the DB.
	orphan, err := store.GetApp("orphan-app")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if orphan.Status != "stopped" {
		t.Errorf("expected orphan-app status=stopped, got %s", orphan.Status)
	}
}
